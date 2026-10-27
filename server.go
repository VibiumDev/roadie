package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/VibiumDev/roadie/rtc"
	"html"
	"image"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Server holds the state needed by HTTP handlers.
type Server struct {
	Source         FrameSource
	Buf            *FrameBuffer
	Device         string
	AudioBroadcast *AudioBroadcaster
	// AudioSource says which of the phone's outputs /audio is following,
	// "usb" or "hdmi", when there is a choice (the gadget backend); nil
	// otherwise.
	AudioSource func() string
	SourceType  string // "hardware" or "http"
	HID         HIDBackend
	Capture     *CaptureManager
	// RTC sends the picture over WebRTC; nil when this host has no way to
	// encode H.264 or frames come from an HTTP source.
	RTC *rtc.Broadcaster
	// Auth decides who may use the pages and API; see --auth.
	Auth *Auth

	// InputMode is the pointer mode this target wants — "mouse", "touch", or
	// empty to leave the choice to the viewer. It belongs to the server
	// because it is a property of the device on the other end of the cable,
	// which the browser has no way to know.
	InputMode string

	// Platform is the target's OS, which decides how it reads absolute
	// pointer coordinates and what input mode suits it. Empty means
	// unspecified.
	Platform Platform

	// Name is this instance's --name. It labels the browser tab so that
	// several targets open at once stay tellable apart.
	Name string

	// CropFile is where saved crops are kept between runs; empty keeps
	// them only until roadie stops.
	CropFile string
	cropMu   sync.Mutex

	// turn is which way the phone takes itself to be held (see rotate.go).
	turn phoneTurn
}

// pageTitle names the browser tab after the target this instance drives. The
// default name says nothing the product name does not, so it is left off.
func (s *Server) pageTitle() string {
	if s.Name == "" || s.Name == "roadie" {
		return "Roadie"
	}
	return "Roadie — " + s.Name
}

// effectiveInputMode is the mode viewers actually start in: --input when
// given, otherwise whatever the platform implies. Reported by /health so the
// answer is visible rather than inferred from two flags.
func (s *Server) effectiveInputMode() string {
	if s.InputMode != "" {
		return s.InputMode
	}
	return s.Platform.DefaultInputMode()
}

// NewMux wires up all HTTP routes and returns a handler.
func NewMux(s *Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/view", s.handleView)
	mux.HandleFunc("/grid", s.handleGrid)
	mux.HandleFunc("/stream", s.handleStream)
	mux.HandleFunc("/snapshot", s.handleSnapshot)
	mux.HandleFunc("/raw-stream", s.handleRawStream)
	mux.HandleFunc("/raw-snapshot", s.handleRawSnapshot)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/settings", s.handleSettings)
	mux.HandleFunc("/api/settings", s.handleAPISettings)
	mux.HandleFunc("/api/crop", s.handleAPICrop)
	mux.HandleFunc("/audio", s.handleAudio)
	mux.HandleFunc("/api/mic", s.handleMic)
	mux.HandleFunc("/test", s.handleTest)
	mux.HandleFunc("/soundcheck", s.handleSoundcheck)
	mux.HandleFunc("/gestures", s.handleGestures)
	mux.HandleFunc("/api/capture/reset", s.handleCaptureReset)
	mux.HandleFunc("/api/hid/release", s.handleHIDRelease)
	mux.HandleFunc("/api/usb/reset", s.handleUSBReset)
	mux.HandleFunc("/api/rotate", s.handleRotate)
	mux.HandleFunc("/api/relay/reset", s.handleRelayReset)
	mux.HandleFunc("/api/hid/type", s.handleHIDType)
	mux.HandleFunc("/api/hid/key", s.handleHIDKey)
	mux.HandleFunc("/api/hid/mouse/move", s.handleHIDMouseMove)
	mux.HandleFunc("/api/hid/mouse/click", s.handleHIDMouseClick)
	mux.HandleFunc("/api/hid/mouse/scroll", s.handleHIDMouseScroll)
	mux.HandleFunc("/api/hid/touch", s.handleHIDTouch)
	mux.HandleFunc("/api/hid/status", s.handleHIDStatus)
	mux.HandleFunc("/api/hid/ws", s.handleHIDWebSocket)
	mux.HandleFunc("/session", s.handleBiDi)
	mux.HandleFunc("/api/webrtc/signal", s.handleWebRTCSignal)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/viewer/webrtc.js", s.handleViewerJS)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, "<!DOCTYPE html>\n<html>\n<head><title>"+html.EscapeString(s.pageTitle())+"</title>")
	fmt.Fprint(w, `
<style>
body { font-family: monospace; max-width: 600px; margin: 40px auto; padding: 0 20px; }
a { color: #0066cc; }
li { padding: 8px 0; font-size: 1.1em; }
</style>
</head>
<body>
<h1>Roadie</h1>
<ul>
<li><a href="/view">/view</a> — watch the live feed in your browser</li>
<li><a href="/stream">/stream</a> — MJPEG stream (auto-cropped)</li>
<li><a href="/snapshot">/snapshot</a> — single frame (auto-cropped JPEG)</li>
<li><a href="/raw-stream">/raw-stream</a> — MJPEG stream (uncropped)</li>
<li><a href="/raw-snapshot">/raw-snapshot</a> — single frame (uncropped JPEG)</li>
<li><a href="/health">/health</a> — service status (JSON)</li>
<li><a href="/settings">/settings</a> — adjust quality and view device info</li>
<li><a href="/test">/test</a> — test HID mouse and keyboard control</li>
</ul>
</body>
</html>`)
}

func (s *Server) handleView(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, "<!DOCTYPE html>\n<html>\n<head><title>"+html.EscapeString(s.pageTitle())+"</title>")
	fmt.Fprint(w, `<link rel="icon" href="data:,">
<style>
  :root { --page-bg:#1a1a1a; --kbd-bg:#222; --kbd-key:rgba(255,255,255,0.12); --kbd-key-active:rgba(255,255,255,0.25); --kbd-text:#fff; --kbd-border:transparent; }
  :root[data-theme="light"] { --page-bg:#e8e8ed; --kbd-bg:#d4d4d9; --kbd-key:rgba(255,255,255,0.85); --kbd-key-active:rgba(255,255,255,0.55); --kbd-text:#1a1a1a; --kbd-border:rgba(0,0,0,0.08); }
  @media (prefers-color-scheme:light) { :root[data-theme="system"] { --page-bg:#e8e8ed; --kbd-bg:#d4d4d9; --kbd-key:rgba(255,255,255,0.85); --kbd-key-active:rgba(255,255,255,0.55); --kbd-text:#1a1a1a; --kbd-border:rgba(0,0,0,0.08); } }
  /* The toolbar and its menu, in the same three themes. */
  :root { --ui-btn:rgba(50,50,50,0.9); --ui-btn-border:rgba(255,255,255,0.15); --ui-icon:#ccc; --ui-panel:rgba(50,50,50,0.95); --ui-text:#fff; --ui-muted:#aaa; --ui-ctl:#333; --ui-ctl-text:#888; --ui-ctl-border:#555; --ui-ctl-on:#444; --ui-ctl-on-text:#fff; --ui-accent:#6af; --ui-btn2:#444; --ui-btn2-border:#666; --ui-danger:#633; --ui-danger-border:#955; --ui-danger-text:#fff; --ui-divider:rgba(255,255,255,0.1); --ui-error:#f99; color-scheme:dark; }
  :root[data-theme="light"] { --ui-btn:rgba(255,255,255,0.9); --ui-btn-border:rgba(0,0,0,0.15); --ui-icon:#444; --ui-panel:rgba(255,255,255,0.97); --ui-text:#1a1a1a; --ui-muted:#666; --ui-ctl:#f2f2f5; --ui-ctl-text:#777; --ui-ctl-border:#c9c9d0; --ui-ctl-on:#fff; --ui-ctl-on-text:#111; --ui-accent:#2f7fe0; --ui-btn2:#fff; --ui-btn2-border:#bbb; --ui-danger:#fbe3e3; --ui-danger-border:#e0a0a0; --ui-danger-text:#8b1e1e; --ui-divider:rgba(0,0,0,0.1); --ui-error:#c0392b; color-scheme:light; }
  @media (prefers-color-scheme:light) { :root[data-theme="system"] { --ui-btn:rgba(255,255,255,0.9); --ui-btn-border:rgba(0,0,0,0.15); --ui-icon:#444; --ui-panel:rgba(255,255,255,0.97); --ui-text:#1a1a1a; --ui-muted:#666; --ui-ctl:#f2f2f5; --ui-ctl-text:#777; --ui-ctl-border:#c9c9d0; --ui-ctl-on:#fff; --ui-ctl-on-text:#111; --ui-accent:#2f7fe0; --ui-btn2:#fff; --ui-btn2-border:#bbb; --ui-danger:#fbe3e3; --ui-danger-border:#e0a0a0; --ui-danger-text:#8b1e1e; --ui-divider:rgba(0,0,0,0.1); --ui-error:#c0392b; color-scheme:light; } }
  .tbtn { background:var(--ui-btn); border:1px solid var(--ui-btn-border); color:var(--ui-icon); }
  .tbtn.live { background:rgba(180,40,40,0.95); color:#fff; }
  .tbtn.busy { animation:tbbusy 1.2s ease-in-out infinite; cursor:progress; }
  /* While Roadie turns the screen: the picture greyed, a note at its top
     (the menu Roadie clicks is in the middle), and input held back (see
     setTurning). */
  #feed { transition:filter 0.2s; }
  #feed.turning { cursor:progress !important; filter:grayscale(0.7) brightness(0.7); }
  #turnNote { position:absolute; display:none; justify-content:center; align-items:flex-start;
    pointer-events:none; z-index:9; }
  #turnNote span { margin-top:16px; padding:8px 14px; border-radius:6px; background:rgba(0,0,0,0.75); color:#fff;
    font-family:monospace; font-size:0.85em; }
  @keyframes tbbusy { 50% { opacity:0.35; } }
  #qslider { background:var(--ui-panel); border:1px solid var(--ui-btn-border); color:var(--ui-text); }
  #qslider select { background:var(--ui-ctl); color:var(--ui-text); border:1px solid var(--ui-ctl-border); }
  .seg button { background:var(--ui-ctl); color:var(--ui-ctl-text); border:1px solid var(--ui-ctl-border); }
  .seg button.on { background:var(--ui-ctl-on); color:var(--ui-ctl-on-text); border-color:var(--ui-accent); }
  .btn2 { background:var(--ui-btn2); color:var(--ui-ctl-on-text); border:1px solid var(--ui-btn2-border); }
  .danger { background:var(--ui-danger); color:var(--ui-danger-text); border:1px solid var(--ui-danger-border); }
  .qhelp { background:var(--ui-ctl); color:var(--ui-icon); border:1px solid var(--ui-btn2-border); }
  .qhelp[aria-expanded="true"] { background:var(--ui-ctl-on); color:var(--ui-ctl-on-text); }
  .hint { color:var(--ui-muted); }
  .err { color:var(--ui-error); }
  .divider { border-top:1px solid var(--ui-divider); }
  #overlay { position:absolute; inset:0; background:rgba(0,0,0,0.85); color:#fff; font-family:monospace; font-size:1.2em;
    justify-content:center; align-items:center; text-align:center; z-index:10; }
  /* No signal: a message over the picture, which stays visible (dark, in
     the target's outline) instead of being covered. */
  #overlay.notice { background:transparent; align-items:flex-start; pointer-events:none; }
  #overlay.notice span { margin-top:16px; padding:8px 14px; border-radius:6px; background:rgba(0,0,0,0.75);
    font-size:0.85em; max-width:80%; }
  #toolbarNote { position:fixed; right:12px; bottom:12px; z-index:30; max-width:260px; padding:8px 12px; border-radius:6px;
    background:var(--ui-panel); color:var(--ui-text); border:1px solid var(--ui-btn-border); font-family:monospace; font-size:0.85em;
    transition:opacity 0.4s; }
  /* Auto-hide: the toolbar tucks off the right edge when not wanted, and
     floats over the picture when the picture needs its column. */
  #toolbar { position:relative; flex-direction:column; gap:4px; padding:8px 6px; transition:transform 0.15s ease-out; }
  #toolbar.floating { position:fixed; z-index:20; }
  #toolbar.tucked { transform:translateX(100%); }
  /* The side the toolbar sits on: a column at the right (the default) or
     left, a row at the top or bottom from the left-hand corner. */
  body.tb-left #toolbar, body.tb-top #toolbar { order:-1; }
  body.tb-top, body.tb-bottom { flex-direction:column; }
  body.tb-top #toolbar, body.tb-bottom #toolbar { flex-direction:row; justify-content:flex-start; padding:6px 8px; }
  body.tb-right #toolbar.floating { right:0; top:0; }
  body.tb-left #toolbar.floating { left:0; top:0; }
  body.tb-top #toolbar.floating { top:0; left:0; }
  body.tb-bottom #toolbar.floating { bottom:0; left:0; }
  body.tb-left #toolbar.tucked { transform:translateX(-100%); }
  body.tb-top #toolbar.tucked { transform:translateY(-100%); }
  body.tb-bottom #toolbar.tucked { transform:translateY(100%); }
  body { background:var(--page-bg); }
  @keyframes spin { to { transform:rotate(360deg); } }
  #onscreen-kbd .kr { display:flex; gap:3px; margin-bottom:3px; justify-content:center; }
  #onscreen-kbd .kr:last-child { margin-bottom:0; }
  #onscreen-kbd .kk { width:40px; height:40px; flex-shrink:0; background:var(--kbd-key); color:var(--kbd-text); border:1px solid var(--kbd-border); border-radius:5px; font-family:-apple-system,BlinkMacSystemFont,sans-serif; font-size:12px; font-weight:400; cursor:pointer; display:flex; align-items:center; justify-content:center; padding:0; }
  #onscreen-kbd .kk:active, #onscreen-kbd .kk.pressed { background:var(--kbd-key-active); }
  #onscreen-kbd .kk.mod-active { background:var(--kbd-key-active); box-shadow:0 0 0 1.5px rgba(255,255,255,0.5); }
  #onscreen-kbd .kk.blank { visibility:hidden; }
  #onscreen-kbd .kk.space { width:255px; }
  #onscreen-kbd .kgap { width:12px; flex-shrink:0; }
  .numpad { display:none; }
  @media (min-width:1200px) { .numpad { display:flex; } }
</style>`)

	// minimal=1 hides the toolbar so the page embeds cleanly in an iframe (see
	// /grid). The controls stay in the DOM rather than being omitted, so the
	// page's scripts — which look them up by id — keep working untouched.
	if r.URL.Query().Get("minimal") == "1" {
		fmt.Fprint(w, `
<style>#toolbar { display:none !important; }</style>`)
	}

	// input=mouse|touch preselects the pointer mode for this page load. The
	// toggle lives in the toolbar, so a minimal view has no other way to
	// reach it — and the stored preference is per origin, so it does not
	// carry from one instance to another. Emitted as fixed literals, never
	// interpolated, so nothing from the query string reaches the script.
	// Pointer mode: ?input= for this page load, otherwise the target's own
	// --input. Either beats the viewer's stored preference, which cannot be
	// read as a deliberate choice — until recently the page rewrote that key
	// on every load, so most browsers hold a value nobody picked.
	mode := r.URL.Query().Get("input")
	if mode != "touch" && mode != "mouse" {
		mode = s.effectiveInputMode()
	}
	switch mode {
	case "touch":
		fmt.Fprint(w, "\n<script>window.__roadieInputMode='touch';</script>")
	case "mouse":
		fmt.Fprint(w, "\n<script>window.__roadieInputMode='mouse';</script>")
	}

	// A target that addresses its own display needs the crop left out of the
	// pointer mapping; see Platform.PointerSpace. Only the non-default value
	// is emitted, so pages for every other target are unchanged.
	if s.Platform.PointerSpace() == "screen" {
		fmt.Fprint(w, "\n<script>window.__roadiePointerSpace='screen';</script>")
	}

	// The target's platform: a phone gets its own buttons (Home and the
	// like), and screenshots are named for it. Fixed literals only.
	switch s.Platform {
	case PlatformIOS, PlatformAndroid, PlatformMac, PlatformWindows, PlatformLinux:
		fmt.Fprintf(w, "\n<script>window.__roadiePlatform='%s';</script>", s.Platform)
	}

	// With the Pi as the phone's USB devices there are no boards to reset:
	// the menu offers one reset that replugs them all.
	if s.HID != nil && s.HID.Backend() == BackendLinuxGadget {
		fmt.Fprint(w, "\n<script>window.__roadieHIDBackend='linux-gadget';</script>")
	}
	// What a USB reset replugs, for the menu's explanation of it.
	if d := s.usbDevices(); d != nil {
		b, _ := json.Marshal(d)
		fmt.Fprintf(w, "\n<script>window.__roadieUSBDevices=%s;</script>", b)
	}
	// WebRTC: H.264 over a peer connection instead of MJPEG, when this host
	// can encode it. The menu switches between the two and this browser
	// remembers the choice; ?transport=webrtc or mjpeg sets it for this page
	// load. The page falls back to MJPEG by itself if the connection fails.
	// Fixed literals only, nothing from the query reaches the page.
	webrtcOffered := s.RTC != nil && s.RTC.Available()
	if webrtcOffered {
		fmt.Fprint(w, "\n<script>window.__roadieWebRTC=true;</script>")
		switch r.URL.Query().Get("transport") {
		case "webrtc":
			fmt.Fprint(w, "\n<script>window.__roadieTransport='webrtc';</script>")
		case "mjpeg":
			fmt.Fprint(w, "\n<script>window.__roadieTransport='mjpeg';</script>")
		}
	}

	fmt.Fprint(w, `
</head>
<body class="tb-right" style="margin:0; display:flex; height:100vh; overflow:hidden; overscroll-behavior:none;">
  <div style="flex:1; min-width:0; min-height:0; display:flex; flex-direction:column;">
    <div id="viewer" style="position:relative; flex:1; min-width:0; min-height:0; overflow:hidden; user-select:none; -webkit-user-select:none;">
      <div id="overlay" style="display:flex;">
        Connecting&hellip;
      </div>
      <video id="rtcVideo" autoplay playsinline muted disablepictureinpicture style="position:absolute; display:none; pointer-events:none; background:#000;"></video>
      <div id="rtcStats" style="position:absolute; left:8px; bottom:8px; display:none; z-index:11; padding:4px 7px; border-radius:4px; background:rgba(0,0,0,0.6); color:#fff; font:12px monospace; pointer-events:none; white-space:pre;"></div>
      <img id="feed" draggable="false" oncontextmenu="return false" style="max-width:100%; max-height:100%; display:none; touch-action:none; cursor:crosshair; position:relative; z-index:1;">
      <div id="turnNote"><span>Rotating the screen&hellip;</span></div>
    </div>
    <div id="onscreen-kbd" style="display:none; user-select:none; -webkit-user-select:none; overflow-x:auto;">
      <div id="kbd-inner" style="padding:6px 8px;">
      <div id="kbd-body" style="background:var(--kbd-bg); border-radius:8px; padding:6px; display:inline-block;">
      <div class="kr" id="kr-0"></div>
      <div class="kr" id="kr-1"></div>
      <div class="kr" id="kr-2"></div>
      <div class="kr" id="kr-3"></div>
      <div class="kr" id="kr-4"></div>
      <div class="kr" id="kr-5"></div>
      </div>
      </div>
    </div>
  </div>
  <div id="toolbar" style="display:flex;">
    <style>#toolbar button { outline:none; } #toolbar button:focus { outline:none; }</style>
    <button id="qbtn" class="tbtn" style="width:36px; height:36px; border-radius:6px; cursor:pointer; padding:0; display:flex; align-items:center; justify-content:center;" title="Settings"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M9.5 13a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0m0-5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0m0-5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0"/></svg></button>
    <button id="unmute" class="tbtn" style="width:36px; height:36px; border-radius:6px; cursor:pointer; padding:0; display:none; align-items:center; justify-content:center;" title="Toggle audio"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M6.717 3.55A.5.5 0 0 1 7 4v8a.5.5 0 0 1-.812.39L3.825 10.5H1.5A.5.5 0 0 1 1 10V6a.5.5 0 0 1 .5-.5h2.325l2.363-1.89a.5.5 0 0 1 .529-.06m7.137 2.096a.5.5 0 0 1 0 .708L12.207 8l1.647 1.646a.5.5 0 0 1-.708.708L11.5 8.707l-1.646 1.647a.5.5 0 0 1-.708-.708L10.793 8 9.146 6.354a.5.5 0 1 1 .708-.708L11.5 7.293l1.646-1.647a.5.5 0 0 1 .708 0"/></svg></button>
    <button id="volUpBtn" class="tbtn" style="width:36px; height:36px; border-radius:6px; cursor:pointer; padding:0; display:none; align-items:center; justify-content:center;" title="Volume up"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M6.717 3.55A.5.5 0 0 1 7 4v8a.5.5 0 0 1-.812.39L3.825 10.5H1.5A.5.5 0 0 1 1 10V6a.5.5 0 0 1 .5-.5h2.325l2.363-1.89a.5.5 0 0 1 .529-.06"/><path d="M12 5.5a.5.5 0 0 1 .5.5v1.5H14a.5.5 0 0 1 0 1h-1.5V10a.5.5 0 0 1-1 0V8.5H10a.5.5 0 0 1 0-1h1.5V6a.5.5 0 0 1 .5-.5"/></svg></button>
    <button id="volDownBtn" class="tbtn" style="width:36px; height:36px; border-radius:6px; cursor:pointer; padding:0; display:none; align-items:center; justify-content:center;" title="Volume down"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M6.717 3.55A.5.5 0 0 1 7 4v8a.5.5 0 0 1-.812.39L3.825 10.5H1.5A.5.5 0 0 1 1 10V6a.5.5 0 0 1 .5-.5h2.325l2.363-1.89a.5.5 0 0 1 .529-.06"/><path d="M10 7.5h4a.5.5 0 0 1 0 1h-4a.5.5 0 0 1 0-1"/></svg></button>
    <button id="ptt" class="tbtn" style="width:36px; height:36px; border-radius:6px; cursor:pointer; padding:0; display:flex; align-items:center; justify-content:center;" title="Toggle microphone"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M5 3a3 3 0 0 1 6 0v5a3 3 0 0 1-6 0z"/><path d="M3.5 6.5A.5.5 0 0 1 4 7v1a4 4 0 0 0 8 0V7a.5.5 0 0 1 1 0v1a5 5 0 0 1-4.5 4.975V15h3a.5.5 0 0 1 0 1h-7a.5.5 0 0 1 0-1h3v-2.025A5 5 0 0 1 3 8V7a.5.5 0 0 1 .5-.5"/></svg></button>
    <button id="kbdBtn" class="tbtn" style="width:36px; height:36px; border-radius:6px; cursor:pointer; padding:0; display:flex; align-items:center; justify-content:center; opacity:0.4;" title="On-screen keyboard"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M0 6a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v5a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2zm13 .25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25h-.5a.25.25 0 0 0-.25.25M2.25 8a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h.5A.25.25 0 0 0 3 8.75v-.5A.25.25 0 0 0 2.75 8zM4 8.25v.5c0 .138.112.25.25.25h.5A.25.25 0 0 0 5 8.75v-.5A.25.25 0 0 0 4.75 8h-.5a.25.25 0 0 0-.25.25M6.25 8a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h.5A.25.25 0 0 0 7 8.75v-.5A.25.25 0 0 0 6.75 8zM8 8.25v.5c0 .138.112.25.25.25h.5A.25.25 0 0 0 9 8.75v-.5A.25.25 0 0 0 8.75 8h-.5a.25.25 0 0 0-.25.25M13.25 8a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25zm0 2a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25zm-3-2a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h1.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25zm.75 2.25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25h-.5a.25.25 0 0 0-.25.25M11.25 6a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25zM9 6.25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5A.25.25 0 0 0 9.75 6h-.5a.25.25 0 0 0-.25.25M7.25 6a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h.5A.25.25 0 0 0 8 6.75v-.5A.25.25 0 0 0 7.75 6zM5 6.25v.5c0 .138.112.25.25.25h.5A.25.25 0 0 0 6 6.75v-.5A.25.25 0 0 0 5.75 6h-.5a.25.25 0 0 0-.25.25M2.25 6a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h1.5A.25.25 0 0 0 4 6.75v-.5A.25.25 0 0 0 3.75 6zM2 10.25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25h-.5a.25.25 0 0 0-.25.25M4.25 10a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h5.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25z"/></svg></button>
    <button id="backBtn" class="tbtn" style="width:36px; height:36px; border-radius:6px; cursor:pointer; padding:0; display:none; align-items:center; justify-content:center;" title="Back (Esc)"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M11.5 2.8v10.4a.5.5 0 0 1-.77.42L3.4 8.42a.5.5 0 0 1 0-.84l7.33-5.2a.5.5 0 0 1 .77.42"/></svg></button>
    <button id="homeBtn" class="tbtn" style="width:36px; height:36px; border-radius:6px; cursor:pointer; padding:0; display:none; align-items:center; justify-content:center;" title="Home"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M8 .8.6 7.3l.8.9.6-.5V15h4.5v-4h3v4H14V7.7l.6.5.8-.9z"/></svg></button>
    <button id="recentsBtn" class="tbtn" style="width:36px; height:36px; border-radius:6px; cursor:pointer; padding:0; display:none; align-items:center; justify-content:center;" title="Recent apps (Alt+Tab)"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path fill-rule="evenodd" d="M3 3.5a.5.5 0 0 1 .5-.5h9a.5.5 0 0 1 .5.5v9a.5.5 0 0 1-.5.5h-9a.5.5 0 0 1-.5-.5zm1.5 1v7h7v-7z"/></svg></button>
    <button id="rotateBtn" class="tbtn" style="width:36px; height:36px; border-radius:6px; cursor:pointer; padding:0; display:none; align-items:center; justify-content:center;" title="Rotate"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path fill-rule="evenodd" d="M8 3a5 5 0 1 0 4.546 2.914.5.5 0 0 1 .908-.417A6 6 0 1 1 8 2z"/><path d="M8 4.466V.534a.25.25 0 0 1 .41-.192l2.36 1.966c.12.1.12.284 0 .384L8.41 4.658A.25.25 0 0 1 8 4.466"/><rect x="5.9" y="5.1" width="4.2" height="5.8" rx="0.7" fill="none" stroke="currentColor" stroke-width="1.1"/></svg></button>
    <button id="lockBtn" class="tbtn" style="width:36px; height:36px; border-radius:6px; cursor:pointer; padding:0; display:none; align-items:center; justify-content:center;" title="Lock"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"></svg></button>
    <button id="shotBtn" class="tbtn" style="width:36px; height:36px; border-radius:6px; cursor:pointer; padding:0; display:flex; align-items:center; justify-content:center;" title="Save a screenshot"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M10.5 8.5a2.5 2.5 0 1 1-5 0 2.5 2.5 0 0 1 5 0"/><path d="M2 4a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V6a2 2 0 0 0-2-2h-1.17a2 2 0 0 1-1.42-.59l-.82-.82A2 2 0 0 0 9.17 2H6.83a2 2 0 0 0-1.42.59l-.82.82A2 2 0 0 1 3.17 4zm6 1.5a3 3 0 1 1 0 6 3 3 0 0 1 0-6"/></svg></button>
    <div id="qslider" style="display:none; position:absolute; top:8px; z-index:21; border-radius:8px; padding:10px 14px; font-family:monospace; font-size:0.85em; white-space:nowrap;">
      <div id="videoRow" style="display:none; align-items:center; gap:8px; margin-bottom:8px;">
        <label style="min-width:55px;">Video</label>
        <div class="seg" style="display:flex; gap:0;">
          <button id="videoMJPEGBtn" style="padding:2px 10px; border-radius:4px 0 0 4px; font-family:monospace; cursor:pointer;">MJPEG</button>
          <button id="videoWebRTCBtn" style="padding:2px 10px; border-radius:0 4px 4px 0; font-family:monospace; cursor:pointer;">WebRTC</button>
        </div>
        <label style="cursor:pointer; display:flex; align-items:center; gap:4px;" title="Frame rate, bitrate and delays over the picture"><input id="videoStatsCheck" type="checkbox"> Stats</label>
      </div>
      <div id="qualityRow" style="display:flex; align-items:center; gap:8px; margin-bottom:8px;">
        <label style="min-width:55px;" title="JPEG quality of the MJPEG stream">Quality</label>
        <input id="qrange" type="range" min="30" max="95" style="width:120px; vertical-align:middle;">
        <span id="qval" style="min-width:2em; text-align:right;"></span>
      </div>
      <div id="bitrateRow" style="display:none; align-items:center; gap:8px; margin-bottom:8px;" title="The most the H.264 stream may use; auto lets the connection decide, up to 4 Mbps">
        <label style="min-width:55px;">Bitrate</label>
        <input id="bitrateRange" type="range" min="300" max="4000" step="100" value="4000" style="width:120px; vertical-align:middle;">
        <span id="bitrateVal" style="min-width:3em; text-align:right;">auto</span>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-bottom:8px;">
        <label style="min-width:55px;">FPS</label>
        <select id="fpsSelect" style="border-radius:4px; padding:2px 6px; font-family:monospace;">
          <option value="10">10</option>
          <option value="15">15</option>
          <option value="20">20</option>
          <option value="30">30</option>
        </select>
      </div>
      <div style="display:flex; align-items:center; gap:8px;">
        <label style="min-width:55px;">Size</label>
        <select id="resSelect" style="border-radius:4px; padding:2px 6px; font-family:monospace;">
          <option value="1920x1080">1080p</option>
          <option value="1280x720">720p</option>
          <option value="640x480">480p</option>
        </select>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Input</label>
        <div class="seg" style="display:flex; gap:0;">
          <button id="modeMouseBtn" style="padding:2px 10px; border-radius:4px 0 0 4px; font-family:monospace; cursor:pointer;">Mouse</button>
          <button id="modeTouchBtn" style="padding:2px 10px; border-radius:0 4px 4px 0; font-family:monospace; cursor:pointer;">Touch</button>
        </div>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;" title="How far the wheel scrolls the target">
        <label style="min-width:55px;">Scroll</label>
        <input id="scrollRange" type="range" min="25" max="200" step="5" value="100" style="width:120px; vertical-align:middle;">
        <span id="scrollVal" style="min-width:3em; text-align:right;">100%</span>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Keys</label>
        <label style="cursor:pointer; display:flex; align-items:center; gap:4px;"><input id="kbdCaptureCheck" type="checkbox" checked> Capture</label>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Kbd Size</label>
        <input id="kbdZoomRange" type="range" min="50" max="200" value="100" style="width:120px; vertical-align:middle;">
        <span id="kbdZoomVal" style="min-width:3em; text-align:right;">100%</span>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Crop</label>
        <div class="seg" style="display:flex; gap:0;">
          <button id="cropAutoBtn" title="Trim whatever is black at the edges" style="padding:2px 10px; border-radius:4px 0 0 4px; font-family:monospace; cursor:pointer;">Auto</button>
          <button id="cropOffBtn" title="Show the whole frame" style="padding:2px 10px; border-radius:0; font-family:monospace; cursor:pointer;">Off</button>
          <button id="cropSavedBtn" title="Use the crop saved for the way the device is turned" style="padding:2px 10px; border-radius:0 4px 4px 0; font-family:monospace; cursor:pointer;">Saved</button>
        </div>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin:6px 0 0 63px;">
        <button id="cropSaveBtn" title="Save the crop of what is on screen now, for the way the device is turned. Show something bright to its edges first, such as a white page." class="btn2" style="padding:2px 10px; border-radius:4px; font-family:monospace; cursor:pointer;">Save</button>
        <button id="cropClearBtn" title="Forget the saved crops" class="btn2" style="padding:2px 10px; border-radius:4px; font-family:monospace; cursor:pointer;">Clear</button>
      </div>
      <div id="cropStatus" class="err" hidden style="margin:4px 0 0 63px; font-size:0.9em; max-width:260px; white-space:normal;"></div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Zoom</label>
        <input id="zoomRange" type="range" min="50" max="400" value="100" style="width:120px; vertical-align:middle;">
        <span id="zoomVal" style="min-width:3em; text-align:right;">100%</span>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Align</label>
        <div class="seg" style="display:flex; gap:0;">
          <button id="alignTLBtn" style="padding:2px 10px; border-radius:4px 0 0 4px; font-family:monospace; cursor:pointer;">Top-Left</button>
          <button id="alignCenterBtn" style="padding:2px 10px; border-radius:0 4px 4px 0; font-family:monospace; cursor:pointer;">Center</button>
        </div>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Theme</label>
        <div class="seg" style="display:flex; gap:0;">
          <button id="themeDarkBtn" style="padding:2px 10px; border-radius:4px 0 0 4px; font-family:monospace; cursor:pointer;">Dark</button>
          <button id="themeLightBtn" style="padding:2px 10px; border-radius:0; font-family:monospace; cursor:pointer;">Light</button>
          <button id="themeSystemBtn" style="padding:2px 10px; border-radius:0 4px 4px 0; font-family:monospace; cursor:pointer;">System</button>
        </div>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;" title="JPEG quality of the Screenshot button's pictures">
        <label style="min-width:55px;">Screenshot</label>
        <input id="shotQualityRange" type="range" min="30" max="95" value="90" style="width:120px; vertical-align:middle;">
        <span id="shotQualityVal" style="min-width:2em; text-align:right;">90</span>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px; padding-top:8px;" class="divider">
        <label style="min-width:55px;">Resets</label>
        <button id="resetHelpBtn" aria-controls="resetHelp" aria-expanded="false" title="What each reset does" class="qhelp" style="width:20px; height:20px; padding:0; border-radius:50%; font-family:monospace; font-size:0.85em; cursor:pointer;">?</button>
      </div>
      <div id="resetHelp" class="hint" hidden style="margin:6px 0 0 63px; font-size:0.9em; max-width:260px; white-space:normal;"></div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Video</label>
        <button id="resetVideoBtn" class="danger" style="padding:2px 10px; border-radius:4px; font-family:monospace; cursor:pointer; position:relative; overflow:hidden;"></button>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label id="resetHIDLabel" style="min-width:55px;">HID</label>
        <button id="resetHIDBtn" class="danger" style="padding:2px 10px; border-radius:4px; font-family:monospace; cursor:pointer; position:relative; overflow:hidden;"></button>
      </div>
      <div id="resetRelayRow" style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Relay</label>
        <button id="resetRelayBtn" class="danger" style="padding:2px 10px; border-radius:4px; font-family:monospace; cursor:pointer; position:relative; overflow:hidden;"></button>
      </div>
      <div class="divider" style="display:flex; align-items:center; gap:8px; margin-top:8px; padding-top:8px;">
        <label style="min-width:55px;">Toolbar</label>
        <div class="seg" style="display:flex; gap:0;">
          <button id="tbShowBtn" title="Always show the toolbar" style="padding:2px 10px; border-radius:4px 0 0 4px; font-family:monospace; cursor:pointer;">Show</button>
          <button id="tbAutoBtn" title="Tuck the toolbar off the right edge; it comes back when the pointer reaches the edge" style="padding:2px 10px; border-radius:0; font-family:monospace; cursor:pointer;">Auto-hide</button>
          <button id="tbOffBtn" title="No toolbar. Bring it back with ?toolbar=show in the address, or on /settings" style="padding:2px 10px; border-radius:0 4px 4px 0; font-family:monospace; cursor:pointer;">Off</button>
        </div>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Side</label>
        <div class="seg" style="display:flex; gap:0;">
          <button id="tbSideTop" style="padding:2px 10px; border-radius:4px 0 0 4px; font-family:monospace; cursor:pointer;">Top</button>
          <button id="tbSideRight" style="padding:2px 10px; border-radius:0; font-family:monospace; cursor:pointer;">Right</button>
          <button id="tbSideBottom" style="padding:2px 10px; border-radius:0; font-family:monospace; cursor:pointer;">Bottom</button>
          <button id="tbSideLeft" style="padding:2px 10px; border-radius:0 4px 4px 0; font-family:monospace; cursor:pointer;">Left</button>
        </div>
      </div>
    </div>
  </div>
`)
	if webrtcOffered {
		fmt.Fprint(w, "  <script src=\"/viewer/webrtc.js\"></script>\n")
	}
	fmt.Fprint(w, `  <script>
  (function(){
    var img = document.getElementById('feed');
    var overlay = document.getElementById('overlay');
    var unmuteBtn = document.getElementById('unmute');

    var healthOk = false;
    // The target's screen dark (no signal): the Lock button wakes it then.
    var screenDark = false, updateLockBtn = function() {}, updateRotateBtn = function() {};
    // Whether roadie is serving pictures: when it is fine, and also with no
    // signal, when the picture is dark in the target's outline.
    var serving = false;
    var cropX = 0, cropY = 0, cropW = 0, cropH = 0, fullW = 0, fullH = 0;
    // Where the target's picture sits in the frame, whether or not we crop
    // to it. With autocrop off the whole frame is served, and input still
    // has to land on the picture rather than the letterbox bars.
    var contentX = 0, contentY = 0, contentW = 0, contentH = 0;

    function showOverlay(msg) {
      overlay.classList.remove('notice');
      overlay.textContent = msg || 'Disconnected \u2014 waiting for capture device\u2026';
      overlay.style.display = 'flex';
    }
    // A message over the picture rather than covering it.
    function showNotice(msg) {
      overlay.classList.add('notice');
      overlay.textContent = '';
      var span = document.createElement('span');
      span.textContent = msg;
      overlay.appendChild(span);
      overlay.style.display = 'flex';
    }
    function tryHideOverlay() {
      if (healthOk) {
        overlay.style.display = 'none';
      }
    }

    // The feed: /stream read with fetch and each frame shown in the <img>.
    // An <img> given /stream itself freezes on its last frame when the
    // stream ends, with no event to say so; this way the page knows when it
    // ends, or stalls, and starts it again.
    var stream = null;      // the AbortController of the stream being read
    var lastFrame = 0;      // when its last frame arrived
    var shownURL = null, nextURL = null;
    var STALL_MS = 4000;
    var mjpegFrames = 0, mjpegBytes = 0;
    // For the stats box (viewer/webrtc.js): what the MJPEG feed delivers.
    window.__roadieMJPEGStats = function() {
      return {frames: mjpegFrames, bytes: mjpegBytes, size: shownSize,
              quality: parseInt(document.getElementById('qrange').value, 10) || 0};
    };
    function showFrame(bytes) {
      lastFrame = Date.now();
      mjpegFrames++;
      mjpegBytes += bytes.length;
      if (nextURL) return;  // the last one is still decoding: skip this one
      nextURL = URL.createObjectURL(new Blob([bytes], {type: 'image/jpeg'}));
      img.src = nextURL;
    }
    var shownSize = '';
    img.onload = function() {
      var size = img.naturalWidth + 'x' + img.naturalHeight;
      if (size !== shownSize) { shownSize = size; if (typeof relayout === 'function') relayout(); }
      if (shownURL) URL.revokeObjectURL(shownURL);
      shownURL = nextURL;
      nextURL = null;
      img.style.display = '';
      tryHideOverlay();
    };
    img.onerror = function() {
      if (nextURL) URL.revokeObjectURL(nextURL);
      nextURL = null;
    };
    function indexOfCRLF2(buf) {
      for (var i = 0; i + 3 < buf.length; i++) {
        if (buf[i] === 13 && buf[i+1] === 10 && buf[i+2] === 13 && buf[i+3] === 10) return i;
      }
      return -1;
    }
    // takeFrames shows each whole frame in buf (parts of
    // "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: N\r\n\r\n"
    // and N bytes) and returns what is left of the next.
    var decoder = new TextDecoder();
    function takeFrames(buf) {
      for (;;) {
        var end = indexOfCRLF2(buf);
        if (end < 0) return buf;
        var m = /Content-Length:\s*(\d+)/i.exec(decoder.decode(buf.subarray(0, end)));
        var start = end + 4;
        if (!m) { buf = buf.subarray(start); continue; }
        var len = parseInt(m[1], 10);
        if (buf.length < start + len) return buf;
        showFrame(buf.slice(start, start + len));
        buf = buf.subarray(start + len);
      }
    }
    function startStream() {
      if (window.__roadieExternalFeed) return;  // WebRTC is showing the picture
      if (stream) stream.abort();
      var ctl = new AbortController();
      stream = ctl;
      lastFrame = Date.now();
      // ?as=bytes: Safari hands fetch nothing of a multipart/x-mixed-replace
      // response, so ask for the same stream under a plain type.
      fetch('/stream?as=bytes', {signal: ctl.signal, cache: 'no-store'}).then(function(r) {
        if (!r.ok || !r.body) throw new Error('stream: ' + r.status);
        var reader = r.body.getReader();
        var buf = new Uint8Array(0);
        function pump() {
          return reader.read().then(function(res) {
            if (res.done) throw new Error('stream ended');
            var joined = new Uint8Array(buf.length + res.value.length);
            joined.set(buf);
            joined.set(res.value, buf.length);
            buf = takeFrames(joined);
            return pump();
          });
        }
        return pump();
      }).catch(function() {
        if (stream === ctl) stream = null;  // the watchdog starts another
      });
    }
    // The WebRTC module takes the picture over and gives it back through
    // these.
    window.__roadieFeed = {
      stop: function() { if (stream) { stream.abort(); stream = null; } },
      start: function() { if (!stream && serving) startStream(); }
    };
    // The watchdog: a stream that ended, or has sent nothing for STALL_MS
    // (a network that dropped it silently, a laptop back from sleep), is
    // started again once roadie is serving.
    setInterval(function() {
      if (stream && Date.now() - lastFrame > STALL_MS) {
        stream.abort();
        stream = null;
      }
      if (!stream && serving) startStream();
    }, 1000);

    function poll() {
      var ctl = new AbortController();
      var timeout = setTimeout(function() { ctl.abort(); }, 3000);
      fetch('/health', {signal: ctl.signal}).then(function(r){ return r.json(); }).then(function(data){
        clearTimeout(timeout);
        serving = data.status === 'ok' || data.status === 'no_signal';
        setTurning(!!data.rotating);
        if ((data.status === 'no_signal') !== screenDark) {
          screenDark = data.status === 'no_signal';
          updateLockBtn();
        }
        if (serving && !stream) startStream();
        if (data.status === 'ok') {
          healthOk = true;
          tryHideOverlay();
        } else {
          healthOk = false;
          if (data.status === 'no_signal') { showNotice('No signal: the target\u2019s screen is off or locked, or its HDMI cable is out'); }
          else if (data.status === 'connecting') { showOverlay('Connecting\u2026'); }
          else { showOverlay(); }
        }
        if (data.resolution) {
          var rp = data.resolution.split('x');
          fullW = parseInt(rp[0]); fullH = parseInt(rp[1]);
        }
        if (data.crop) {
          cropX = data.crop.x; cropY = data.crop.y;
          cropW = data.crop.width; cropH = data.crop.height;
        } else {
          cropX = 0; cropY = 0; cropW = fullW; cropH = fullH;
        }
        if (data.content) {
          contentX = data.content.x; contentY = data.content.y;
          contentW = data.content.width; contentH = data.content.height;
          updateRotateBtn();
        }
        // Show unmute button when audio is available.
        if (data.audio) { unmuteBtn.style.display = 'flex'; }
      }).catch(function(){ clearTimeout(timeout); healthOk = serving = false; showOverlay(); });
    }
    poll();
    setInterval(poll, 1000);

    // --- Audio via WebSocket ---
    var audioCtx = null;
    var audioWs = null;
    var muted = true;
    var audioQueue = [];
    var workletReady = false;
    var workletNode = null;

    var workletCode = [
      'class PCMPlayer extends AudioWorkletProcessor {',
      '  constructor() {',
      '    super();',
      '    this.buf = [];',
      '    this.pos = 0;',
      '    this.port.onmessage = (e) => { this.buf.push(e.data); };',
      '  }',
      '  process(inputs, outputs) {',
      '    var out = outputs[0];',
      '    var ch = out.length;',
      '    var frames = out[0].length;',
      '    var written = 0;',
      '    while (written < frames && this.buf.length > 0) {',
      '      var chunk = this.buf[0];',
      '      var spc = chunk.length / ch;',
      '      var avail = spc - this.pos;',
      '      var n = Math.min(avail, frames - written);',
      '      for (var c = 0; c < ch; c++) {',
      '        for (var i = 0; i < n; i++) {',
      '          out[c][written + i] = chunk[(this.pos + i) * ch + c];',
      '        }',
      '      }',
      '      written += n;',
      '      this.pos += n;',
      '      if (this.pos >= spc) { this.buf.shift(); this.pos = 0; }',
      '    }',
      '    for (var c = 0; c < ch; c++) {',
      '      for (var i = written; i < frames; i++) out[c][i] = 0;',
      '    }',
      '    while (this.buf.length > 25) { this.buf.shift(); this.pos = 0; }',
      '    return true;',
      '  }',
      '}',
      'registerProcessor("pcm-player", PCMPlayer);'
    ].join('\n');

    function useWorklet() {
      return window.isSecureContext && audioCtx && audioCtx.audioWorklet;
    }

    function setupWorklet(params) {
      var blob = new Blob([workletCode], { type: 'application/javascript' });
      var url = URL.createObjectURL(blob);
      audioCtx.audioWorklet.addModule(url).then(function() {
        URL.revokeObjectURL(url);
        workletNode = new AudioWorkletNode(audioCtx, 'pcm-player', {
          outputChannelCount: [params.channels]
        });
        workletNode.connect(audioCtx.destination);
        workletReady = true;
      });
    }

    function setupScriptProcessor(params) {
      var readPos = 0;
      var node = audioCtx.createScriptProcessor(4096, 0, params.channels);
      node.onaudioprocess = function(ev) {
        var out = ev.outputBuffer;
        var frames = out.length;
        var channels = out.numberOfChannels;
        for (var ch = 0; ch < channels; ch++) {
          var b = out.getChannelData(ch);
          for (var i = 0; i < frames; i++) b[i] = 0;
        }
        var written = 0;
        while (written < frames && audioQueue.length > 0) {
          var chunk = audioQueue[0];
          var spc = chunk.length / channels;
          var avail = spc - readPos;
          var n = Math.min(avail, frames - written);
          for (var ch = 0; ch < channels; ch++) {
            var b = out.getChannelData(ch);
            for (var i = 0; i < n; i++) {
              b[written + i] = chunk[(readPos + i) * channels + ch];
            }
          }
          written += n;
          readPos += n;
          if (readPos >= spc) { audioQueue.shift(); readPos = 0; }
        }
        while (audioQueue.length > 25) { audioQueue.shift(); readPos = 0; }
      };
      node.connect(audioCtx.destination);
    }

    function startAudio() {
      var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      audioWs = new WebSocket(proto + '//' + location.host + '/audio');
      audioWs.binaryType = 'arraybuffer';
      var params = null;
      workletReady = false;
      workletNode = null;

      audioWs.onmessage = function(e) {
        if (!params) {
          params = JSON.parse(e.data);
          audioCtx = new AudioContext({ sampleRate: params.sampleRate });
          if (useWorklet()) {
            setupWorklet(params);
          } else {
            setupScriptProcessor(params);
          }
          return;
        }
        var samples = new Float32Array(e.data);
        if (useWorklet()) {
          if (workletReady && workletNode) {
            workletNode.port.postMessage(samples);
          }
        } else {
          audioQueue.push(samples);
        }
      };

      audioWs.onclose = function() {
        if (!muted) setTimeout(startAudio, 2000);
      };
    }

    function stopAudio() {
      if (audioWs) { audioWs.close(); audioWs = null; }
      if (audioCtx) { audioCtx.close(); audioCtx = null; }
      audioQueue = [];
      workletReady = false;
      workletNode = null;
    }

    var muteIconSVG = '<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M6.717 3.55A.5.5 0 0 1 7 4v8a.5.5 0 0 1-.812.39L3.825 10.5H1.5A.5.5 0 0 1 1 10V6a.5.5 0 0 1 .5-.5h2.325l2.363-1.89a.5.5 0 0 1 .529-.06m7.137 2.096a.5.5 0 0 1 0 .708L12.207 8l1.647 1.646a.5.5 0 0 1-.708.708L11.5 8.707l-1.646 1.647a.5.5 0 0 1-.708-.708L10.793 8 9.146 6.354a.5.5 0 1 1 .708-.708L11.5 7.293l1.646-1.647a.5.5 0 0 1 .708 0"/></svg>';
    var volumeIconSVG = '<svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M11.536 14.01A8.47 8.47 0 0 0 14.026 8a8.47 8.47 0 0 0-2.49-6.01l-.708.707A7.48 7.48 0 0 1 13.025 8c0 2.071-.84 3.946-2.197 5.303z"/><path d="M10.121 12.596A6.48 6.48 0 0 0 12.025 8a6.48 6.48 0 0 0-1.904-4.596l-.707.707A5.48 5.48 0 0 1 11.025 8a5.48 5.48 0 0 1-1.61 3.89z"/><path d="M8.707 11.182A4.5 4.5 0 0 0 10.025 8a4.5 4.5 0 0 0-1.318-3.182L8 5.525A3.5 3.5 0 0 1 9.025 8 3.5 3.5 0 0 1 8 10.475zM6.717 3.55A.5.5 0 0 1 7 4v8a.5.5 0 0 1-.812.39L3.825 10.5H1.5A.5.5 0 0 1 1 10V6a.5.5 0 0 1 .5-.5h2.325l2.363-1.89a.5.5 0 0 1 .529-.06"/></svg>';
    unmuteBtn.onclick = function() {
      muted = !muted;
      if (muted) {
        unmuteBtn.innerHTML = muteIconSVG;
        stopAudio();
      } else {
        unmuteBtn.innerHTML = volumeIconSVG;
        startAudio();
      }
    };

    // --- Voice input: mic toggle, browser mic -> /api/mic -> target's USB mic ---
    var pttBtn = document.getElementById('ptt');
    var pttWs = null, pttCtx = null, pttStream = null, pttNode = null;
    var talking = false;

    if (!(window.isSecureContext && navigator.mediaDevices)) {
      // getUserMedia needs a secure context (https or localhost)
      pttBtn.style.opacity = '0.4';
      pttBtn.title = 'Voice input needs https or localhost';
    }

    var pttWorkletCode = [
      'class PCMSender extends AudioWorkletProcessor {',
      '  process(inputs) {',
      '    var ch = inputs[0][0];',
      '    if (ch) this.port.postMessage(ch.slice(0));',
      '    return true;',
      '  }',
      '}',
      "registerProcessor('pcm-sender', PCMSender);"
    ].join('\n');

    function startTalk() {
      if (talking || !navigator.mediaDevices) return;
      talking = true;
      pttBtn.classList.add('live');
      navigator.mediaDevices.getUserMedia({ audio: {
        channelCount: 1, echoCancellation: true, noiseSuppression: true
      } }).then(function(stream) {
        if (!talking) { stream.getTracks().forEach(function(t){ t.stop(); }); return; }
        pttStream = stream;
        var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        pttWs = new WebSocket(proto + '//' + location.host + '/api/mic');
        pttWs.binaryType = 'arraybuffer';
        pttCtx = new AudioContext({ sampleRate: 16000 });
        var url = URL.createObjectURL(new Blob([pttWorkletCode], { type: 'application/javascript' }));
        pttCtx.audioWorklet.addModule(url).then(function() {
          if (!talking || !pttCtx) return;
          var src = pttCtx.createMediaStreamSource(pttStream);
          pttNode = new AudioWorkletNode(pttCtx, 'pcm-sender', { numberOfInputs: 1, numberOfOutputs: 0 });
          var acc = [], accLen = 0;
          pttNode.port.onmessage = function(ev) {
            if (!pttWs || pttWs.readyState !== 1) return;
            acc.push(ev.data); accLen += ev.data.length;
            if (accLen >= 320) {  // send ~20ms batches; board buffers only ~100ms
              var f32 = new Float32Array(accLen), o = 0;
              for (var i = 0; i < acc.length; i++) { f32.set(acc[i], o); o += acc[i].length; }
              acc = []; accLen = 0;
              var i16 = new Int16Array(f32.length);
              for (var j = 0; j < f32.length; j++) {
                var s2 = Math.max(-1, Math.min(1, f32[j]));
                i16[j] = s2 < 0 ? s2 * 32768 : s2 * 32767;
              }
              pttWs.send(i16.buffer);
            }
          };
          src.connect(pttNode);
        });
      }).catch(function() { stopTalk(); });
    }

    function stopTalk() {
      talking = false;
      pttBtn.classList.remove('live');
      if (pttNode) { pttNode.disconnect(); pttNode = null; }
      if (pttCtx) { pttCtx.close(); pttCtx = null; }
      if (pttStream) { pttStream.getTracks().forEach(function(t){ t.stop(); }); pttStream = null; }
      if (pttWs) { pttWs.close(); pttWs = null; }
    }

    pttBtn.onclick = function() {
      if (talking) { stopTalk(); } else { startTalk(); }
    };

    // --- Settings panel ---
    var qbtn = document.getElementById('qbtn');
    var qslider = document.getElementById('qslider');
    var qrange = document.getElementById('qrange');
    var qval = document.getElementById('qval');
    var fpsSelect = document.getElementById('fpsSelect');
    var resSelect = document.getElementById('resSelect');
    var zoomRange = document.getElementById('zoomRange');
    var zoomVal = document.getElementById('zoomVal');
    var alignTLBtn = document.getElementById('alignTLBtn');
    var alignCenterBtn = document.getElementById('alignCenterBtn');
    var alignment = 'top-left';
    var settingsTimer = null;
    var qHideTimer = null;

    function applyToUI(d) {
      if (d.quality !== undefined) { qrange.value = d.quality; qval.textContent = d.quality; }
      if (d.fps !== undefined) { fpsSelect.value = d.fps; }
      if (d.width !== undefined && d.height !== undefined) { resSelect.value = d.width + 'x' + d.height; }
    }

    (function loadSettings() {
      var saved = {};
      try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
      // The crop is chosen on the server (see /api/crop); replaying an old
      // autocrop from this browser would undo that.
      if ('autocrop' in saved) {
        delete saved.autocrop;
        try { localStorage.setItem('roadie-settings', JSON.stringify(saved)); } catch(e) {}
      }
      if (Object.keys(saved).length > 0) {
        applyToUI(saved);
        fetch('/api/settings', {
          method: 'PUT',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(saved)
        });
      } else {
        fetch('/api/settings').then(function(r){ return r.json(); }).then(function(d){
          applyToUI(d);
        });
      }
    })();

    function saveSettings(obj) {
      var saved = {};
      try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
      for (var k in obj) { saved[k] = obj[k]; }
      localStorage.setItem('roadie-settings', JSON.stringify(saved));
    }

    function positionPanel() {
      var toolbar = document.getElementById('toolbar');
      var tr = toolbar.getBoundingClientRect();
      var st = qslider.style;
      st.left = st.right = st.top = st.bottom = '';
      var room;
      if (toolbarSide === 'top') {
        st.top = 'calc(100% + 2px)'; st.left = '8px';
        room = window.innerHeight - tr.bottom - 10;
      } else if (toolbarSide === 'bottom') {
        st.bottom = 'calc(100% + 2px)'; st.left = '8px';
        room = tr.top - 10;
      } else {
        st.top = '8px';
        if (window.innerWidth - tr.right > 230) st.left = 'calc(100% + 2px)';
        else st.right = 'calc(100% - 2px)';
        room = window.innerHeight - tr.top - 18;
      }
      st.maxHeight = Math.max(120, room) + 'px';
      st.overflowY = 'auto';
    }

    qbtn.onclick = function() {
      var vis = qslider.style.display !== 'none';
      if (vis) {
        qslider.style.display = 'none';
        menuClosed();
      } else {
        positionPanel();
        qslider.style.display = 'block';
        cropRequest('GET');
      }
    };

    // A press anywhere but the menu and its button closes the menu, as on a
    // Mac, and does nothing else: a click meant to close the menu doesn't
    // also tap the phone, type a key or press another button. Everything
    // the press would set off, up to its click, is swallowed.
    var swallowing = false;
    document.addEventListener('pointercancel', function() { swallowing = false; }, true);
    document.addEventListener('pointerdown', function(e) {
      swallowing = false;  // a release lost outside the window ends it too
      if (qslider.style.display === 'none' || qslider.contains(e.target) || qbtn.contains(e.target)) return;
      qslider.style.display = 'none';
      menuClosed();
      swallowing = true;
      e.preventDefault();
      e.stopPropagation();
    }, true);
    ['mousedown', 'mouseup', 'touchstart', 'touchmove', 'touchend', 'pointermove', 'pointerup', 'click', 'contextmenu'].forEach(function(type) {
      document.addEventListener(type, function(e) {
        if (!swallowing) return;
        e.preventDefault();
        e.stopPropagation();
        // The click comes straight after the release, if at all.
        if (type === 'pointerup') setTimeout(function() { swallowing = false; }, 0);
      }, {capture: true, passive: false});
    });

    // --- Toolbar auto-hide ---
    // Tucked off the right edge until the pointer comes within half a
    // button's width of it; tucked again once the pointer has left it,
    // unless the menu is open. Where the picture fits beside the toolbar's
    // column, the column stays and nothing moves. Where it doesn't (a
    // narrow window), the tucked toolbar gives its column to the picture
    // and floats over it when it comes back. The picture moves only when
    // the toolbar slides away, or, turning auto-hide off, when the menu
    // closes: never at the moment the box is ticked.
    var toolbarEl = document.getElementById('toolbar');
    var REVEAL_PX = 18;
    var autoHide = false;
    var tuckTimer = null, overToolbar = false;
    var toolbarSide = 'right';
    // Whether the picture, fitted to the window's height and zoomed, fits
    // beside the toolbar's column: taking the column wouldn't enlarge it.
    function roomBeside() {
      var nw = img.naturalWidth, nh = img.naturalHeight;
      if (!nw || !nh) return true;
      var across = toolbarSide === 'top' || toolbarSide === 'bottom';
      var W = document.documentElement.clientWidth;
      var H = document.documentElement.clientHeight - document.getElementById('onscreen-kbd').offsetHeight;
      var bar = across ? toolbarEl.offsetHeight : toolbarEl.offsetWidth;
      // The picture's width, fitted into w by h.
      var fit = function(w, h) { return Math.min(nw, w, nw * h / nh); };
      var full = fit(W, H);
      var kept = across ? fit(W, H - bar) : fit(W - bar, H);
      var zoom = (parseInt(zoomRange.value, 10) || 100) / 100;
      var zoomedFits = across ? kept * nh / nw * zoom <= H - bar + 0.5 : kept * zoom <= W - bar + 0.5;
      return full <= kept + 0.5 && zoomedFits;
    }
    // A tucked toolbar keeps or gives up its column as the picture needs;
    // a shown one floating where there is now room goes back to it.
    function relayout() {
      if (!autoHide) return;
      var room = roomBeside();
      if (toolbarEl.classList.contains('tucked')) toolbarEl.classList.toggle('floating', !room);
      else if (room) toolbarEl.classList.remove('floating');
    }
    function tuck() {
      if (autoHide && qslider.style.display === 'none' && !overToolbar) {
        toolbarEl.classList.add('tucked');
        relayout();
      }
    }
    function scheduleTuck(ms) { clearTimeout(tuckTimer); tuckTimer = setTimeout(tuck, ms); }
    function untuck() { clearTimeout(tuckTimer); toolbarEl.classList.remove('tucked'); }
    function menuClosed() {
      if (autoHide) scheduleTuck(600);
      else toolbarEl.classList.remove('floating', 'tucked');
    }
    window.addEventListener('resize', relayout);
    if (window.ResizeObserver) new ResizeObserver(relayout).observe(document.getElementById('viewer'));
    zoomRange.addEventListener('input', relayout);
    function applyAutoHide(on) {
      autoHide = on;
      if (on) scheduleTuck(600);
      else if (qslider.style.display === 'none') menuClosed();
    }
    toolbarEl.addEventListener('mouseenter', function() { overToolbar = true; untuck(); });
    toolbarEl.addEventListener('mouseleave', function() { overToolbar = false; scheduleTuck(600); });
    // Within half a button's width of the toolbar's edge.
    function nearEdge(e) {
      switch (toolbarSide) {
        case 'left': return e.clientX <= REVEAL_PX;
        case 'top': return e.clientY <= REVEAL_PX;
        case 'bottom': return e.clientY >= window.innerHeight - REVEAL_PX;
        default: return e.clientX >= window.innerWidth - REVEAL_PX;
      }
    }
    document.addEventListener('mousemove', function(e) {
      if (toolbarEl.classList.contains('tucked') && nearEdge(e)) {
        untuck();
        scheduleTuck(1500);  // unless the pointer goes on to it
      }
    });
    // A touch screen has no hover: a tap at the edge shows it, a tap
    // anywhere else tucks it.
    document.addEventListener('pointerdown', function(e) {
      if (e.pointerType === 'mouse' || !autoHide) return;
      if (nearEdge(e)) untuck();
      else if (!toolbarEl.contains(e.target)) { overToolbar = false; scheduleTuck(600); }
    }, true);
    // --- The phone's own buttons, as keyboard shortcuts the phone already
    // takes from a hardware keyboard: nothing here needs a consumer-control
    // device. Home is GUI+H on iOS (Cmd+H) and Android (Meta+H); on a
    // computer the same keys hide the app or start dictation, so these are
    // offered for phones only. Android also gets Back (Escape), Recents
    // (Alt+Tab) and volume (the keyboard volume keys, which Linux hands to
    // Android as its volume keys). iOS has no system-wide Back or Recents
    // shortcut, and its volume is fixed while Roadie's audio is its output.
    function sendKeys(codes) {
      codes.forEach(function(c) { hidSend({cmd: 'key_press', keycode: c}); });
      codes.slice().reverse().forEach(function(c) { hidSend({cmd: 'key_release', keycode: c}); });
    }
    (function() {
      var plat = window.__roadiePlatform;
      if (plat !== 'ios' && plat !== 'android') return;
      var GUI = 227, ALT = 226;
      var buttons = {homeBtn: [GUI, 11]};
      if (plat === 'android') {
        buttons.backBtn = [41];          // Escape
        buttons.recentsBtn = [ALT, 43];  // Alt+Tab
        buttons.volUpBtn = [128];        // Keyboard Volume Up
        buttons.volDownBtn = [129];      // Keyboard Volume Down
      }
      Object.keys(buttons).forEach(function(id) {
        var b = document.getElementById(id);
        b.style.display = 'flex';
        b.onclick = function() { sendKeys(buttons[id]); };
      });
      // Lock and wake, like the phone's side button: iOS locks on Ctrl+Cmd+Q
      // and wakes on any key, Shift being one that types nothing; Android's
      // power key (the keyboard Power usage) does both. While the screen is
      // dark (roadie sees no signal) the button wakes; otherwise it locks.
      var lockBtn = document.getElementById('lockBtn');
      var LOCK = '<path d="M8 1a3 3 0 0 0-3 3v2.5H4.5A1.5 1.5 0 0 0 3 8v5.5A1.5 1.5 0 0 0 4.5 15h7a1.5 1.5 0 0 0 1.5-1.5V8a1.5 1.5 0 0 0-1.5-1.5H11V4a3 3 0 0 0-3-3m-1.5 3a1.5 1.5 0 1 1 3 0v2.5h-3z"/>';
      var WAKE = '<path d="M8 4.5a3.5 3.5 0 1 0 0 7 3.5 3.5 0 0 0 0-7"/><path d="M8 0a.6.6 0 0 1 .6.6v1.8a.6.6 0 0 1-1.2 0V.6A.6.6 0 0 1 8 0m0 13a.6.6 0 0 1 .6.6v1.8a.6.6 0 0 1-1.2 0v-1.8A.6.6 0 0 1 8 13m8-5a.6.6 0 0 1-.6.6h-1.8a.6.6 0 0 1 0-1.2h1.8A.6.6 0 0 1 16 8M3 8a.6.6 0 0 1-.6.6H.6a.6.6 0 0 1 0-1.2h1.8A.6.6 0 0 1 3 8m10.66-5.66a.6.6 0 0 1 0 .85l-1.27 1.27a.6.6 0 1 1-.85-.85l1.27-1.27a.6.6 0 0 1 .85 0m-9.2 9.2a.6.6 0 0 1 0 .85l-1.27 1.27a.6.6 0 1 1-.85-.85l1.27-1.27a.6.6 0 0 1 .85 0m9.2 2.12a.6.6 0 0 1-.85 0l-1.27-1.27a.6.6 0 1 1 .85-.85l1.27 1.27a.6.6 0 0 1 0 .85m-9.2-9.2a.6.6 0 0 1-.85 0L2.34 3.19a.6.6 0 1 1 .85-.85l1.27 1.27a.6.6 0 0 1 0 .85"/>';
      var lockKeys = plat === 'ios' ? [224, GUI, 20] : [102];   // Ctrl+Cmd+Q; Power
      var wakeKeys = plat === 'ios' ? [225] : [102];            // Shift; Power
      updateLockBtn = function() {
        lockBtn.title = screenDark
          ? (plat === 'ios' ? 'Wake (Shift)' : 'Wake (Power)')
          : (plat === 'ios' ? 'Lock (Ctrl+\u2318Q)' : 'Lock (Power)');
        lockBtn.querySelector('svg').innerHTML = screenDark ? WAKE : LOCK;
      };
      updateLockBtn();
      lockBtn.style.display = 'flex';
      lockBtn.onclick = function() { sendKeys(screenDark ? wakeKeys : lockKeys); };

      // Rotate: /api/rotate turns an iPhone through AssistiveTouch's menu,
      // in a few seconds.
      if (plat === 'ios') {
        var rotBtn = document.getElementById('rotateBtn');
        rotBtn.style.display = 'flex';
        var nextTurn = function() {
          var w = contentW || img.naturalWidth, h = contentH || img.naturalHeight;
          return w > h ? 'portrait' : 'landscape';
        };
        // The icon is the turn it makes: the phone turns Left, so an arrow
        // counterclockwise round a wide screen to landscape, clockwise round
        // a tall one back.
        updateRotateBtn = function() {
          var land = nextTurn() === 'landscape';
          var svg = rotBtn.querySelector('svg'), rect = svg.querySelector('rect');
          svg.style.transform = land ? 'scaleX(-1)' : '';
          rect.setAttribute('x', land ? 5.1 : 5.9); rect.setAttribute('y', land ? 5.9 : 5.1);
          rect.setAttribute('width', land ? 5.8 : 4.2); rect.setAttribute('height', land ? 4.2 : 5.8);
          if (!turning) rotBtn.title = land ? 'Rotate to landscape' : 'Rotate to portrait';
        };
        updateRotateBtn();
        rotBtn.onclick = function() {
          if (turning) return;
          var to = nextTurn();
          turningHere = true;
          setTurning(true);
          rotBtn.title = 'Rotating to ' + to + '\u2026';
          fetch('/api/rotate', {method: 'POST', headers: {'Content-Type': 'application/json'},
                                body: JSON.stringify({to: to})})
            .then(function(r) { return r.ok ? '' : r.text(); }, function(e) { return e.message; })
            .then(function(err) {
              turningHere = false;
              setTurning(false);
              updateRotateBtn();
              if (err) toolbarNote(err.trim());
            });
        };
      }

      var homeBtn = document.getElementById('homeBtn');
      if (plat === 'ios') {
        homeBtn.title = 'Home (\u2318H)';
      } else {
        homeBtn.title = 'Home (Meta+H)';
        // Android's navigation bar draws Home as a circle.
        homeBtn.querySelector('svg').innerHTML = '<path fill-rule="evenodd" d="M8 2.5a5.5 5.5 0 1 1 0 11 5.5 5.5 0 0 1 0-11m0 1.5a4 4 0 1 0 0 8 4 4 0 0 0 0-8"/>';
      }
    })();

    // --- Screenshot: the picture as it is now, saved by the browser as
    // <site>-<platform>-<local time>.jpg. Nothing is sent to the target.
    // The site is the host's own name: roadie for roadie.local, vibium for
    // vibium.com or app.vibium.com, and roadie for a bare address.
    function siteName() {
      var h = location.hostname;
      if (!h || h.indexOf('.') < 0 || h.indexOf(':') >= 0 || /^[\d.]+$/.test(h)) return 'roadie';
      var parts = h.split('.'), last = parts.length - 1;
      if (parts[last] === 'local') return parts[last - 1];
      var i = last - 1;
      if (i > 0 && parts[i].length <= 3 && parts[last].length === 2) i--;  // vibium.co.uk
      return parts[i];
    }
    document.getElementById('shotBtn').onclick = function() {
      fetch('/snapshot?quality=' + shotQuality(), {cache: 'no-store'}).then(function(r) {
        if (!r.ok) throw new Error(r.status);
        return r.blob();
      }).then(function(blob) {
        var d = new Date(), two = function(n) { return ('0' + n).slice(-2); };
        var stamp = d.getFullYear() + '-' + two(d.getMonth() + 1) + '-' + two(d.getDate()) + '-' +
          two(d.getHours()) + two(d.getMinutes()) + two(d.getSeconds());
        var a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        var plat = window.__roadiePlatform;
        var kind = plat === 'ios' ? 'iphone' : plat;
        a.download = siteName() + (kind ? '-' + kind : '') + '-' + stamp + '.jpg';
        document.body.appendChild(a);
        a.click();
        a.remove();
        setTimeout(function() { URL.revokeObjectURL(a.href); }, 1000);
      }).catch(function() {});
    };

    // --- Toolbar: Show, Auto-hide or Off ---
    // Remembered by this browser. Off hides it entirely; ?toolbar=show in
    // the address (or /settings) brings it back. ?toolbar= also sets it,
    // and is remembered, for a bookmark or an agent wanting a bare viewer.
    var tbBtns = {
      show: document.getElementById('tbShowBtn'),
      auto: document.getElementById('tbAutoBtn'),
      off: document.getElementById('tbOffBtn')
    };
    function setToolbar(mode) {
      if (!tbBtns[mode]) mode = 'show';
      for (var k in tbBtns) tbBtns[k].classList.toggle('on', k === mode);
      if (mode === 'off') {
        autoHide = false;
        clearTimeout(tuckTimer);
        qslider.style.display = 'none';
        toolbarEl.classList.remove('floating', 'tucked');
        toolbarEl.style.display = 'none';
      } else {
        toolbarEl.style.display = 'flex';
        applyAutoHide(mode === 'auto');
      }
    }
    function toolbarNote(text) {
      var n = document.getElementById('toolbarNote');
      if (!n) { n = document.createElement('div'); n.id = 'toolbarNote'; document.body.appendChild(n); }
      n.textContent = text;
      n.style.opacity = '1';
      clearTimeout(n.timer);
      n.timer = setTimeout(function() { n.style.opacity = '0'; }, 6000);
    }
    Object.keys(tbBtns).forEach(function(mode) {
      tbBtns[mode].onclick = function() {
        saveSettings({toolbar: mode});
        setToolbar(mode);
        if (mode === 'off') toolbarNote('Toolbar off. To bring it back, add ?toolbar=show to the address, or choose it on /settings.');
      };
    });
    var sideBtns = {
      top: document.getElementById('tbSideTop'),
      right: document.getElementById('tbSideRight'),
      bottom: document.getElementById('tbSideBottom'),
      left: document.getElementById('tbSideLeft')
    };
    function setToolbarSide(side) {
      if (!sideBtns[side]) side = 'right';
      toolbarSide = side;
      for (var k in sideBtns) {
        sideBtns[k].classList.toggle('on', k === side);
        document.body.classList.toggle('tb-' + k, k === side);
      }
      if (qslider.style.display !== 'none') positionPanel();
      relayout();
    }
    Object.keys(sideBtns).forEach(function(side) {
      sideBtns[side].onclick = function() {
        saveSettings({toolbarSide: side});
        setToolbarSide(side);
      };
    });
    (function() {
      var saved = {};
      try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
      setToolbarSide(saved.toolbarSide);
      var mode = saved.toolbar || (saved.toolbarAutoHide ? 'auto' : 'show');
      var asked = new URLSearchParams(location.search).get('toolbar');
      if (tbBtns[asked]) mode = asked;
      if (saved.toolbar !== mode || 'toolbarAutoHide' in saved) {
        delete saved.toolbarAutoHide;
        saved.toolbar = mode;
        try { localStorage.setItem('roadie-settings', JSON.stringify(saved)); } catch(e) {}
      }
      setToolbar(mode);
    })();

    function sendSettings(obj) {
      clearTimeout(settingsTimer);
      saveSettings(obj);
      settingsTimer = setTimeout(function(){
        fetch('/api/settings', {
          method: 'PUT',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(obj)
        });
      }, 300);
    }

    qrange.oninput = function() {
      qval.textContent = qrange.value;
      sendSettings({quality: parseInt(qrange.value)});
    };

    fpsSelect.onchange = function() {
      sendSettings({fps: parseInt(fpsSelect.value)});
    };

    resSelect.onchange = function() {
      var parts = resSelect.value.split('x');
      sendSettings({width: parseInt(parts[0]), height: parseInt(parts[1])});
    };

    // The crop: auto, off, or the one saved for the way the device is
    // turned. Chosen and kept by the server, not in this browser.
    var cropBtns = {
      auto: document.getElementById('cropAutoBtn'),
      off: document.getElementById('cropOffBtn'),
      saved: document.getElementById('cropSavedBtn')
    };
    var cropSaveBtn = document.getElementById('cropSaveBtn');
    var cropClearBtn = document.getElementById('cropClearBtn');
    var cropStatus = document.getElementById('cropStatus');
    function showCrop(d) {
      for (var u in cropBtns) {
        var on = d.use === u;
        cropBtns[u].classList.toggle('on', on);
      }
      var saved = [];
      ['portrait', 'landscape'].forEach(function(o) {
        var c = d.saved && d.saved[o];
        if (c) saved.push(o + ' ' + c.width + '\u00d7' + c.height + (d.mode === 'saved' && d.orientation === o ? ' (in use)' : ''));
      });
      var any = saved.length > 0;
      cropBtns.saved.disabled = cropClearBtn.disabled = !any;
      cropBtns.saved.style.opacity = cropClearBtn.style.opacity = any ? '' : '0.4';
      // Which crops are saved, and which is in use, for whoever hovers over
      // Saved; the menu itself only speaks up when something went wrong.
      var text = any ? 'Saved: ' + saved.join(', ') + '.' : 'No crop saved.';
      if (d.use === 'saved' && d.mode !== 'saved') text += ' None for ' + (d.orientation || 'this way') + ': auto for now.';
      cropBtns.saved.title = 'Use the crop saved for the way the device is turned. ' + text;
    }
    var cropErrorTimer = null;
    function cropError(msg) {
      cropStatus.textContent = msg;
      cropStatus.hidden = false;
      clearTimeout(cropErrorTimer);
      cropErrorTimer = setTimeout(function() { cropStatus.hidden = true; }, 8000);
    }
    function cropRequest(method, body) {
      var opts = {method: method};
      if (body) { opts.headers = {'Content-Type': 'application/json'}; opts.body = JSON.stringify(body); }
      fetch('/api/crop', opts).then(function(r) {
        if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
        return r.json();
      }).then(function(d) { cropStatus.hidden = true; showCrop(d); }).catch(function(e) { cropError(e.message); });
    }
    Object.keys(cropBtns).forEach(function(u) {
      cropBtns[u].onclick = function() { cropRequest('PUT', {use: u}); };
    });
    cropSaveBtn.onclick = function() { cropRequest('POST'); };
    cropClearBtn.onclick = function() { cropRequest('DELETE'); };
    cropRequest('GET');

    function applyAlignment(mode) {
      alignment = mode;
      var viewer = document.getElementById('viewer');
      var kbdI = document.getElementById('kbd-inner');
      if (mode === 'center') {
        alignTLBtn.classList.remove('on'); alignCenterBtn.classList.add('on');
        viewer.style.display = 'flex';
        viewer.style.justifyContent = 'center';
        viewer.style.alignItems = 'center';
        kbdI.style.textAlign = 'center';
      } else {
        alignCenterBtn.classList.remove('on'); alignTLBtn.classList.add('on');
        viewer.style.display = '';
        viewer.style.justifyContent = '';
        viewer.style.alignItems = '';
        kbdI.style.textAlign = '';
      }
      applyZoom(parseInt(zoomRange.value));
    }

    function applyZoom(pct) {
      zoomVal.textContent = pct + '%';
      zoomRange.value = pct;
      var viewer = document.getElementById('viewer');
      var scale = pct / 100;
      img.style.transform = scale === 1 ? '' : 'scale(' + scale + ')';
      img.style.transformOrigin = alignment === 'center' ? 'center center' : 'top left';
      viewer.style.overflow = 'hidden';
    }

    function snapZoom(val) {
      var nearest = Math.round(val / 25) * 25;
      return Math.abs(val - nearest) <= 3 ? nearest : val;
    }

    zoomRange.oninput = function() {
      var pct = snapZoom(parseInt(zoomRange.value));
      applyZoom(pct);
      saveSettings({zoom: pct});
    };

    alignTLBtn.onclick = function() {
      applyAlignment('top-left');
      saveSettings({alignment: 'top-left'});
    };

    alignCenterBtn.onclick = function() {
      applyAlignment('center');
      saveSettings({alignment: 'center'});
    };

    // Restore zoom and alignment from saved settings.
    (function() {
      var saved = {};
      try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
      applyAlignment(saved.alignment || alignment);
      if (saved.zoom !== undefined) { applyZoom(saved.zoom); }
    })();

    // --- Screenshot quality: the Screenshot button's own JPEG quality,
    // remembered by this browser; the Quality slider above is the MJPEG
    // stream's.
    var shotQualityRange = document.getElementById('shotQualityRange');
    var shotQualityVal = document.getElementById('shotQualityVal');
    function shotQuality() { return parseInt(shotQualityRange.value, 10) || 90; }
    (function() {
      var saved = {};
      try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
      if (saved.shotQuality) shotQualityRange.value = saved.shotQuality;
      shotQualityVal.textContent = shotQuality();
    })();
    shotQualityRange.oninput = function() {
      shotQualityVal.textContent = shotQuality();
      saveSettings({shotQuality: shotQuality()});
    };

    // --- Video transport: MJPEG or WebRTC, when the host offers it. The
    // module (viewer/webrtc.js) does the switching; the choice is this
    // browser's, like zoom. The Quality row is the MJPEG stream's and the
    // Bitrate row WebRTC's, so one shows at a time.
    (function() {
      if (!window.__roadieWebRTC || !window.__roadieRTC) return;
      var row = document.getElementById('videoRow');
      var mjpegBtn = document.getElementById('videoMJPEGBtn');
      var webrtcBtn = document.getElementById('videoWebRTCBtn');
      var statsCheck = document.getElementById('videoStatsCheck');
      var qualityRow = document.getElementById('qualityRow');
      var bitrateRow = document.getElementById('bitrateRow');
      var bitrateRange = document.getElementById('bitrateRange');
      var bitrateVal = document.getElementById('bitrateVal');
      row.style.display = 'flex';
      function show(transport) {
        var rtc = transport === 'webrtc';
        webrtcBtn.classList.toggle('on', rtc);
        mjpegBtn.classList.toggle('on', !rtc);
        qualityRow.style.display = rtc ? 'none' : 'flex';
        bitrateRow.style.display = rtc ? 'flex' : 'none';
      }
      // The bitrate cap: the slider's top means no cap of ours.
      function applyBitrate(kbps) {
        var auto = kbps >= parseInt(bitrateRange.max, 10);
        bitrateRange.value = kbps;
        bitrateVal.textContent = auto ? 'auto' : (kbps >= 1000 ? (kbps / 1000).toFixed(1).replace(/\.0$/, '') + ' Mbps' : kbps + ' kbps');
        window.__roadieRTC.capBitrate(auto ? 0 : kbps * 1000);
      }
      bitrateRange.oninput = function() {
        var kbps = parseInt(bitrateRange.value, 10);
        applyBitrate(kbps);
        saveSettings({bitrate: kbps});
      };
      (function() {
        var saved = {};
        try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
        applyBitrate(saved.bitrate !== undefined ? saved.bitrate : parseInt(bitrateRange.max, 10));
      })();
      mjpegBtn.onclick = function() { window.__roadieRTC.stop(); show('mjpeg'); saveSettings({transport: 'mjpeg'}); };
      webrtcBtn.onclick = function() { window.__roadieRTC.start(); show('webrtc'); saveSettings({transport: 'webrtc'}); };
      statsCheck.checked = window.__roadieRTC.statsShown();
      statsCheck.onchange = function() { window.__roadieRTC.showStats(statsCheck.checked); saveSettings({stats: statsCheck.checked}); };
      window.addEventListener('roadie-transport', function(e) { show(e.detail.transport); });
      show(window.__roadieRTC.active() ? 'webrtc' : 'mjpeg');
    })();

    // --- Theme ---
    var themeBtns = {dark: document.getElementById('themeDarkBtn'), light: document.getElementById('themeLightBtn'), system: document.getElementById('themeSystemBtn')};
    function applyTheme(t) {
      document.documentElement.setAttribute('data-theme', t);
      for (var k in themeBtns) {
        themeBtns[k].classList.toggle('on', k === t);
      }
      saveSettings({theme: t});
    }
    themeBtns.dark.onclick = function() { applyTheme('dark'); };
    themeBtns.light.onclick = function() { applyTheme('light'); };
    themeBtns.system.onclick = function() { applyTheme('system'); };
    (function() {
      var saved = {};
      try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
      applyTheme(saved.theme || 'system');
    })();

    function setupResetBtn(id, url) {
      var btn = document.getElementById(id);
      var fill = document.createElement('div');
      fill.style.cssText = 'position:absolute;left:0;top:0;bottom:0;width:0;background:rgba(255,80,80,0.4);transition:none;pointer-events:none;';
      btn.appendChild(fill);
      var label = document.createElement('span');
      label.style.position = 'relative';
      label.textContent = 'Reset';
      btn.appendChild(label);
      var raf = null, startTime = 0, HOLD_MS = 1500;
      function animateFill() {
        var elapsed = Date.now() - startTime;
        var pct = Math.min(0.2 + (elapsed / HOLD_MS) * 0.8, 1);
        fill.style.width = (pct * 100) + '%';
        if (pct >= 1) {
          cancelHold();
          btn.style.minWidth = btn.offsetWidth + 'px';
          label.innerHTML = '<svg style="animation:spin 1s linear infinite" xmlns="http://www.w3.org/2000/svg" width="14" height="14" fill="currentColor" viewBox="0 0 16 16"><path d="M11.534 7h3.932a.25.25 0 0 1 .192.41l-1.966 2.36a.25.25 0 0 1-.384 0l-1.966-2.36a.25.25 0 0 1 .192-.41m-11 2h3.932a.25.25 0 0 0 .192-.41L2.692 6.23a.25.25 0 0 0-.384 0L.342 8.59A.25.25 0 0 0 .534 9"/><path fill-rule="evenodd" d="M8 3c-1.552 0-2.94.707-3.857 1.818a.5.5 0 1 1-.771-.636A6.002 6.002 0 0 1 13.917 7H12.9A5 5 0 0 0 8 3M3.1 9a5.002 5.002 0 0 0 8.757 2.182.5.5 0 1 1 .771.636A6.002 6.002 0 0 1 2.083 9z"/></svg>';
          btn.disabled = true;
          fetch(url, {method:'POST'}).then(function(r){ return r.json(); }).then(function(){
            setTimeout(function() { label.textContent = 'Reset'; btn.disabled = false; btn.style.minWidth = ''; }, 2000);
          }).catch(function(){
            setTimeout(function() { label.textContent = 'Reset'; btn.disabled = false; btn.style.minWidth = ''; }, 2000);
          });
        } else {
          raf = requestAnimationFrame(animateFill);
        }
      }
      function cancelHold() {
        if (raf) { cancelAnimationFrame(raf); raf = null; }
        fill.style.width = '0';
        startTime = 0;
      }
      btn.addEventListener('pointerdown', function(e) {
        e.preventDefault();
        startTime = Date.now();
        raf = requestAnimationFrame(animateFill);
      });
      btn.addEventListener('pointerup', cancelHold);
      btn.addEventListener('pointerleave', cancelHold);
    }
    setupResetBtn('resetVideoBtn', '/api/capture/reset');
    setupResetBtn('resetHIDBtn', '/api/usb/reset');
    setupResetBtn('resetRelayBtn', '/api/relay/reset');
    // The Pi as the phone's USB devices: no HID or relay board, and its
    // keyboard, mouse, touch, audio and network are one USB device, reset
    // together.
    var gadget = window.__roadieHIDBackend === 'linux-gadget';
    if (gadget) {
      document.getElementById('resetHIDLabel').textContent = 'USB';
      document.getElementById('resetRelayRow').style.display = 'none';
    }

    // The "?" by Resets: what each reset shown does.
    (function() {
      var devs = window.__roadieUSBDevices || [];
      var named = devs.length < 2 ? devs.join('')
        : devs.slice(0, -1).join(', ') + ' and ' + devs[devs.length - 1];
      var seen = named ? 'the target sees its ' + named + ' unplugged and plugged back in' : 'the target sees it unplugged and plugged back in';
      var rows = [['Video', 'replugs the capture dongle, which renegotiates HDMI; the picture drops for a few seconds.']];
      if (gadget) {
        rows.push(['USB', 'the phone sees Roadie\u2019s ' + (named || 'USB devices') + ' unplugged and plugged back in. Ends a touch the phone is stuck on.']);
      } else {
        rows.push(['HID', 'reboots the HID board; ' + seen + '.']);
        rows.push(['Relay', 'reboots the relay board on this machine; the target sees nothing.']);
      }
      var help = document.getElementById('resetHelp');
      rows.forEach(function(r) {
        var p = document.createElement('div');
        p.style.margin = '0 0 4px';
        var b = document.createElement('b');
        b.textContent = r[0];
        p.appendChild(b);
        p.appendChild(document.createTextNode(': ' + r[1]));
        help.appendChild(p);
      });
      var hold = document.createElement('div');
      hold.textContent = 'Hold a button to reset.';
      help.appendChild(hold);
      document.getElementById('resetHIDBtn').title = gadget
        ? 'Hold: the phone sees Roadie\u2019s ' + (named || 'USB devices') + ' unplugged and plugged back in'
        : 'Hold: reboots the HID board';
      var helpBtn = document.getElementById('resetHelpBtn');
      helpBtn.onclick = function() {
        help.hidden = !help.hidden;
        helpBtn.setAttribute('aria-expanded', String(!help.hidden));
      };
    })();

    // --- HID WebSocket ---
    var hidWs = null, hidReady = false;
    function hidConnect() {
      var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      hidWs = new WebSocket(proto + '//' + location.host + '/api/hid/ws');
      hidWs.onopen = function() { hidReady = true; };
      hidWs.onclose = function() { hidReady = false; setTimeout(hidConnect, 2000); };
      hidWs.onerror = function() { hidReady = false; };
    }
    function hidSend(msg) {
      if (turning) return;
      if (hidWs && hidReady) hidWs.send(JSON.stringify(msg));
    }

    // While Roadie turns the screen it clicks through AssistiveTouch's menu,
    // and the pointer moving under it would spoil that: this viewer's input
    // waits until the turn is done, whoever asked for it (/health says so).
    // Whatever is held is let go first.
    var turning = false, turningHere = false;
    function setTurning(on) {
      on = on || turningHere;
      var note = document.getElementById('turnNote');
      if (on) {
        // Over the picture, which can change shape as the screen turns.
        var r = img.getBoundingClientRect(), v = img.parentNode.getBoundingClientRect();
        note.style.left = (r.left - v.left) + 'px'; note.style.top = (r.top - v.top) + 'px';
        note.style.width = r.width + 'px'; note.style.height = r.height + 'px';
      }
      if (on === turning) return;
      if (on) releaseEverythingHeld();
      turning = on;
      img.classList.toggle('turning', on);
      note.style.display = on ? 'flex' : 'none';
      var b = document.getElementById('rotateBtn');
      if (b) b.classList.toggle('busy', on);
    }
    hidConnect();

    // --- Input mode toggle ---
    // Precedence: the mode the server resolved for this load (?input=, else
    // --input), then a mode this viewer picked from the toolbar, then mouse.
    // The server's value is not persisted, so toggling still works and still
    // survives within the session; a reload returns to the configured mode.
    var inputMode =
      (window.__roadieInputMode === 'touch' || window.__roadieInputMode === 'mouse')
        ? window.__roadieInputMode
        : (localStorage.getItem('roadie-input-mode') === 'touch' ? 'touch' : 'mouse');
    var modeMouseBtn = document.getElementById('modeMouseBtn');
    var modeTouchBtn = document.getElementById('modeTouchBtn');
    function applyInputMode(mode) {
      inputMode = mode;
      modeMouseBtn.classList.toggle('on', mode === 'mouse');
      modeTouchBtn.classList.toggle('on', mode === 'touch');
    }
    function setInputMode(mode) {
      applyInputMode(mode);
      localStorage.setItem('roadie-input-mode', mode);
    }
    applyInputMode(inputMode);
    modeMouseBtn.onclick = function() { setInputMode('mouse'); };
    modeTouchBtn.onclick = function() { setInputMode('touch'); };

    // --- Coordinate helpers ---
    function remapToAbsolute(px, py) {
      // A target addressing its own display wants the position as-is. One
      // addressing the whole captured frame needs the crop folded back in, so
      // the letterbox bars count toward the coordinate. See --platform.
      if (window.__roadiePointerSpace === 'screen') {
        // px,py are fractions of the picture on screen. When the whole frame
        // is served, the target's picture is only part of it, so take the
        // letterbox bars back out before scaling.
        var whole = !((cropW > 0 && cropW !== fullW) || (cropH > 0 && cropH !== fullH));
        if (whole && contentW > 0 && fullW > 0) {
          var fx = Math.max(0, Math.min(1, (px * fullW - contentX) / contentW));
          var fy = Math.max(0, Math.min(1, (py * fullH - contentY) / contentH));
          return { x: Math.round(fx * 32767), y: Math.round(fy * 32767) };
        }
        return { x: Math.round(px * 32767), y: Math.round(py * 32767) };
      }
      var ax = (cropW > 0 && fullW > 0) ? (cropX + px * cropW) / fullW : px;
      var ay = (cropH > 0 && fullH > 0) ? (cropY + py * cropH) / fullH : py;
      return { x: Math.round(ax * 32767), y: Math.round(ay * 32767) };
    }

    function posFromFeed(e) {
      var rect = img.getBoundingClientRect();
      var px = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
      var py = Math.max(0, Math.min(1, (e.clientY - rect.top) / rect.height));
      var abs = remapToAbsolute(px, py);
      return { x: abs.x, y: abs.y };
    }

    // --- Rate-limited HID sending ---
    // Only the latest position is sent, on a timer. 33ms keeps the pointer
    // up with the video; the chain measures ~2ms a move and sustains 60 a
    // second, so this is not close to its limit.
    var pendingX = -1, pendingY = -1, mouseDirty = false;
    setInterval(function() {
      if (mouseDirty) {
        hidSend({cmd:'mouse_move', x:pendingX, y:pendingY});
        mouseDirty = false;
      }
    }, 33);

    var pendingContacts = [], touchDirty = false;
    setInterval(function() {
      if (touchDirty) {
        hidSend({cmd:'touch', contacts:pendingContacts});
        touchDirty = false;
      }
    }, 50);

    // Notches of the wheel go out as they add up (see the wheel handler).
    // An iPhone scrolls only for a run of notches at most about 100 ms
    // apart, and not at all for one alone, and the closer they come the
    // further it goes: there, while the wheel turns, a notch goes out at
    // least every runGap, so a slow scroll scrolls too, and Scroll sets how
    // fast that is.
    var pendingScroll = 0, scrollDirty = false, scrollSpeed = 1;
    var wheelDir = 0, wheelAt = 0, scrollAt = 0;
    var wheelRuns = window.__roadiePlatform === 'ios';
    // 80 ms at 25% (iOS drops notches much further apart), 50 at 100%, 25
    // at 200%.
    function runGap() {
      return scrollSpeed <= 1 ? 80 - (scrollSpeed - 0.25) * 40 : 50 - (scrollSpeed - 1) * 25;
    }
    setInterval(function() {
      var now = performance.now();
      if (wheelRuns && wheelAt > scrollAt && !scrollDirty && now - scrollAt >= runGap()) {
        pendingScroll = wheelDir;
        wheelRest = 0;
        scrollDirty = true;
      }
      if (scrollDirty) {
        hidSend({cmd:'mouse_scroll', amount:pendingScroll});
        pendingScroll = 0;
        scrollDirty = false;
        scrollAt = now;
      }
    }, 10);

    // --- Mouse handlers on feed ---
    img.addEventListener('mousemove', function(e) {
      var p = posFromFeed(e);
      if (inputMode === 'mouse') {
        pendingX = p.x; pendingY = p.y; mouseDirty = true;
      } else if (feedPressed) {
        // Only a press that began on the picture: one dragged in from
        // beside it was never sent, and would never be lifted.
        pendingContacts = [{id:0, tip:true, x:p.x, y:p.y}];
        touchDirty = true;
      }
    });

    var feedPressed = false, feedButton = 0;
    // The feed prevents default on mousedown to stop drag-selection, which
    // also suppresses the focus a click would normally give this document.
    // Standalone that goes unnoticed, because the page already holds focus.
    // Embedded in /grid each panel is an iframe that therefore never gains
    // focus, keydown never fires, and no panel accepts keyboard input at all.
    // Claim it explicitly — on entry as well as on click, so the keyboard
    // follows the same panel the pointer is already driving.
    function claimKeyboardFocus() {
      try { window.focus(); } catch (err) {}
    }
    img.addEventListener('mouseenter', claimKeyboardFocus);
    img.addEventListener('mousedown', claimKeyboardFocus);

    img.addEventListener('mousedown', function(e) {
      e.preventDefault();
      feedPressed = true;
      if (inputMode === 'mouse') {
        var btn = e.button === 2 ? 2 : e.button === 1 ? 4 : 1;
        // Release what actually went down. A secondary click can have its
        // mouseup taken by the context menu, and releasing whatever that
        // event reports then leaves the target holding the button.
        feedButton = btn;
        // Moves go out on the timer above, so the last one may not have
        // yet: say where the pointer is first, or the press lands at the
        // old spot and drags to the new one.
        var p = posFromFeed(e);
        pendingX = p.x; pendingY = p.y; mouseDirty = false;
        hidSend({cmd:'mouse_move', x:p.x, y:p.y});
        hidSend({cmd:'mouse_press', buttons:btn});
      } else {
        var p = posFromFeed(e);
        pendingContacts = [{id:0, tip:true, x:p.x, y:p.y}];
        hidSend({cmd:'touch', contacts:pendingContacts});
      }
    });

    document.addEventListener('mouseup', function(e) {
      if (!feedPressed) return;
      feedPressed = false;
      if (inputMode === 'mouse') {
        if (mouseDirty) { // the same for a move still waiting on the timer
          hidSend({cmd:'mouse_move', x:pendingX, y:pendingY});
          mouseDirty = false;
        }
        hidSend({cmd:'mouse_release', buttons:feedButton || 1});
        feedButton = 0;
      } else {
        var p = posFromFeed(e);
        hidSend({cmd:'touch', contacts:[{id:0, tip:false, x:p.x, y:p.y}]});
        setTimeout(function(){ hidSend({cmd:'touch', contacts:[]}); }, 20);
      }
    });

    // --- Scroll wheel on feed ---
    // In notches: at 100% Scroll a line is one, 25 pixels are one, a page
    // twenty. What is left over adds up, so a trackpad's and a smooth
    // wheel's small steps scroll too.
    var wheelRest = 0;
    img.addEventListener('wheel', function(e) {
      e.preventDefault();
      if (inputMode !== 'mouse' || !e.deltaY) return;
      var steps = e.deltaMode === 1 ? e.deltaY : e.deltaMode === 2 ? e.deltaY * 20 : e.deltaY / 25;
      wheelRest += steps * scrollSpeed;
      wheelDir = e.deltaY > 0 ? 1 : -1;
      wheelAt = performance.now();
      var notches = Math.trunc(wheelRest);
      if (notches !== 0) {
        wheelRest -= notches;
        pendingScroll = Math.max(-127, Math.min(127, pendingScroll + notches));
        scrollDirty = true;
      }
    }, {passive: false});

    // Scroll: how far the wheel goes, for this viewer.
    var scrollRange = document.getElementById('scrollRange');
    var scrollVal = document.getElementById('scrollVal');
    function applyScrollSpeed(pct) {
      scrollSpeed = pct / 100;
      scrollRange.value = pct;
      scrollVal.textContent = pct + '%';
    }
    scrollRange.oninput = function() {
      var pct = parseInt(scrollRange.value, 10);
      applyScrollSpeed(pct);
      saveSettings({scrollSpeed: pct});
    };
    (function() {
      var saved = {};
      try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
      if (saved.scrollSpeed >= 25 && saved.scrollSpeed <= 200) applyScrollSpeed(saved.scrollSpeed);
    })();

    // --- Touch handlers on feed ---
    function touchToContacts(e) {
      var contacts = [];
      var rect = img.getBoundingClientRect();
      for (var i = 0; i < Math.min(e.touches.length, 2); i++) {
        var t = e.touches[i];
        var px = Math.max(0, Math.min(1, (t.clientX - rect.left) / rect.width));
        var py = Math.max(0, Math.min(1, (t.clientY - rect.top) / rect.height));
        var abs = remapToAbsolute(px, py);
        contacts.push({id:i, tip:true, x:abs.x, y:abs.y});
      }
      return contacts;
    }

    var twoFingerLastY = null, scrollAccum = 0;

    img.addEventListener('touchstart', function(e) {
      e.preventDefault();
      if (inputMode === 'touch') {
        hidSend({cmd:'touch', contacts:touchToContacts(e)});
      } else if (e.touches.length === 1) {
        var p = posFromFeed(e.touches[0]);
        pendingX = p.x; pendingY = p.y; mouseDirty = true;
      }
    }, {passive: false});

    img.addEventListener('touchmove', function(e) {
      e.preventDefault();
      if (inputMode === 'touch') {
        pendingContacts = touchToContacts(e);
        touchDirty = true;
      } else {
        if (e.touches.length >= 2) {
          var avgY = (e.touches[0].clientY + e.touches[1].clientY) / 2;
          if (twoFingerLastY !== null) {
            scrollAccum += (avgY - twoFingerLastY);
            while (Math.abs(scrollAccum) >= 10) {
              var step = scrollAccum > 0 ? 3 : -3;
              pendingScroll = Math.max(-127, Math.min(127, pendingScroll + step));
              scrollDirty = true;
              scrollAccum -= (scrollAccum > 0 ? 10 : -10);
            }
          }
          twoFingerLastY = avgY;
        } else {
          twoFingerLastY = null; scrollAccum = 0;
          var p = posFromFeed(e.touches[0]);
          pendingX = p.x; pendingY = p.y; mouseDirty = true;
        }
      }
    }, {passive: false});

    img.addEventListener('touchend', function(e) {
      e.preventDefault();
      if (inputMode === 'touch') {
        if (e.touches.length === 0) { hidSend({cmd:'touch', contacts:[]}); }
        else { hidSend({cmd:'touch', contacts:touchToContacts(e)}); }
      }
      if (e.touches.length < 2) { twoFingerLastY = null; scrollAccum = 0; }
    }, {passive: false});

    // --- Keyboard on document (skip toolbar interactions) ---
    var KEY_MAP = {
      KeyA:4,KeyB:5,KeyC:6,KeyD:7,KeyE:8,KeyF:9,KeyG:10,KeyH:11,KeyI:12,
      KeyJ:13,KeyK:14,KeyL:15,KeyM:16,KeyN:17,KeyO:18,KeyP:19,KeyQ:20,
      KeyR:21,KeyS:22,KeyT:23,KeyU:24,KeyV:25,KeyW:26,KeyX:27,KeyY:28,KeyZ:29,
      Digit1:30,Digit2:31,Digit3:32,Digit4:33,Digit5:34,Digit6:35,
      Digit7:36,Digit8:37,Digit9:38,Digit0:39,
      Enter:40,Escape:41,Backspace:42,Tab:43,Space:44,
      Minus:45,Equal:46,BracketLeft:47,BracketRight:48,Backslash:49,
      Semicolon:51,Quote:52,Backquote:53,Comma:54,Period:55,Slash:56,
      CapsLock:57,
      F1:58,F2:59,F3:60,F4:61,F5:62,F6:63,F7:64,F8:65,F9:66,F10:67,F11:68,F12:69,
      PrintScreen:70,ScrollLock:71,Pause:72,Insert:73,Home:74,PageUp:75,
      Delete:76,End:77,PageDown:78,
      ArrowRight:79,ArrowLeft:80,ArrowDown:81,ArrowUp:82,
      NumLock:83,
      ControlLeft:224,ShiftLeft:225,AltLeft:226,MetaLeft:227,
      ControlRight:228,ShiftRight:229,AltRight:230,MetaRight:231
    };

    // --- Keyboard capture ---
    var kbdCapture = true;
    var kbdCaptureCheck = document.getElementById('kbdCaptureCheck');
    kbdCaptureCheck.onchange = function() { kbdCapture = kbdCaptureCheck.checked; };

    // Keys the target currently believes are down. A keyup that never
    // arrives — the window losing focus mid-chord is the usual way — leaves
    // one held indefinitely, and nothing downstream can tell: the relay sends
    // key events fire-and-forget, so no error surfaces and the next thing
    // typed just comes out wrong.
    var heldKeys = {};
    function releaseEverythingHeld() {
      // Buttons and contacts go too. A press whose release never reached
      // the page -- let go over the toolbar, or with the window no longer
      // focused -- otherwise leaves the target holding the button down,
      // which it has no way of knowing it should stop doing.
      hidSend({cmd:'mouse_release', buttons:7});
      hidSend({cmd:'touch', contacts:[]});
      feedPressed = false;
      pendingContacts = [];
      touchDirty = false;
      for (var k in heldKeys) hidSend({cmd:'key_release', keycode:parseInt(k, 10)});
      heldKeys = {};
      // The on-screen keyboard latches its own modifiers; clear those too.
      if (typeof activeModifiers === 'object' && activeModifiers) {
        for (var m in activeModifiers) hidSend({cmd:'key_release', keycode:parseInt(m, 10)});
        activeModifiers = {};
        if (typeof refreshModifierButtons === 'function') refreshModifierButtons();
      }
    }
    window.addEventListener('blur', releaseEverythingHeld);
    window.addEventListener('pagehide', releaseEverythingHeld);
    document.addEventListener('visibilitychange', function() {
      if (document.hidden) releaseEverythingHeld();
    });

    document.addEventListener('keydown', function(e) {
      if (!kbdCapture) return;
      var tag = e.target.tagName;
      if (tag === 'INPUT' || tag === 'SELECT') return;
      var hid = KEY_MAP[e.code];
      if (hid !== undefined) {
        e.preventDefault();
        heldKeys[hid] = true;
        hidSend({cmd:'key_press', keycode:hid});
      }
    });
    document.addEventListener('keyup', function(e) {
      var hid = KEY_MAP[e.code];
      if (hid === undefined) return;
      // Release on the strength of what we actually pressed, not on the
      // conditions that applied at keydown. Capture can be switched off,
      // focus can move into a form control, and either between a keydown and
      // its keyup would otherwise skip the release and leave the key down on
      // the target — where a stuck modifier silently rewrites every character
      // that follows.
      if (heldKeys[hid]) {
        e.preventDefault();
        delete heldKeys[hid];
        hidSend({cmd:'key_release', keycode:hid});
      }
    });

    // --- On-screen keyboard ---
    var oskPanel = document.getElementById('onscreen-kbd');
    var kbdInner = document.getElementById('kbd-inner');
    var kbdBtn = document.getElementById('kbdBtn');
    var oskVisible = false;
    var kbdZoomPct = 100;

    function syncKbdWidth() {}
    function showKbd(show) {
      oskVisible = show;
      if (oskVisible) {
        oskPanel.style.display = 'block';
        syncKbdWidth();
        applyKbdZoom(kbdZoomPct);
      } else {
        oskPanel.style.display = 'none';
      }
      kbdBtn.style.opacity = oskVisible ? '1' : '0.4';
    }
    kbdBtn.onclick = function() {
      showKbd(!oskVisible);
      saveSettings({kbdVisible: oskVisible});
    };

    var kbdZoomRange = document.getElementById('kbdZoomRange');
    var kbdZoomVal = document.getElementById('kbdZoomVal');
    function applyKbdZoom(pct) {
      kbdZoomPct = pct;
      kbdInner.style.zoom = (pct / 100);
      kbdZoomRange.value = pct;
      kbdZoomVal.textContent = pct + '%';
    }
    kbdZoomRange.oninput = function() {
      var pct = parseInt(kbdZoomRange.value);
      applyKbdZoom(pct);
      saveSettings({kbdZoom: pct});
    };
    (function() {
      var saved = {};
      try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
      if (saved.kbdZoom !== undefined) { kbdZoomPct = saved.kbdZoom; }
      if (saved.kbdVisible) { showKbd(true); }
    })();

    var MOD_CODES = {224:1,225:1,226:1,227:1,228:1,229:1,230:1,231:1,57:1};
    var activeModifiers = {};

    function oskPress(code) {
      if (MOD_CODES[code]) {
        if (activeModifiers[code]) { hidSend({cmd:'key_release', keycode:code}); delete activeModifiers[code]; }
        else { hidSend({cmd:'key_press', keycode:code}); activeModifiers[code] = true; }
        updateModButtons();
        return;
      }
      hidSend({cmd:'key_press', keycode:code});
      hidSend({cmd:'key_release', keycode:code});
      for (var m in activeModifiers) { hidSend({cmd:'key_release', keycode:parseInt(m)}); }
      activeModifiers = {};
      updateModButtons();
    }
    function updateModButtons() {
      var btns = oskPanel.querySelectorAll('.kk[data-mod]');
      for (var i = 0; i < btns.length; i++) {
        var c = parseInt(btns[i].getAttribute('data-code'));
        btns[i].classList.toggle('mod-active', !!activeModifiers[c]);
      }
    }
    function mkKey(label, code, w, sigil) {
      var btn = document.createElement('button');
      btn.className = 'kk';
      btn.setAttribute('data-code', code);
      if (w) btn.style.width = w + 'px';
      if (MOD_CODES[code]) btn.setAttribute('data-mod', '1');
      if (code === 0 && !label) btn.classList.add('blank');
      if (sigil === 'hide') {
        btn.style.width = '0';
        btn.style.minWidth = '0';
        btn.style.padding = '0';
        btn.style.border = 'none';
        btn.style.visibility = 'hidden';
        btn.style.marginLeft = '-3px';
      } else if (sigil === 'small') {
        btn.style.fontSize = '8px';
        btn.textContent = label;
      } else if (sigil) {
        var isShiftChar = sigil.length === 1 && sigil.charCodeAt(0) < 128;
        if (isShiftChar) {
          btn.innerHTML = '<span style="font-size:11px;line-height:1;opacity:0.6">' + sigil + '</span><span style="font-size:11px;line-height:1">' + label + '</span>';
        } else {
          btn.innerHTML = '<span style="font-size:14px;line-height:1">' + sigil + '</span><span style="font-size:8px;line-height:1">' + label + '</span>';
        }
        btn.style.flexDirection = 'column';
        btn.style.gap = '1px';
      } else if (label.indexOf('<svg') === 0) {
        btn.innerHTML = label;
      } else {
        btn.textContent = label;
      }
      btn.addEventListener('pointerdown', function(e) { e.preventDefault(); if (code) oskPress(code); });
      return btn;
    }
    function mkGap() { var d = document.createElement('div'); d.className = 'kgap numpad'; return d; }

    // 15 cols x 6 rows ortholinear + gap + 4 numpad (on wide screens)
    // [label, keycode, width, sigil]
    var rows = [
      {m:[['esc',41],['F1',58,0,'small'],['F2',59,0,'small'],['F3',60,0,'small'],['F4',61,0,'small'],['F5',62,0,'small'],['F6',63,0,'small'],['F7',64,0,'small'],['F8',65,0,'small'],['F9',66,0,'small'],['F10',67,0,'small'],['F11',68,0,'small'],['F12',69,0,'small'],['<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" fill="currentColor" viewBox="0 0 16 16"><path fill-rule="evenodd" d="M.5 6a.5.5 0 0 0-.488.608l1.652 7.434A2.5 2.5 0 0 0 4.104 16h5.792a2.5 2.5 0 0 0 2.44-1.958l.131-.59a3 3 0 0 0 1.3-5.854l.221-.99A.5.5 0 0 0 13.5 6zM13 12.5a2 2 0 0 1-.316-.025l.867-3.898A2.001 2.001 0 0 1 13 12.5M2.64 13.825 1.123 7h11.754l-1.517 6.825A1.5 1.5 0 0 1 9.896 15H4.104a1.5 1.5 0 0 1-1.464-1.175"/><path d="m4.4.8-.003.004-.014.019a4 4 0 0 0-.204.31 2 2 0 0 0-.141.267c-.026.06-.034.092-.037.103v.004a.6.6 0 0 0 .091.248c.075.133.178.272.308.445l.01.012c.118.158.26.347.37.543.112.2.22.455.22.745 0 .188-.065.368-.119.494a3 3 0 0 1-.202.388 5 5 0 0 1-.253.382l-.018.025-.005.008-.002.002A.5.5 0 0 1 3.6 4.2l.003-.004.014-.019a4 4 0 0 0 .204-.31 2 2 0 0 0 .141-.267c.026-.06.034-.092.037-.103a.6.6 0 0 0-.09-.252A4 4 0 0 0 3.6 2.8l-.01-.012a5 5 0 0 1-.37-.543A1.53 1.53 0 0 1 3 1.5c0-.188.065-.368.119-.494.059-.138.134-.274.202-.388a6 6 0 0 1 .253-.382l.025-.035A.5.5 0 0 1 4.4.8m3 0-.003.004-.014.019a4 4 0 0 0-.204.31 2 2 0 0 0-.141.267c-.026.06-.034.092-.037.103v.004a.6.6 0 0 0 .091.248c.075.133.178.272.308.445l.01.012c.118.158.26.347.37.543.112.2.22.455.22.745 0 .188-.065.368-.119.494a3 3 0 0 1-.202.388 5 5 0 0 1-.253.382l-.018.025-.005.008-.002.002A.5.5 0 0 1 6.6 4.2l.003-.004.014-.019a4 4 0 0 0 .204-.31 2 2 0 0 0 .141-.267c.026-.06.034-.092.037-.103a.6.6 0 0 0-.09-.252A4 4 0 0 0 6.6 2.8l-.01-.012a5 5 0 0 1-.37-.543A1.53 1.53 0 0 1 6 1.5c0-.188.065-.368.119-.494.059-.138.134-.274.202-.388a6 6 0 0 1 .253-.382l.025-.035A.5.5 0 0 1 7.4.8m3 0-.003.004-.014.019a4 4 0 0 0-.204.31 2 2 0 0 0-.141.267c-.026.06-.034.092-.037.103v.004a.6.6 0 0 0 .091.248c.075.133.178.272.308.445l.01.012c.118.158.26.347.37.543.112.2.22.455.22.745 0 .188-.065.368-.119.494a3 3 0 0 1-.202.388 5 5 0 0 1-.252.382l-.019.025-.005.008-.002.002A.5.5 0 0 1 9.6 4.2l.003-.004.014-.019a4 4 0 0 0 .204-.31 2 2 0 0 0 .141-.267c.026-.06.034-.092.037-.103a.6.6 0 0 0-.09-.252A4 4 0 0 0 9.6 2.8l-.01-.012a5 5 0 0 1-.37-.543A1.53 1.53 0 0 1 9 1.5c0-.188.065-.368.119-.494.059-.138.134-.274.202-.388a6 6 0 0 1 .253-.382l.025-.035A.5.5 0 0 1 10.4.8"/></svg>',249],['\u2326',76]], p:[['',0],['',0],['',0],['',0]]},
      {m:[[String.fromCharCode(96),53,0,'~'],['1',30,0,'!'],['2',31,0,'@'],['3',32,0,'#'],['4',33,0,'$'],['5',34,0,'%'],['6',35,0,'^'],['7',36,0,'&'],['8',37,0,'*'],['9',38,0,'('],['0',39,0,')'],['-',45,0,'_'],['=',46,0,'+'],['delete',42,83,'small']], p:[['clear',83,0,'small'],['=',103],['/',84],['*',85]]},
      {m:[['tab',43,0,'\u21e5'],['Q',20],['W',26],['E',8],['R',21],['T',23],['Y',28],['U',24],['I',12],['O',18],['P',19],['[',47,0,'{'],[']',48,0,'}'],['\\',49,0,'|'],['home',74,0,'small']], p:[['7',95],['8',96],['9',97],['-',86]]},
      {m:[['caps lock',57,0,'\u21ea'],['A',4],['S',22],['D',7],['F',9],['G',10],['H',11],['J',13],['K',14],['L',15],[';',51,0,':'],["'",52,0,'"'],['return',40,83,'\u21a9'],['end',77,0,'small']], p:[['4',92],['5',93],['6',94],['+',87]]},
      {m:[['shift',225,0,'\u21e7'],['Z',29],['X',27],['C',6],['V',25],['B',5],['N',17],['M',16],[',',54,0,'<'],['.',55,0,'>'],['/',56,0,'?'],['shift',229,0,'\u21e7'],['page up',75,0,'small'],['\u2191',82],['page down',78,0,'small']], p:[['1',89],['2',90],['3',91],['enter',88,0,'\u21a9']]},
      {m:[['control',224,0,'\u2303'],['option',226,0,'\u2325'],['command',227,0,'\u2318'],['',44,255],['command',231,0,'\u2318'],['option',230,0,'\u2325'],['control',228,0,'\u2303'],['\u2190',80],['\u2193',81],['\u2192',79]], p:[['0',98,83],['',0,0,'hide'],['.',99],['',0]]}
    ];
    for (var ri = 0; ri < rows.length; ri++) {
      var el = document.getElementById('kr-' + ri);
      var m = rows[ri].m, p = rows[ri].p;
      for (var i = 0; i < m.length; i++) { el.appendChild(mkKey(m[i][0], m[i][1], m[i][2], m[i][3])); }
      if (p) {
        el.appendChild(mkGap());
        for (var j = 0; j < p.length; j++) {
          var k = mkKey(p[j][0], p[j][1], p[j][2], p[j][3]);
          k.classList.add('numpad');
          el.appendChild(k);
        }
      }
    }
  })();
  </script>
</body>
</html>`)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	// ?as=bytes is the same stream for a page reading it with fetch (/view):
	// Safari takes multipart/x-mixed-replace apart in its network layer and
	// hands fetch nothing, so it gets a type no browser treats specially.
	if r.URL.Query().Get("as") == "bytes" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
	} else {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	}
	w.Header().Set("Cache-Control", "no-cache")

	s.Buf.AddViewer()
	defer s.Buf.RemoveViewer()

	interval := time.Duration(float64(time.Second) / float64(s.Buf.FPS()))

	var hadFrame bool
	for {
		select {
		case <-r.Context().Done():
			return
		default:
			frame := s.Source.Latest()
			if frame == nil {
				if hadFrame {
					// Device disconnected: end the stream, so the
					// viewer sees it end and starts another once
					// capture resumes.
					return
				}
				time.Sleep(interval)
				continue
			}
			hadFrame = true

			fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame))
			if _, err := w.Write(frame); err != nil {
				return
			}
			fmt.Fprint(w, "\r\n")

			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}

			time.Sleep(interval)
		}
	}
}

// snapshotQuality is ?quality=30..95 on a snapshot, or 0 for the stream's.
func snapshotQuality(r *http.Request) int {
	q, err := strconv.Atoi(r.URL.Query().Get("quality"))
	if err != nil || q < 30 || q > 95 {
		return 0
	}
	return q
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	var frame []byte
	if q := snapshotQuality(r); q != 0 && s.Buf != nil {
		frame = s.Buf.LatestAt(q, false)
	}
	if frame == nil {
		frame = s.Source.Latest()
	}
	if frame == nil {
		http.Error(w, "no frame available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(frame)))
	w.Write(frame)
}

func (s *Server) handleRawStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-cache")

	s.Buf.AddViewer()
	defer s.Buf.RemoveViewer()

	interval := time.Duration(float64(time.Second) / float64(s.Buf.FPS()))

	for {
		select {
		case <-r.Context().Done():
			return
		default:
			frame := s.Source.LatestRaw()
			if frame == nil {
				time.Sleep(interval)
				continue
			}

			fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame))
			if _, err := w.Write(frame); err != nil {
				return
			}
			fmt.Fprint(w, "\r\n")

			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}

			time.Sleep(interval)
		}
	}
}

func (s *Server) handleRawSnapshot(w http.ResponseWriter, r *http.Request) {
	var frame []byte
	if q := snapshotQuality(r); q != 0 && s.Buf != nil {
		frame = s.Buf.LatestAt(q, true)
	}
	if frame == nil {
		frame = s.Source.LatestRaw()
	}
	if frame == nil {
		http.Error(w, "no frame available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(frame)))
	w.Write(frame)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	status := "ok"
	switch s.Source.Status() {
	case StatusDisconnected:
		status = "disconnected"
	case StatusConnecting:
		status = "connecting"
	case StatusNoSignal:
		status = "no_signal"
	}

	resp := map[string]interface{}{"status": status}
	if s.SourceType != "" {
		resp["source_type"] = s.SourceType
	}
	if host := readHostStats(); host != nil {
		resp["host"] = host
	}
	if s.Buf != nil {
		resp["quality"] = s.Buf.Quality()
	}
	if mode := s.effectiveInputMode(); mode != "" {
		resp["input_mode"] = mode
	}
	if s.HID != nil && s.HID.Status() == HIDConnected && !s.HID.BoardResponding() {
		resp["hid_board"] = "not_answering"
	}
	if s.Platform != "" {
		resp["platform"] = string(s.Platform)
		resp["pointer_space"] = s.Platform.PointerSpace()
	}
	if s.turn.rotating.Load() {
		resp["rotating"] = true
	}
	if status == "ok" || status == "no_signal" {
		resp["device"] = s.Device
		resp["resolution"] = fmt.Sprintf("%dx%d", s.Buf.Width(), s.Buf.Height())
		resp["fps"] = s.Buf.FPS()
	}
	if content := s.Source.ContentRect(); content != (image.Rectangle{}) {
		resp["content"] = map[string]interface{}{
			"x": content.Min.X, "y": content.Min.Y,
			"width": content.Dx(), "height": content.Dy(),
		}
	}
	if s.Buf != nil {
		resp["crop_mode"] = s.Buf.CropMode()
	}
	cropRect := s.Source.CropRect()
	if cropRect != (image.Rectangle{}) && cropRect != image.Rect(0, 0, s.Buf.Width(), s.Buf.Height()) {
		resp["crop"] = map[string]interface{}{
			"x":      cropRect.Min.X,
			"y":      cropRect.Min.Y,
			"width":  cropRect.Dx(),
			"height": cropRect.Dy(),
		}
	}
	if s.AudioBroadcast != nil && s.AudioBroadcast.IsActive() {
		p := s.AudioBroadcast.Params()
		audio := map[string]interface{}{
			"sampleRate": p.SampleRate,
			"channels":   p.Channels,
		}
		if s.AudioSource != nil {
			if src := s.AudioSource(); src != "" {
				audio["source"] = src
			}
		}
		resp["audio"] = audio
	}
	if s.RTC != nil && s.RTC.Available() {
		resp["webrtc"] = s.RTC.Status()
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(resp)
}

// glider is the input wrapper's glide switch (settledHID).
type glider interface {
	Glide() bool
	SetGlide(bool)
}

func (s *Server) handleAPISettings(w http.ResponseWriter, r *http.Request) {
	if s.Buf == nil {
		http.Error(w, "not available", http.StatusServiceUnavailable)
		return
	}

	glide, _ := s.HID.(glider)
	settings := func() map[string]interface{} {
		m := map[string]interface{}{
			"quality":  s.Buf.Quality(),
			"fps":      s.Buf.FPS(),
			"width":    s.Buf.Width(),
			"height":   s.Buf.Height(),
			"autocrop": s.Buf.Autocrop(),
		}
		if glide != nil {
			m["glide"] = glide.Glide()
		}
		return m
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(settings())
	case http.MethodPut:
		var body struct {
			Quality  *int  `json:"quality"`
			FPS      *int  `json:"fps"`
			Width    *int  `json:"width"`
			Height   *int  `json:"height"`
			Autocrop *bool `json:"autocrop"`
			Glide    *bool `json:"glide"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body.Glide != nil {
			if glide == nil {
				http.Error(w, "this input backend can't glide", http.StatusConflict)
				return
			}
			glide.SetGlide(*body.Glide)
		}
		if body.Quality != nil {
			s.Buf.SetQuality(*body.Quality)
		}
		if body.Autocrop != nil {
			// Auto (true) or off (false): the two-way form of /api/crop's
			// choice, kept with it.
			s.cropMu.Lock()
			s.Buf.SetAutocrop(*body.Autocrop)
			if _, err := s.storeCrops(); err != nil {
				log.Printf("crop: %v", err)
			}
			s.cropMu.Unlock()
		}
		captureChanged := false
		if body.FPS != nil && *body.FPS != s.Buf.FPS() {
			s.Buf.SetFPS(*body.FPS)
			captureChanged = true
		}
		if body.Width != nil && body.Height != nil && (*body.Width != s.Buf.Width() || *body.Height != s.Buf.Height()) {
			s.Buf.SetWidth(*body.Width)
			s.Buf.SetHeight(*body.Height)
			captureChanged = true
		}
		if captureChanged && s.Capture != nil {
			s.Capture.NotifySettingsChanged()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(settings())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCaptureReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Capture == nil {
		http.Error(w, "capture not available", http.StatusServiceUnavailable)
		return
	}
	if err := s.Capture.ResetUSB(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleHIDRelease lets go of every button and contact the target might
// still think is held. A press whose release never arrived -- the window
// lost focus mid-drag, or a test was killed between the two -- leaves the
// target holding it with no way to know better, and recovering otherwise
// means unplugging the board.
func (s *Server) handleHIDRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.HID == nil {
		http.Error(w, "HID not available", http.StatusServiceUnavailable)
		return
	}
	if err := s.HID.MouseRelease(7); err != nil { // left | right | middle
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.HID.Touch(nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// usbDeviceLister names what an input backend presents to the target over
// USB: what a USB reset replugs.
type usbDeviceLister interface {
	USBDevices() []string
}

// usbDevices is what the target sees replugged by a USB reset, or nil when
// the backend can't say.
func (s *Server) usbDevices() []string {
	if l, ok := s.HID.(usbDeviceLister); ok {
		return l.USBDevices()
	}
	return nil
}

// handleUSBReset replugs every device Roadie presents to the target: the
// HID board reboots, or the gadget is unplugged and plugged back in. The
// answer names what the target saw replugged.
func (s *Server) handleUSBReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.HID == nil {
		http.Error(w, "HID not available", http.StatusServiceUnavailable)
		return
	}
	if err := s.HID.ResetHID(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := map[string]any{"status": "ok"}
	if d := s.usbDevices(); d != nil {
		resp["replugged"] = d
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleRelayReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.HID == nil {
		http.Error(w, "HID not available", http.StatusServiceUnavailable)
		return
	}
	if err := s.HID.ResetRelay(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Roadie — Settings</title>
<style>
body { font-family: monospace; max-width: 600px; margin: 40px auto; padding: 0 20px; }
a { color: #0066cc; }
label { display: block; margin: 16px 0 4px; font-weight: bold; }
input[type=range] { width: 100%; }
select { padding: 4px 8px; font-family: monospace; font-size: 1em; }
.val { font-size: 1.2em; }
.info { margin-top: 24px; padding: 12px; background: #f5f5f5; border-radius: 6px; }
.info div { margin: 4px 0; }
nav { margin-bottom: 16px; }
nav a { margin-right: 12px; }
button { font-family: monospace; font-size: 1em; padding: 4px 10px; margin-right: 8px; }
.hint { color: #666; margin: 6px 0; }
#cropStatus div { margin: 2px 0; }
label.opt { display: block; margin: 4px 0; font-weight: normal; }
.head { display: block; margin: 16px 0 4px; font-weight: bold; }
button.help { font-size: 0.8em; width: 1.6em; height: 1.6em; padding: 0; margin: 0 0 0 6px; border: 1px solid #999; border-radius: 50%; background: #fff; color: #555; cursor: pointer; vertical-align: middle; }
button.help[aria-expanded=true] { background: #555; color: #fff; }
</style>
</head>
<body>
<h1>Settings</h1>
<nav><a href="/">/</a> <a href="/view">/view</a></nav>

<label for="quality">JPEG Quality: <span id="qval" class="val"></span></label>
<input id="quality" type="range" min="30" max="95">

<label for="fps">FPS:</label>
<select id="fps">
  <option value="10">10</option>
  <option value="15">15</option>
  <option value="20">20</option>
  <option value="30">30</option>
</select>

<label for="res">Resolution:</label>
<select id="res">
  <option value="1920x1080">1080p</option>
  <option value="1280x720">720p</option>
  <option value="640x480">480p</option>
</select>

<div class="head">Crop:<button class="help" aria-controls="cropHelp" aria-expanded="false" title="About the crop">?</button></div>
<label class="opt"><input type="radio" name="crop" value="auto"> Auto: trim whatever is black at the edges</label>
<label class="opt"><input type="radio" name="crop" value="off"> Off: show the whole frame</label>
<label class="opt"><input type="radio" name="crop" value="saved"> Saved: the crop saved for the way the device is turned</label>
<div id="cropStatus">Loading&hellip;</div>
<p><button id="cropSave">Save current crop</button><button id="cropClear">Clear saved crops</button></p>
<p class="hint" id="cropHelp" hidden>A dark app can fool auto-crop into shrinking the picture. Show something
bright to the screen's edges, such as a white page, and save the crop: Saved is then
chosen, and it survives restarts. Portrait and landscape each keep their own; where
there is none, Saved uses auto-crop.</p>

<label for="zoom">Zoom: <span id="zval" class="val">100%</span></label>
<input id="zoom" type="range" min="50" max="400" value="100">

<label>Viewer toolbar:</label>
<label class="opt"><input type="radio" name="toolbar" value="show"> Show</label>
<label class="opt"><input type="radio" name="toolbar" value="auto"> Auto-hide: tucked off the right edge until the pointer reaches it</label>
<label class="opt"><input type="radio" name="toolbar" value="off"> Off</label>

<div class="info" id="devinfo">Loading device info&hellip;</div>

<script>
(function(){
  var slider = document.getElementById('quality');
  var valSpan = document.getElementById('qval');
  var fpsSelect = document.getElementById('fps');
  var resSelect = document.getElementById('res');
  var zoomSlider = document.getElementById('zoom');
  var zvalSpan = document.getElementById('zval');
  var info = document.getElementById('devinfo');
  var timer = null;

  function applyToUI(d) {
    if (d.quality !== undefined) { slider.value = d.quality; valSpan.textContent = d.quality; }
    if (d.fps !== undefined) { fpsSelect.value = d.fps; }
    if (d.width !== undefined && d.height !== undefined) { resSelect.value = d.width + 'x' + d.height; }
    if (d.zoom !== undefined) { zoomSlider.value = d.zoom; zvalSpan.textContent = d.zoom + '%'; }
  }

  (function loadSettings() {
    var saved = {};
    try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
    // The crop is chosen on the server (see /api/crop); replaying an old
    // autocrop from this browser would undo that.
    if ('autocrop' in saved) {
      delete saved.autocrop;
      try { localStorage.setItem('roadie-settings', JSON.stringify(saved)); } catch(e) {}
    }
    if (Object.keys(saved).length > 0) {
      applyToUI(saved);
      fetch('/api/settings', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(saved)
      });
    } else {
      fetch('/api/settings').then(function(r){ return r.json(); }).then(function(d){
        applyToUI(d);
      });
    }
  })();

  function saveSettings(obj) {
    var saved = {};
    try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
    for (var k in obj) { saved[k] = obj[k]; }
    localStorage.setItem('roadie-settings', JSON.stringify(saved));
  }

  function sendSettings(obj) {
    clearTimeout(timer);
    saveSettings(obj);
    timer = setTimeout(function(){
      fetch('/api/settings', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(obj)
      });
    }, 300);
  }

  slider.oninput = function(){
    valSpan.textContent = slider.value;
    sendSettings({quality: parseInt(slider.value)});
  };

  fpsSelect.onchange = function(){
    sendSettings({fps: parseInt(fpsSelect.value)});
  };

  resSelect.onchange = function(){
    var parts = resSelect.value.split('x');
    sendSettings({width: parseInt(parts[0]), height: parseInt(parts[1])});
  };

  // The viewer's toolbar, kept by this browser for /view (a way back from Off).
  var tbRadios = document.querySelectorAll('input[name=toolbar]');
  (function() {
    var saved = {};
    try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
    var mode = saved.toolbar || (saved.toolbarAutoHide ? 'auto' : 'show');
    tbRadios.forEach(function(r) { r.checked = r.value === mode; });
  })();
  tbRadios.forEach(function(r) {
    r.onchange = function() { if (r.checked) saveSettings({toolbar: r.value}); };
  });

  zoomSlider.oninput = function(){
    zvalSpan.textContent = zoomSlider.value + '%';
    saveSettings({zoom: parseInt(zoomSlider.value)});
  };

  // Each "?" shows or hides the explanation it controls.
  document.querySelectorAll('button.help').forEach(function(b) {
    b.onclick = function() {
      var hint = document.getElementById(b.getAttribute('aria-controls'));
      hint.hidden = !hint.hidden;
      b.setAttribute('aria-expanded', String(!hint.hidden));
    };
  });

  // The crop is chosen, and saved crops kept, by the server, not in this
  // browser.
  var cropRadios = document.querySelectorAll('input[name=crop]');
  var cropStatus = document.getElementById('cropStatus');
  var cropSave = document.getElementById('cropSave');
  var cropClear = document.getElementById('cropClear');
  function showCrop(d) {
    var any = !!(d.saved && (d.saved.portrait || d.saved.landscape));
    cropRadios.forEach(function(r) {
      r.checked = r.value === d.use;
      if (r.value === 'saved') r.disabled = !any;
    });
    var html = '';
    ['portrait', 'landscape'].forEach(function(o) {
      var c = d.saved && d.saved[o];
      html += '<div>' + o + ': ' + (c
        ? c.width + '\u00d7' + c.height + ' at (' + c.x + ', ' + c.y + ')' + (d.mode === 'saved' && d.orientation === o ? ' &mdash; in use' : '')
        : 'none') + '</div>';
    });
    var mode = {saved: 'the saved crop', auto: 'auto-crop', off: 'no crop'}[d.mode] || d.mode;
    html += '<div>Device is ' + (d.orientation || 'turned an unknown way') + '; using ' + mode + '.</div>';
    cropStatus.innerHTML = html;
    cropClear.disabled = !any;
  }
  function cropRequest(method, body) {
    var opts = {method: method};
    if (body) { opts.headers = {'Content-Type': 'application/json'}; opts.body = JSON.stringify(body); }
    fetch('/api/crop', opts).then(function(r) {
      if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
      return r.json();
    }).then(showCrop).catch(function(e) { cropStatus.textContent = e.message; });
  }
  cropRadios.forEach(function(r) {
    r.onchange = function(){ if (r.checked) cropRequest('PUT', {use: r.value}); };
  });
  cropSave.onclick = function(){ cropRequest('POST'); };
  cropClear.onclick = function(){ cropRequest('DELETE'); };
  cropRequest('GET');

  fetch('/health').then(function(r){ return r.json(); }).then(function(d){
    var html = '';
    if (d.device) html += '<div><b>Device:</b> ' + d.device + '</div>';
    if (d.source_type) html += '<div><b>Source:</b> ' + d.source_type + '</div>';
    if (d.resolution) html += '<div><b>Resolution:</b> ' + d.resolution + '</div>';
    if (d.fps) html += '<div><b>FPS:</b> ' + d.fps + '</div>';
    html += '<div><b>Status:</b> ' + d.status + '</div>';
    info.innerHTML = html || 'No device info available';
  });
})();
</script>
</body>
</html>`)
}

func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	if s.AudioBroadcast == nil || !s.AudioBroadcast.IsActive() {
		http.Error(w, "no audio available", http.StatusServiceUnavailable)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // allow any origin for local network use
	})
	if err != nil {
		log.Printf("audio websocket accept: %v", err)
		return
	}
	defer conn.CloseNow()

	ctx := conn.CloseRead(r.Context())

	// Send audio params as first (text) message.
	params := s.AudioBroadcast.Params()
	paramsJSON, _ := json.Marshal(params)
	if err := conn.Write(ctx, websocket.MessageText, paramsJSON); err != nil {
		return
	}

	// Subscribe to audio chunks and forward as binary messages.
	ch, unsub := s.AudioBroadcast.Subscribe()
	defer unsub()

	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-ch:
			if !ok {
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, chunk); err != nil {
				return
			}
		}
	}
}

// handleMic receives PCM audio from the browser and forwards it to the
// HID board's USB microphone (voice input). Binary messages carry 16 kHz
// S16LE mono PCM; the browser paces them in real time, and the board only
// buffers ~100 ms ahead, so no server-side pacing is applied.
func (s *Server) handleMic(w http.ResponseWriter, r *http.Request) {
	if s.HID == nil {
		http.Error(w, "HID not available", http.StatusServiceUnavailable)
		return
	}
	// Default same-origin check. This endpoint writes into the target -- it can
	// speak into a live call -- so a page on some other origin must not be
	// able to open it. Non-browser clients send no Origin header and are
	// unaffected; /soundcheck is served by roadie so it is same-origin.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Printf("/api/mic websocket accept: %v", err)
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary || len(data) == 0 {
			continue
		}
		if err := s.HID.SendAudio(data); err != nil {
			log.Printf("/api/mic: %v", err)
			return
		}
	}
}

// soundcheckPage is served at /soundcheck. It captures the browser's
// microphone and streams it to /api/mic, showing what arrives -- the same
// path the viewer's mic button uses. Served from roadie rather than from a
// file so the page is same-origin with /api/mic, which refuses cross-origin
// connections.
//
//go:embed board/e2e/gesture-page.html
var gesturePageHTML []byte

// handleGestures serves a page that shows what a multi-touch gesture did --
// pinch to scale, twist to rotate -- so a gesture can be checked by eye as
// well as by the test that drives the same file.
func (s *Server) handleGestures(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(gesturePageHTML)
}

//go:embed board/e2e/mic-page.html
var soundcheckPage []byte

func (s *Server) handleSoundcheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(soundcheckPage)
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// The test page maps pointer coordinates the same way /view does, so it
	// needs the same hint about which space the target addresses.
	if s.Platform.PointerSpace() == "screen" {
		fmt.Fprint(w, "<script>window.__roadiePointerSpace='screen';</script>\n")
	}

	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Roadie — HID Test</title>
<style>
* { box-sizing: border-box; }
body { font-family: monospace; max-width: 700px; margin: 40px auto; padding: 0 20px; background: #1a1a1a; color: #e0e0e0; }
h1, h2 { color: #fff; }
nav { margin-bottom: 16px; }
nav a { color: #6af; margin-right: 12px; }
a { color: #6af; }
.section { background: #252525; border-radius: 8px; padding: 16px; margin-bottom: 20px; }
.status { display: inline-block; width: 10px; height: 10px; border-radius: 50%; margin-right: 6px; }
.status.connected { background: #4c4; }
.status.disconnected { background: #c44; }
.status.connecting { background: #ca4; }

/* Mouse trackpad */
#trackpad {
  width: 100%; max-width: 300px; aspect-ratio: 1 / 1;
  border: 2px solid #555; border-radius: 4px;
  position: relative; cursor: crosshair;
  background: #1e1e1e; touch-action: none;
  overflow: hidden;
}
#trackpad-feed {
  position: absolute; top: 0; left: 0; width: 100%; height: 100%;
  object-fit: fill; pointer-events: none; opacity: 0.5;
}
/* Mode toggle */
.mode-toggle { display: flex; gap: 0; margin-bottom: 12px; }
.mode-toggle button { padding: 8px 20px; background: #333; color: #888; border: 1px solid #555; cursor: pointer; font-family: monospace; }
.mode-toggle button:first-child { border-radius: 4px 0 0 4px; }
.mode-toggle button:last-child { border-radius: 0 4px 4px 0; }
.mode-toggle button.active { background: #444; color: #fff; border-color: #6af; }
#crosshair-h, #crosshair-v {
  position: absolute; background: rgba(100,170,255,0.3); pointer-events: none;
}
#crosshair-h { height: 1px; width: 100%; top: 50%; }
#crosshair-v { width: 1px; height: 100%; left: 50%; }
#coords { margin-top: 8px; color: #888; font-size: 0.9em; }
.mouse-btns { margin-top: 8px; }
.mouse-btns button { padding: 6px 16px; margin-right: 8px; background: #333; color: #e0e0e0; border: 1px solid #555; border-radius: 4px; cursor: pointer; }
.mouse-btns button:active { background: #555; }

/* Keyboard */
#keyinput {
  width: 100%; padding: 10px; font-family: monospace; font-size: 16px;
  background: #1e1e1e; color: #e0e0e0; border: 2px solid #555; border-radius: 4px;
  outline: none;
}
#keyinput:focus { border-color: #6af; }
#keylabel { margin-top: 8px; color: #888; font-size: 0.9em; }
#typearea { width: 100%; height: 60px; margin-top: 12px; padding: 8px; font-family: monospace; font-size: 14px; background: #1e1e1e; color: #e0e0e0; border: 1px solid #555; border-radius: 4px; resize: vertical; }
#typebtn { margin-top: 6px; padding: 6px 16px; background: #333; color: #e0e0e0; border: 1px solid #555; border-radius: 4px; cursor: pointer; }
#typebtn:active { background: #555; }

/* Combos */
.combo-grid { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 12px; }
.combo-grid button { padding: 8px 14px; background: #333; color: #e0e0e0; border: 1px solid #555; border-radius: 4px; cursor: pointer; font-family: monospace; }
.gesture-grid { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 8px; }
.gesture-grid button { padding: 8px 14px; background: #333; color: #e0e0e0; border: 1px solid #555; border-radius: 4px; cursor: pointer; font-family: monospace; }
.gesture-grid button:disabled { opacity: 0.4; cursor: default; }
#gesturehint { color: #888; font-size: 13px; line-height: 1.5; }
#gesturehint code { color: #b0b0b0; }
#gesturestatus { margin-top: 8px; color: #888; font-family: monospace; font-size: 13px; }
.combo-grid button:active { background: #555; }
.custom-combo { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.custom-combo label { cursor: pointer; }
.custom-combo input[type=checkbox] { margin-right: 2px; }
#combokey { padding: 6px; background: #1e1e1e; color: #e0e0e0; border: 1px solid #555; border-radius: 4px; font-family: monospace; }
#combosend { padding: 6px 16px; background: #333; color: #e0e0e0; border: 1px solid #555; border-radius: 4px; cursor: pointer; }
</style>
</head>
<body>
<h1>HID Test</h1>
<nav><a href="/">/</a> <a href="/view">/view</a> <a href="/settings">/settings</a></nav>
<p><span class="status" id="statusdot"></span><span id="statustext">connecting...</span></p>

<div class="section">
<h2>Input</h2>
<div class="mode-toggle">
  <button id="modeMouseBtn" class="active" onclick="setMode('mouse')">Mouse</button>
  <button id="modeTouchBtn" onclick="setMode('touch')">Touch</button>
</div>
<div id="trackpad">
  <img id="trackpad-feed" src="/stream">
  <div id="crosshair-h"></div>
  <div id="crosshair-v"></div>
</div>
<div id="coords">x: 0, y: 0</div>
<div class="mouse-btns">
  <button onmousedown="mouseBtn(1,'press')" onmouseup="mouseBtn(1,'release')">Left</button>
  <button onmousedown="mouseBtn(2,'press')" onmouseup="mouseBtn(2,'release')">Right</button>
  <button onmousedown="mouseBtn(4,'press')" onmouseup="mouseBtn(4,'release')">Middle</button>
</div>
</div>

<div class="section">
<h2>Gestures</h2>
<div id="gesturehint">Two contacts, centred on the crosshair. The target has
to see the board as a multi-touch device. A phone does by itself; a Linux
host needs <code>make multitouch-on</code>, which costs it the separate
mouse device until you switch back.</div>
<div class="gesture-grid">
  <button onclick="gesture('pinch-in')">Pinch in</button>
  <button onclick="gesture('pinch-out')">Pinch out</button>
  <button onclick="gesture('scroll-up')">Two-finger up</button>
  <button onclick="gesture('scroll-down')">Two-finger down</button>
  <button onclick="gesture('rotate-cw')">Rotate &#8635;</button>
  <button onclick="gesture('two-tap')">Two-finger tap</button>
</div>
<div id="gesturestatus">&nbsp;</div>
</div>

<div class="section">
<h2>Keyboard</h2>
<input id="keyinput" type="text" placeholder="Click here and type — keys are sent to target" autocomplete="off" autocorrect="off" autocapitalize="off" spellcheck="false">
<div id="keylabel">Press keys to send individual key events</div>
<textarea id="typearea" placeholder="Type text here and click Send to type it on the target"></textarea>
<button id="typebtn" onclick="sendTypeText()">Send Text</button>
</div>

<div class="section">
<h2>Key Combos</h2>
<div class="combo-grid">
  <button onclick="combo([224,6])">Ctrl+C</button>
  <button onclick="combo([224,25])">Ctrl+V</button>
  <button onclick="combo([224,4])">Ctrl+A</button>
  <button onclick="combo([224,29])">Ctrl+Z</button>
  <button onclick="combo([224,22])">Ctrl+S</button>
  <button onclick="combo([226,43])">Alt+Tab</button>
  <button onclick="combo([224,226,76])">Ctrl+Alt+Del</button>
  <button onclick="combo([227])">GUI/Win</button>
</div>
<div class="custom-combo">
  <label><input type="checkbox" id="modCtrl"> Ctrl</label>
  <label><input type="checkbox" id="modShift"> Shift</label>
  <label><input type="checkbox" id="modAlt"> Alt</label>
  <label><input type="checkbox" id="modGui"> GUI</label>
  <select id="combokey">
    <option value="4">A</option><option value="5">B</option><option value="6">C</option>
    <option value="7">D</option><option value="8">E</option><option value="9">F</option>
    <option value="10">G</option><option value="11">H</option><option value="12">I</option>
    <option value="13">J</option><option value="14">K</option><option value="15">L</option>
    <option value="16">M</option><option value="17">N</option><option value="18">O</option>
    <option value="19">P</option><option value="20">Q</option><option value="21">R</option>
    <option value="22">S</option><option value="23">T</option><option value="24">U</option>
    <option value="25">V</option><option value="26">W</option><option value="27">X</option>
    <option value="28">Y</option><option value="29">Z</option>
    <option value="30">1</option><option value="31">2</option><option value="32">3</option>
    <option value="33">4</option><option value="34">5</option><option value="35">6</option>
    <option value="36">7</option><option value="37">8</option><option value="38">9</option>
    <option value="39">0</option><option value="40">Enter</option><option value="41">Esc</option>
    <option value="42">Backspace</option><option value="43">Tab</option><option value="44">Space</option>
    <option value="58">F1</option><option value="59">F2</option><option value="60">F3</option>
    <option value="61">F4</option><option value="62">F5</option><option value="63">F6</option>
    <option value="64">F7</option><option value="65">F8</option><option value="66">F9</option>
    <option value="67">F10</option><option value="68">F11</option><option value="69">F12</option>
    <option value="70">PrtSc</option><option value="73">Insert</option><option value="74">Home</option>
    <option value="75">PgUp</option><option value="76">Delete</option><option value="77">End</option>
    <option value="78">PgDn</option><option value="79">Right</option><option value="80">Left</option>
    <option value="81">Down</option><option value="82">Up</option>
  </select>
  <button id="combosend" onclick="sendCustomCombo()">Send</button>
</div>
</div>

<script>
(function(){
  // USB HID keycode map: JS event.code -> HID keycode
  var KEY_MAP = {
    KeyA:4,KeyB:5,KeyC:6,KeyD:7,KeyE:8,KeyF:9,KeyG:10,KeyH:11,KeyI:12,
    KeyJ:13,KeyK:14,KeyL:15,KeyM:16,KeyN:17,KeyO:18,KeyP:19,KeyQ:20,
    KeyR:21,KeyS:22,KeyT:23,KeyU:24,KeyV:25,KeyW:26,KeyX:27,KeyY:28,KeyZ:29,
    Digit1:30,Digit2:31,Digit3:32,Digit4:33,Digit5:34,Digit6:35,
    Digit7:36,Digit8:37,Digit9:38,Digit0:39,
    Enter:40,Escape:41,Backspace:42,Tab:43,Space:44,
    Minus:45,Equal:46,BracketLeft:47,BracketRight:48,Backslash:49,
    Semicolon:51,Quote:52,Backquote:53,Comma:54,Period:55,Slash:56,
    CapsLock:57,
    F1:58,F2:59,F3:60,F4:61,F5:62,F6:63,F7:64,F8:65,F9:66,F10:67,F11:68,F12:69,
    PrintScreen:70,ScrollLock:71,Pause:72,Insert:73,Home:74,PageUp:75,
    Delete:76,End:77,PageDown:78,
    ArrowRight:79,ArrowLeft:80,ArrowDown:81,ArrowUp:82,
    NumLock:83,
    ControlLeft:224,ShiftLeft:225,AltLeft:226,MetaLeft:227,
    ControlRight:228,ShiftRight:229,AltRight:230,MetaRight:231
  };

  var ws = null;
  var wsReady = false;

  function connect() {
    var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(proto + '//' + location.host + '/api/hid/ws');
    ws.onopen = function() { wsReady = true; updateStatus(); };
    ws.onclose = function() { wsReady = false; updateStatus(); setTimeout(connect, 2000); };
    ws.onerror = function() { wsReady = false; };
  }

  function send(msg) {
    if (ws && wsReady) ws.send(JSON.stringify(msg));
  }

  function updateStatus() {
    fetch('/api/hid/status').then(function(r){return r.json();}).then(function(d){
      var dot = document.getElementById('statusdot');
      var txt = document.getElementById('statustext');
      dot.className = 'status ' + d.status;
      txt.textContent = d.status + (wsReady ? ' (ws)' : '');
    });
  }
  setInterval(updateStatus, 5000);

  connect();

  // Input mode: 'mouse' or 'touch'
  var inputMode = localStorage.getItem('roadie-input-mode') === 'touch' ? 'touch' : 'mouse';
  window.setMode = function(mode) {
    inputMode = mode;
    localStorage.setItem('roadie-input-mode', mode);
    document.getElementById('modeMouseBtn').className = mode === 'mouse' ? 'active' : '';
    document.getElementById('modeTouchBtn').className = mode === 'touch' ? 'active' : '';
  };
  window.setMode(inputMode);

  // Trackpad
  var trackpad = document.getElementById('trackpad');
  var crossH = document.getElementById('crosshair-h');
  var crossV = document.getElementById('crosshair-v');
  var coordsEl = document.getElementById('coords');
  var pendingX = -1, pendingY = -1, mouseDirty = false;

  // Drain pending mouse/touch at fixed rate
  setInterval(function() {
    if (mouseDirty) {
      if (inputMode === 'mouse') {
        send({cmd:'mouse_move', x:pendingX, y:pendingY});
      }
      mouseDirty = false;
    }
  }, 33);

  // Touch mode state
  var touchDirty = false;
  var pendingContacts = [];
  setInterval(function() {
    if (touchDirty) {
      send({cmd:'touch', contacts:pendingContacts});
      touchDirty = false;
    }
  }, 50);

  // Scroll accumulator (rate-limited)
  var pendingScroll = 0, scrollDirty = false;
  var cropX = 0, cropY = 0, cropW = 0, cropH = 0, fullW = 0, fullH = 0;
  // Where the target's picture sits in the frame, whether or not we crop
  // to it. With autocrop off the whole frame is served, and input still
  // has to land on the picture rather than the letterbox bars.
  var contentX = 0, contentY = 0, contentW = 0, contentH = 0;
  setInterval(function() {
    if (scrollDirty) {
      send({cmd:'mouse_scroll', amount:pendingScroll});
      pendingScroll = 0;
      scrollDirty = false;
    }
  }, 50);

  function remapToAbsolute(px, py) {
    // A target addressing its own display wants the position as-is. One
    // addressing the whole captured frame needs the crop folded back in, so
    // the letterbox bars count toward the coordinate. See --platform.
    if (window.__roadiePointerSpace === 'screen') {
      // px,py are fractions of the picture on screen. When the whole frame
      // is served, the target's picture is only part of it, so take the
      // letterbox bars back out before scaling.
      var whole = !((cropW > 0 && cropW !== fullW) || (cropH > 0 && cropH !== fullH));
      if (whole && contentW > 0 && fullW > 0) {
        var fx = Math.max(0, Math.min(1, (px * fullW - contentX) / contentW));
        var fy = Math.max(0, Math.min(1, (py * fullH - contentY) / contentH));
        return { x: Math.round(fx * 32767), y: Math.round(fy * 32767) };
      }
      return { x: Math.round(px * 32767), y: Math.round(py * 32767) };
    }
    var ax = (cropW > 0 && fullW > 0) ? (cropX + px * cropW) / fullW : px;
    var ay = (cropH > 0 && fullH > 0) ? (cropY + py * cropH) / fullH : py;
    return { x: Math.round(ax * 32767), y: Math.round(ay * 32767) };
  }

  function posFromEvent(e) {
    var rect = trackpad.getBoundingClientRect();
    var px = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
    var py = Math.max(0, Math.min(1, (e.clientY - rect.top) / rect.height));
    var abs = remapToAbsolute(px, py);
    return { x: abs.x, y: abs.y, px: px, py: py };
  }

  var lastPx = 0.5, lastPy = 0.5;

  function updateCrosshair(px, py, x, y) {
    lastPx = px; lastPy = py;
    crossH.style.top = (py * 100) + '%';
    crossV.style.left = (px * 100) + '%';
    var label = 'x: ' + x + ', y: ' + y;
    if (cropX !== 0 || cropY !== 0 || (cropW > 0 && cropW !== fullW) || (cropH > 0 && cropH !== fullH)) {
      label += '  (crop: ' + cropX + ',' + cropY + ' ' + cropW + 'x' + cropH + ')';
    }
    coordsEl.textContent = label;
  }

  // --- Mouse mode handlers ---
  trackpad.addEventListener('mousemove', function(e) {
    var p = posFromEvent(e);
    pendingX = p.x; pendingY = p.y;
    mouseDirty = true;
    updateCrosshair(p.px, p.py, p.x, p.y);
    // Touch mode: update drag if mouse button held
    if (inputMode === 'touch' && e.buttons > 0) {
      pendingContacts = [{id:0, tip:true, x:p.x, y:p.y}];
      touchDirty = true;
    }
  });

  var trackpadPressed = false, trackpadButton = 0;
  trackpad.addEventListener('mousedown', function(e) {
    e.preventDefault();
    trackpadPressed = true;
    if (inputMode === 'mouse') {
      var btn = e.button === 2 ? 2 : e.button === 1 ? 4 : 1;
      // Release what actually went down. A secondary click can have its
      // mouseup taken by the context menu, and releasing whatever that
      // event reports then leaves the target holding the button.
      trackpadButton = btn;
      send({cmd:'mouse_press', buttons:btn});
    } else {
      var p = posFromEvent(e);
      pendingContacts = [{id:0, tip:true, x:p.x, y:p.y}];
      send({cmd:'touch', contacts:pendingContacts});
    }
  });

  document.addEventListener('mouseup', function(e) {
    if (!trackpadPressed) return;
    trackpadPressed = false;
    if (inputMode === 'mouse') {
      send({cmd:'mouse_release', buttons:trackpadButton || 1});
      trackpadButton = 0;
    } else {
      var p = posFromEvent(e);
      send({cmd:'touch', contacts:[{id:0, tip:false, x:p.x, y:p.y}]});
      // Lift — send empty
      setTimeout(function(){ send({cmd:'touch', contacts:[]}); }, 20);
    }
  });

  trackpad.addEventListener('contextmenu', function(e) { e.preventDefault(); });

  // --- Two-finger gestures ---
  // Each gesture is a path for two contacts, played out as a series of touch
  // reports. Positions are fractions of the feed, so a gesture keeps its
  // shape whatever the resolution is, and they are centred on the crosshair
  // so you can aim one.
  var gestureBusy = false;

  function clamp01(v) { return Math.max(0, Math.min(1, v)); }

  function contactsAt(ax, ay, bx, by) {
    var a = remapToAbsolute(clamp01(ax), clamp01(ay));
    var b = remapToAbsolute(clamp01(bx), clamp01(by));
    return [{id: 0, tip: true, x: a.x, y: a.y},
            {id: 1, tip: true, x: b.x, y: b.y}];
  }

  // path(t) returns [ax, ay, bx, by] for t from 0 to 1.
  function playGesture(name, path, steps, ms) {
    if (gestureBusy) return;
    gestureBusy = true;
    var buttons = document.querySelectorAll('.gesture-grid button');
    for (var i = 0; i < buttons.length; i++) { buttons[i].disabled = true; }
    var status = document.getElementById('gesturestatus');
    status.textContent = name + ': sending two contacts...';

    function emit(t) {
      var p = path(t);
      send({cmd: 'touch', contacts: contactsAt(p[0], p[1], p[2], p[3])});
    }
    var step = 0;
    emit(0);
    var timer = setInterval(function() {
      step++;
      emit(step / steps);
      if (step < steps) { return; }
      clearInterval(timer);
      send({cmd: 'touch', contacts: []});   // an empty list lifts both
      gestureBusy = false;
      for (var i = 0; i < buttons.length; i++) { buttons[i].disabled = false; }
      status.textContent = name + ': sent';
    }, Math.max(10, ms / steps));
  }

  function gesture(kind) {
    var cx = lastPx, cy = lastPy;
    var near = 0.04, far = 0.20, travel = 0.22, apart = 0.05;
    if (kind === 'pinch-in' || kind === 'pinch-out') {
      var from = kind === 'pinch-in' ? far : near;
      var to = kind === 'pinch-in' ? near : far;
      playGesture(kind.replace('-', ' '), function(t) {
        var d = from + (to - from) * t;
        return [cx - d, cy, cx + d, cy];
      }, 20, 500);
    } else if (kind === 'scroll-up' || kind === 'scroll-down') {
      var dir = kind === 'scroll-up' ? -1 : 1;
      playGesture('two-finger ' + (dir < 0 ? 'up' : 'down'), function(t) {
        var dy = dir * travel * (t - 0.5);
        return [cx - apart, cy + dy, cx + apart, cy + dy];
      }, 20, 450);
    } else if (kind === 'rotate-cw') {
      playGesture('rotate', function(t) {
        var a = t * Math.PI / 2, r = 0.14;
        return [cx - r * Math.cos(a), cy - r * Math.sin(a),
                cx + r * Math.cos(a), cy + r * Math.sin(a)];
      }, 24, 600);
    } else if (kind === 'two-tap') {
      // Down and straight back up: two contacts arrive and leave without
      // moving, which is what a two-finger tap is.
      playGesture('two-finger tap', function() {
        return [cx - 0.06, cy, cx + 0.06, cy];
      }, 3, 120);
    }
  }

  // --- Scroll wheel (mouse mode) ---
  // In notches, as /view counts them: small steps add up.
  var wheelRest = 0;
  trackpad.addEventListener('wheel', function(e) {
    e.preventDefault();
    if (inputMode !== 'mouse') return;
    wheelRest += e.deltaMode === 1 ? e.deltaY : e.deltaMode === 2 ? e.deltaY * 20 : e.deltaY / 25;
    var notches = Math.trunc(wheelRest);
    if (notches !== 0) {
      wheelRest -= notches;
      pendingScroll = Math.max(-127, Math.min(127, pendingScroll + notches));
      scrollDirty = true;
    }
  }, {passive: false});

  // --- Touch handlers (mobile + touch screens) ---
  var activeTouches = {};

  function touchToContacts(e) {
    var contacts = [];
    var rect = trackpad.getBoundingClientRect();
    for (var i = 0; i < Math.min(e.touches.length, 2); i++) {
      var t = e.touches[i];
      var px = Math.max(0, Math.min(1, (t.clientX - rect.left) / rect.width));
      var py = Math.max(0, Math.min(1, (t.clientY - rect.top) / rect.height));
      var abs = remapToAbsolute(px, py);
      contacts.push({id:i, tip:true, x:abs.x, y:abs.y});
    }
    return contacts;
  }

  trackpad.addEventListener('touchstart', function(e) {
    e.preventDefault();
    if (inputMode === 'touch') {
      var contacts = touchToContacts(e);
      send({cmd:'touch', contacts:contacts});
    } else if (e.touches.length === 1) {
      // Mouse mode: single touch = mouse move
      var p = posFromEvent(e.touches[0]);
      pendingX = p.x; pendingY = p.y;
      mouseDirty = true;
      updateCrosshair(p.px, p.py, p.x, p.y);
    }
  }, {passive: false});

  var twoFingerLastY = null;
  var scrollAccum = 0;

  trackpad.addEventListener('touchmove', function(e) {
    e.preventDefault();
    if (inputMode === 'touch') {
      var contacts = touchToContacts(e);
      pendingContacts = contacts;
      touchDirty = true;
      if (contacts.length > 0) {
        var c = contacts[0];
        updateCrosshair(c.x / 32767, c.y / 32767, c.x, c.y);
      }
    } else {
      // Mouse mode
      if (e.touches.length >= 2) {
        // Two-finger scroll
        var avgY = (e.touches[0].clientY + e.touches[1].clientY) / 2;
        if (twoFingerLastY !== null) {
          scrollAccum += (avgY - twoFingerLastY);
          while (Math.abs(scrollAccum) >= 10) {
            var step = scrollAccum > 0 ? 3 : -3;
            pendingScroll = Math.max(-127, Math.min(127, pendingScroll + step));
            scrollDirty = true;
            scrollAccum -= (scrollAccum > 0 ? 10 : -10);
          }
        }
        twoFingerLastY = avgY;
      } else {
        twoFingerLastY = null;
        scrollAccum = 0;
        var p = posFromEvent(e.touches[0]);
        pendingX = p.x; pendingY = p.y;
        mouseDirty = true;
        updateCrosshair(p.px, p.py, p.x, p.y);
      }
    }
  }, {passive: false});

  trackpad.addEventListener('touchend', function(e) {
    e.preventDefault();
    if (inputMode === 'touch') {
      if (e.touches.length === 0) {
        send({cmd:'touch', contacts:[]});
      } else {
        var contacts = touchToContacts(e);
        send({cmd:'touch', contacts:contacts});
      }
    }
    if (e.touches.length < 2) {
      twoFingerLastY = null;
      scrollAccum = 0;
    }
  }, {passive: false});

  // Expose for inline handlers
  window.mouseBtn = function(buttons, action) {
    send({cmd: action === 'press' ? 'mouse_press' : 'mouse_release', buttons: buttons});
  };

  // --- Responsive trackpad aspect ratio ---
  function updateTrackpadAspect() {
    fetch('/health').then(function(r){return r.json();}).then(function(d) {
      var w, h;
      if (d.resolution) {
        var parts = d.resolution.split('x');
        fullW = parseInt(parts[0]); fullH = parseInt(parts[1]);
      }
      if (d.crop) {
        w = d.crop.width; h = d.crop.height;
        cropX = d.crop.x; cropY = d.crop.y;
        cropW = d.crop.width; cropH = d.crop.height;
      } else {
        w = fullW; h = fullH;
        cropX = 0; cropY = 0; cropW = fullW; cropH = fullH;
      }
      if (d.content) {
        contentX = d.content.x; contentY = d.content.y;
        contentW = d.content.width; contentH = d.content.height;
      }
      if (w && h && w > 0 && h > 0) {
        trackpad.style.aspectRatio = w + ' / ' + h;
      } else {
        trackpad.style.aspectRatio = '1 / 1';
      }
    }).catch(function(){});
  }
  updateTrackpadAspect();
  setInterval(updateTrackpadAspect, 5000);

  // Keyboard input
  var keyinput = document.getElementById('keyinput');
  var keylabel = document.getElementById('keylabel');

  keyinput.addEventListener('keydown', function(e) {
    e.preventDefault();
    var hid = KEY_MAP[e.code];
    if (hid !== undefined) {
      send({cmd:'key_press', keycode:hid});
      keylabel.textContent = e.code + ' -> HID ' + hid + ' (press)';
    } else {
      keylabel.textContent = e.code + ' (unmapped)';
    }
  });

  keyinput.addEventListener('keyup', function(e) {
    e.preventDefault();
    var hid = KEY_MAP[e.code];
    if (hid !== undefined) {
      send({cmd:'key_release', keycode:hid});
      keylabel.textContent = e.code + ' -> HID ' + hid + ' (release)';
    }
  });

  // Type text
  window.sendTypeText = function() {
    var ta = document.getElementById('typearea');
    if (ta.value) {
      send({cmd:'type', text:ta.value});
      ta.value = '';
    }
  };

  // Key combos
  window.combo = function(keycodes) {
    // Press all keys, then release in reverse
    for (var i = 0; i < keycodes.length; i++) {
      send({cmd:'key_press', keycode:keycodes[i]});
    }
    setTimeout(function() {
      for (var i = keycodes.length - 1; i >= 0; i--) {
        send({cmd:'key_release', keycode:keycodes[i]});
      }
    }, 50);
  };

  window.sendCustomCombo = function() {
    var keys = [];
    if (document.getElementById('modCtrl').checked) keys.push(224);
    if (document.getElementById('modShift').checked) keys.push(225);
    if (document.getElementById('modAlt').checked) keys.push(226);
    if (document.getElementById('modGui').checked) keys.push(227);
    keys.push(parseInt(document.getElementById('combokey').value));
    window.combo(keys);
  };
})();
</script>
</body>
</html>`)
}

// HID handlers

func (s *Server) handleHIDStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	status := "unavailable"
	resp := map[string]string{}
	if s.HID != nil {
		status = string(s.HID.Status())
		resp["backend"] = s.HID.Backend()
		// The gadget's far end is the phone itself: say whether it has
		// enumerated us ("configured") or what else the controller sees.
		if g, ok := s.HID.(interface{ USBState() string }); ok {
			if st := g.USBState(); st != "" {
				resp["usb_state"] = st
			}
		}
		// Report the bound board so two roadie instances on one host can be
		// told apart.
		if p := s.HID.Port(); p != "" {
			resp["port"] = p
			if serial := s.HID.Serial(); serial != "" {
				resp["serial"] = serial
			}
		}
		// Reaching the relay is not the same as reaching the board behind
		// it. An unpowered target leaves the relay perfectly healthy while
		// nothing at all arrives at the phone.
		if status == string(HIDConnected) && !s.HID.BoardResponding() {
			resp["board"] = "not_answering"
		}
	}
	resp["status"] = status
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleHIDType(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.HID == nil {
		http.Error(w, "HID not available", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.HID.Type(body.Text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleHIDKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.HID == nil {
		http.Error(w, "HID not available", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Keycode int    `json:"keycode"`
		Action  string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	switch body.Action {
	case "press":
		err = s.HID.KeyPress(body.Keycode)
	case "release":
		err = s.HID.KeyRelease(body.Keycode)
	case "click":
		err = s.HID.KeyPress(body.Keycode)
		if err == nil {
			err = s.HID.KeyRelease(body.Keycode)
		}
	default:
		http.Error(w, "action must be press, release, or click", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleHIDMouseMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.HID == nil {
		http.Error(w, "HID not available", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.HID.MouseMove(body.X, body.Y); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleHIDMouseClick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.HID == nil {
		http.Error(w, "HID not available", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Buttons int    `json:"buttons"`
		Action  string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Buttons == 0 {
		body.Buttons = 1
	}
	var err error
	switch body.Action {
	case "press":
		err = s.HID.MousePress(body.Buttons)
	case "release":
		err = s.HID.MouseRelease(body.Buttons)
	default:
		err = s.HID.MouseClick(body.Buttons)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleHIDMouseScroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.HID == nil {
		http.Error(w, "HID not available", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Amount int `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.HID.MouseScroll(body.Amount); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleHIDTouch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.HID == nil {
		http.Error(w, "HID not available", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Contacts []TouchContact `json:"contacts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.HID.Touch(body.Contacts); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// releasesOnly is whether a viewer's input only lets go of something.
func releasesOnly(cmd string, contacts []TouchContact) bool {
	switch cmd {
	case "mouse_release", "key_release":
		return true
	case "touch":
		for _, c := range contacts {
			if c.Tip {
				return false
			}
		}
		return true
	}
	return false
}

func (s *Server) handleHIDWebSocket(w http.ResponseWriter, r *http.Request) {
	if s.HID == nil {
		http.Error(w, "HID not available", http.StatusServiceUnavailable)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("HID websocket accept: %v", err)
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg struct {
			Cmd      string         `json:"cmd"`
			X        int            `json:"x"`
			Y        int            `json:"y"`
			Keycode  int            `json:"keycode"`
			Buttons  int            `json:"buttons"`
			Text     string         `json:"text"`
			Amount   int            `json:"amount"`
			Contacts []TouchContact `json:"contacts"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		// While Roadie turns the screen it clicks through AssistiveTouch's
		// menu, and a viewer's pointer moving under it would spoil that.
		// Viewers hold back once /health says so, a second later at most;
		// meanwhile only releases go through, so nothing stays held.
		if s.turn.rotating.Load() && !releasesOnly(msg.Cmd, msg.Contacts) {
			continue
		}
		switch msg.Cmd {
		case "mouse_move":
			s.HID.MouseMove(msg.X, msg.Y)
		case "mouse_click":
			if msg.Buttons == 0 {
				msg.Buttons = 1
			}
			s.HID.MouseClick(msg.Buttons)
		case "mouse_press":
			if msg.Buttons == 0 {
				msg.Buttons = 1
			}
			s.HID.MousePress(msg.Buttons)
		case "mouse_release":
			if msg.Buttons == 0 {
				msg.Buttons = 1
			}
			s.HID.MouseRelease(msg.Buttons)
		case "type":
			s.HID.Type(msg.Text)
		case "key_press":
			s.HID.KeyPress(msg.Keycode)
		case "key_release":
			s.HID.KeyRelease(msg.Keycode)
		case "mouse_scroll":
			s.HID.MouseScroll(msg.Amount)
		case "touch":
			s.HID.Touch(msg.Contacts)
		}
	}
}
