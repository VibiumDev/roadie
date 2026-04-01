package main

import (
	"encoding/json"
	"fmt"
	"image"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// Server holds the state needed by HTTP handlers.
type Server struct {
	Source         FrameSource
	Buf            *FrameBuffer
	Device         string
	AudioBroadcast *AudioBroadcaster
	SourceType     string // "hardware" or "http"
	HID            *HIDController
	Capture        *CaptureManager
}

// NewMux wires up all HTTP routes and returns a handler.
func NewMux(s *Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/view", s.handleView)
	mux.HandleFunc("/stream", s.handleStream)
	mux.HandleFunc("/snapshot", s.handleSnapshot)
	mux.HandleFunc("/raw-stream", s.handleRawStream)
	mux.HandleFunc("/raw-snapshot", s.handleRawSnapshot)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/settings", s.handleSettings)
	mux.HandleFunc("/api/settings", s.handleAPISettings)
	mux.HandleFunc("/audio", s.handleAudio)
	mux.HandleFunc("/test", s.handleTest)
	mux.HandleFunc("/api/capture/reset", s.handleCaptureReset)
	mux.HandleFunc("/api/hid/reset", s.handleHIDReset)
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
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Roadie</title>
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
	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Roadie</title><link rel="icon" href="data:,">
<style>
  :root { --page-bg:#1a1a1a; --kbd-bg:#222; --kbd-key:rgba(255,255,255,0.12); --kbd-key-active:rgba(255,255,255,0.25); --kbd-text:#fff; --kbd-border:transparent; }
  :root[data-theme="light"] { --page-bg:#e8e8ed; --kbd-bg:#d4d4d9; --kbd-key:rgba(255,255,255,0.85); --kbd-key-active:rgba(255,255,255,0.55); --kbd-text:#1a1a1a; --kbd-border:rgba(0,0,0,0.08); }
  @media (prefers-color-scheme:light) { :root[data-theme="system"] { --page-bg:#e8e8ed; --kbd-bg:#d4d4d9; --kbd-key:rgba(255,255,255,0.85); --kbd-key-active:rgba(255,255,255,0.55); --kbd-text:#1a1a1a; --kbd-border:rgba(0,0,0,0.08); } }
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
</style>
</head>
<body style="margin:0; display:flex; height:100vh; overflow:hidden; overscroll-behavior:none;">
  <div style="flex:1; min-width:0; display:flex; flex-direction:column;">
    <div id="viewer" style="position:relative; flex:1; min-width:0; min-height:0; overflow:hidden;">
      <div id="overlay" style="display:flex; position:absolute; inset:0; background:rgba(0,0,0,0.85); color:#fff; font-family:monospace; font-size:1.2em; justify-content:center; align-items:center; text-align:center; z-index:10;">
        Connecting&hellip;
      </div>
      <img id="feed" draggable="false" oncontextmenu="return false" style="max-width:100%; max-height:100%; display:none; touch-action:none; cursor:crosshair;">
    </div>
    <div id="onscreen-kbd" style="display:none; background:var(--kbd-bg); user-select:none; -webkit-user-select:none; overflow-x:auto;">
      <div id="kbd-inner" style="padding:6px 8px;">
      <div class="kr" id="kr-0"></div>
      <div class="kr" id="kr-1"></div>
      <div class="kr" id="kr-2"></div>
      <div class="kr" id="kr-3"></div>
      <div class="kr" id="kr-4"></div>
      <div class="kr" id="kr-5"></div>
      </div>
    </div>
  </div>
  <div id="toolbar" style="position:relative; display:flex; flex-direction:column; gap:4px; padding:8px 6px;">
    <style>#toolbar button { outline:none; color:#ccc; } #toolbar button:focus { outline:none; }</style>
    <button id="qbtn" style="width:36px; height:36px; background:rgba(50,50,50,0.9); border:1px solid rgba(255,255,255,0.15); border-radius:6px; cursor:pointer; padding:0; display:flex; align-items:center; justify-content:center;" title="Settings"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M9.5 13a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0m0-5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0m0-5a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0"/></svg></button>
    <button id="unmute" style="width:36px; height:36px; background:rgba(50,50,50,0.9); border:1px solid rgba(255,255,255,0.15); border-radius:6px; cursor:pointer; padding:0; display:none; align-items:center; justify-content:center;" title="Toggle audio"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M6.717 3.55A.5.5 0 0 1 7 4v8a.5.5 0 0 1-.812.39L3.825 10.5H1.5A.5.5 0 0 1 1 10V6a.5.5 0 0 1 .5-.5h2.325l2.363-1.89a.5.5 0 0 1 .529-.06m7.137 2.096a.5.5 0 0 1 0 .708L12.207 8l1.647 1.646a.5.5 0 0 1-.708.708L11.5 8.707l-1.646 1.647a.5.5 0 0 1-.708-.708L10.793 8 9.146 6.354a.5.5 0 1 1 .708-.708L11.5 7.293l1.646-1.647a.5.5 0 0 1 .708 0"/></svg></button>
    <button id="kbdBtn" style="width:36px; height:36px; background:rgba(50,50,50,0.9); border:1px solid rgba(255,255,255,0.15); border-radius:6px; cursor:pointer; padding:0; display:flex; align-items:center; justify-content:center; opacity:0.4;" title="On-screen keyboard"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" fill="currentColor" viewBox="0 0 16 16"><path d="M0 6a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v5a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2zm13 .25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25h-.5a.25.25 0 0 0-.25.25M2.25 8a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h.5A.25.25 0 0 0 3 8.75v-.5A.25.25 0 0 0 2.75 8zM4 8.25v.5c0 .138.112.25.25.25h.5A.25.25 0 0 0 5 8.75v-.5A.25.25 0 0 0 4.75 8h-.5a.25.25 0 0 0-.25.25M6.25 8a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h.5A.25.25 0 0 0 7 8.75v-.5A.25.25 0 0 0 6.75 8zM8 8.25v.5c0 .138.112.25.25.25h.5A.25.25 0 0 0 9 8.75v-.5A.25.25 0 0 0 8.75 8h-.5a.25.25 0 0 0-.25.25M13.25 8a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25zm0 2a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25zm-3-2a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h1.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25zm.75 2.25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25h-.5a.25.25 0 0 0-.25.25M11.25 6a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25zM9 6.25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5A.25.25 0 0 0 9.75 6h-.5a.25.25 0 0 0-.25.25M7.25 6a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h.5A.25.25 0 0 0 8 6.75v-.5A.25.25 0 0 0 7.75 6zM5 6.25v.5c0 .138.112.25.25.25h.5A.25.25 0 0 0 6 6.75v-.5A.25.25 0 0 0 5.75 6h-.5a.25.25 0 0 0-.25.25M2.25 6a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h1.5A.25.25 0 0 0 4 6.75v-.5A.25.25 0 0 0 3.75 6zM2 10.25v.5c0 .138.112.25.25.25h.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25h-.5a.25.25 0 0 0-.25.25M4.25 10a.25.25 0 0 0-.25.25v.5c0 .138.112.25.25.25h5.5a.25.25 0 0 0 .25-.25v-.5a.25.25 0 0 0-.25-.25z"/></svg></button>
    <div id="qslider" style="display:none; position:absolute; top:8px; z-index:21; background:rgba(50,50,50,0.95); border:1px solid rgba(255,255,255,0.15); border-radius:8px; padding:10px 14px; color:#fff; font-family:monospace; font-size:0.85em; white-space:nowrap;">
      <div style="display:flex; align-items:center; gap:8px; margin-bottom:8px;">
        <label style="min-width:55px;">Quality</label>
        <input id="qrange" type="range" min="30" max="95" style="width:120px; vertical-align:middle;">
        <span id="qval" style="min-width:2em; text-align:right;"></span>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-bottom:8px;">
        <label style="min-width:55px;">FPS</label>
        <select id="fpsSelect" style="background:#333; color:#fff; border:1px solid #555; border-radius:4px; padding:2px 6px; font-family:monospace;">
          <option value="10">10</option>
          <option value="15">15</option>
          <option value="20">20</option>
          <option value="30">30</option>
        </select>
      </div>
      <div style="display:flex; align-items:center; gap:8px;">
        <label style="min-width:55px;">Size</label>
        <select id="resSelect" style="background:#333; color:#fff; border:1px solid #555; border-radius:4px; padding:2px 6px; font-family:monospace;">
          <option value="1920x1080">1080p</option>
          <option value="1280x720">720p</option>
          <option value="640x480">480p</option>
        </select>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Input</label>
        <div style="display:flex; gap:0;">
          <button id="modeMouseBtn" style="padding:2px 10px; background:#444; color:#fff; border:1px solid #6af; border-radius:4px 0 0 4px; font-family:monospace; cursor:pointer;">Mouse</button>
          <button id="modeTouchBtn" style="padding:2px 10px; background:#333; color:#888; border:1px solid #555; border-radius:0 4px 4px 0; font-family:monospace; cursor:pointer;">Touch</button>
        </div>
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
        <label style="cursor:pointer; display:flex; align-items:center; gap:4px;"><input id="autocropCheck" type="checkbox" checked> Auto</label>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Zoom</label>
        <input id="zoomRange" type="range" min="50" max="400" value="100" style="width:120px; vertical-align:middle;">
        <span id="zoomVal" style="min-width:3em; text-align:right;">100%</span>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Align</label>
        <div style="display:flex; gap:0;">
          <button id="alignTLBtn" style="padding:2px 10px; background:#444; color:#fff; border:1px solid #6af; border-radius:4px 0 0 4px; font-family:monospace; cursor:pointer;">Top-Left</button>
          <button id="alignCenterBtn" style="padding:2px 10px; background:#333; color:#888; border:1px solid #555; border-radius:0 4px 4px 0; font-family:monospace; cursor:pointer;">Center</button>
        </div>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Theme</label>
        <div style="display:flex; gap:0;">
          <button id="themeDarkBtn" style="padding:2px 10px; background:#333; color:#888; border:1px solid #555; border-radius:4px 0 0 4px; font-family:monospace; cursor:pointer;">Dark</button>
          <button id="themeLightBtn" style="padding:2px 10px; background:#333; color:#888; border:1px solid #555; border-radius:0; font-family:monospace; cursor:pointer;">Light</button>
          <button id="themeSystemBtn" style="padding:2px 10px; background:#444; color:#fff; border:1px solid #6af; border-radius:0 4px 4px 0; font-family:monospace; cursor:pointer;">System</button>
        </div>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px; padding-top:8px; border-top:1px solid rgba(255,255,255,0.1);">
        <label style="min-width:55px;">Video</label>
        <button id="resetVideoBtn" style="padding:2px 10px; background:#633; color:#fff; border:1px solid #955; border-radius:4px; font-family:monospace; cursor:pointer; position:relative; overflow:hidden;"></button>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">HID</label>
        <button id="resetHIDBtn" style="padding:2px 10px; background:#633; color:#fff; border:1px solid #955; border-radius:4px; font-family:monospace; cursor:pointer; position:relative; overflow:hidden;"></button>
      </div>
      <div style="display:flex; align-items:center; gap:8px; margin-top:8px;">
        <label style="min-width:55px;">Relay</label>
        <button id="resetRelayBtn" style="padding:2px 10px; background:#633; color:#fff; border:1px solid #955; border-radius:4px; font-family:monospace; cursor:pointer; position:relative; overflow:hidden;"></button>
      </div>
    </div>
  </div>
  <script>
  (function(){
    var img = document.getElementById('feed');
    var overlay = document.getElementById('overlay');
    var unmuteBtn = document.getElementById('unmute');
    var wasOk = false;

    var healthOk = false;
    var cropX = 0, cropY = 0, cropW = 0, cropH = 0, fullW = 0, fullH = 0;

    function showOverlay(msg) {
      overlay.textContent = msg || 'Disconnected \u2014 waiting for capture device\u2026';
      overlay.style.display = 'flex';
    }
    function startStream() {
      if (!wasOk) {
        img.src = '/stream?' + Date.now();
        img.style.display = '';
        wasOk = true;
      }
    }
    function tryHideOverlay() {
      if (healthOk) {
        overlay.style.display = 'none';
      }
    }

    img.onload = function() { tryHideOverlay(); syncKbdWidth(); };
    img.onerror = function() { wasOk = false; showOverlay(); };
    var prevHealthOk = true;

    function poll() {
      fetch('/health').then(function(r){ return r.json(); }).then(function(data){
        if (data.status === 'ok') {
          if (!prevHealthOk) { wasOk = false; }
          healthOk = true;
          prevHealthOk = true;
          startStream();
          tryHideOverlay();
        } else {
          healthOk = false;
          prevHealthOk = false;
          if (data.status === 'no_signal') { showOverlay('No signal \u2014 check HDMI connection to capture device'); }
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
        // Show unmute button when audio is available.
        if (data.audio) { unmuteBtn.style.display = 'flex'; }
      }).catch(function(){ showOverlay(); });
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

    // --- Settings panel ---
    var qbtn = document.getElementById('qbtn');
    var qslider = document.getElementById('qslider');
    var qrange = document.getElementById('qrange');
    var qval = document.getElementById('qval');
    var fpsSelect = document.getElementById('fpsSelect');
    var resSelect = document.getElementById('resSelect');
    var autocropCheck = document.getElementById('autocropCheck');
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
      if (d.autocrop !== undefined) { autocropCheck.checked = d.autocrop; }
    }

    (function loadSettings() {
      var saved = {};
      try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
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
      if (window.innerWidth - tr.right > 230) {
        qslider.style.left = 'calc(100% + 2px)';
        qslider.style.right = '';
      } else {
        qslider.style.right = 'calc(100% - 2px)';
        qslider.style.left = '';
      }
    }

    qbtn.onclick = function() {
      var vis = qslider.style.display !== 'none';
      if (vis) {
        qslider.style.display = 'none';
      } else {
        positionPanel();
        qslider.style.display = 'block';
      }
    };

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

    autocropCheck.onchange = function() {
      sendSettings({autocrop: autocropCheck.checked});
    };

    function applyAlignment(mode) {
      alignment = mode;
      var viewer = document.getElementById('viewer');
      if (mode === 'center') {
        alignTLBtn.style.background = '#333'; alignTLBtn.style.color = '#888'; alignTLBtn.style.borderColor = '#555';
        alignCenterBtn.style.background = '#444'; alignCenterBtn.style.color = '#fff'; alignCenterBtn.style.borderColor = '#6af';
        viewer.style.display = 'flex';
        viewer.style.justifyContent = 'center';
        viewer.style.alignItems = 'center';
      } else {
        alignCenterBtn.style.background = '#333'; alignCenterBtn.style.color = '#888'; alignCenterBtn.style.borderColor = '#555';
        alignTLBtn.style.background = '#444'; alignTLBtn.style.color = '#fff'; alignTLBtn.style.borderColor = '#6af';
        viewer.style.display = '';
        viewer.style.justifyContent = '';
        viewer.style.alignItems = '';
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
      if (saved.alignment) { applyAlignment(saved.alignment); }
      if (saved.zoom !== undefined) { applyZoom(saved.zoom); }
    })();

    // --- Theme ---
    var themeBtns = {dark: document.getElementById('themeDarkBtn'), light: document.getElementById('themeLightBtn'), system: document.getElementById('themeSystemBtn')};
    function applyTheme(t) {
      document.documentElement.setAttribute('data-theme', t);
      for (var k in themeBtns) {
        if (k === t) { themeBtns[k].style.background = '#444'; themeBtns[k].style.color = '#fff'; themeBtns[k].style.borderColor = '#6af'; }
        else { themeBtns[k].style.background = '#333'; themeBtns[k].style.color = '#888'; themeBtns[k].style.borderColor = '#555'; }
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
    setupResetBtn('resetHIDBtn', '/api/hid/reset');
    setupResetBtn('resetRelayBtn', '/api/relay/reset');

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
      if (hidWs && hidReady) hidWs.send(JSON.stringify(msg));
    }
    hidConnect();

    // --- Input mode toggle ---
    var inputMode = localStorage.getItem('roadie-input-mode') === 'touch' ? 'touch' : 'mouse';
    var modeMouseBtn = document.getElementById('modeMouseBtn');
    var modeTouchBtn = document.getElementById('modeTouchBtn');
    function setInputMode(mode) {
      inputMode = mode;
      localStorage.setItem('roadie-input-mode', mode);
      modeMouseBtn.style.background = mode === 'mouse' ? '#444' : '#333';
      modeMouseBtn.style.color = mode === 'mouse' ? '#fff' : '#888';
      modeMouseBtn.style.borderColor = mode === 'mouse' ? '#6af' : '#555';
      modeTouchBtn.style.background = mode === 'touch' ? '#444' : '#333';
      modeTouchBtn.style.color = mode === 'touch' ? '#fff' : '#888';
      modeTouchBtn.style.borderColor = mode === 'touch' ? '#6af' : '#555';
    }
    setInputMode(inputMode);
    modeMouseBtn.onclick = function() { setInputMode('mouse'); scheduleHide(); };
    modeTouchBtn.onclick = function() { setInputMode('touch'); scheduleHide(); };

    // --- Coordinate helpers ---
    function remapToAbsolute(px, py) {
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
    var pendingX = -1, pendingY = -1, mouseDirty = false;
    setInterval(function() {
      if (mouseDirty) {
        hidSend({cmd:'mouse_move', x:pendingX, y:pendingY});
        mouseDirty = false;
      }
    }, 100);

    var pendingContacts = [], touchDirty = false;
    setInterval(function() {
      if (touchDirty) {
        hidSend({cmd:'touch', contacts:pendingContacts});
        touchDirty = false;
      }
    }, 50);

    var pendingScroll = 0, scrollDirty = false;
    setInterval(function() {
      if (scrollDirty) {
        hidSend({cmd:'mouse_scroll', amount:pendingScroll});
        pendingScroll = 0;
        scrollDirty = false;
      }
    }, 50);

    // --- Mouse handlers on feed ---
    img.addEventListener('mousemove', function(e) {
      var p = posFromFeed(e);
      if (inputMode === 'mouse') {
        pendingX = p.x; pendingY = p.y; mouseDirty = true;
      } else if (e.buttons > 0) {
        pendingContacts = [{id:0, tip:true, x:p.x, y:p.y}];
        touchDirty = true;
      }
    });

    var feedPressed = false;
    img.addEventListener('mousedown', function(e) {
      e.preventDefault();
      feedPressed = true;
      if (inputMode === 'mouse') {
        var btn = e.button === 2 ? 2 : e.button === 1 ? 4 : 1;
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
        var btn = e.button === 2 ? 2 : e.button === 1 ? 4 : 1;
        hidSend({cmd:'mouse_release', buttons:btn});
      } else {
        var p = posFromFeed(e);
        hidSend({cmd:'touch', contacts:[{id:0, tip:false, x:p.x, y:p.y}]});
        setTimeout(function(){ hidSend({cmd:'touch', contacts:[]}); }, 20);
      }
    });

    // --- Scroll wheel on feed ---
    img.addEventListener('wheel', function(e) {
      e.preventDefault();
      if (inputMode !== 'mouse') return;
      var amount;
      if (e.deltaMode === 1) { amount = Math.round(e.deltaY); }
      else { amount = Math.round(e.deltaY / 25); }
      amount = Math.max(-127, Math.min(127, amount));
      if (amount !== 0) {
        pendingScroll = amount;
        scrollDirty = true;
      }
    }, {passive: false});

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

    document.addEventListener('keydown', function(e) {
      if (!kbdCapture) return;
      var tag = e.target.tagName;
      if (tag === 'INPUT' || tag === 'SELECT') return;
      var hid = KEY_MAP[e.code];
      if (hid !== undefined) {
        e.preventDefault();
        hidSend({cmd:'key_press', keycode:hid});
      }
    });
    document.addEventListener('keyup', function(e) {
      if (!kbdCapture) return;
      var tag = e.target.tagName;
      if (tag === 'INPUT' || tag === 'SELECT') return;
      var hid = KEY_MAP[e.code];
      if (hid !== undefined) {
        e.preventDefault();
        hidSend({cmd:'key_release', keycode:hid});
      }
    });

    // --- On-screen keyboard ---
    var oskPanel = document.getElementById('onscreen-kbd');
    var kbdInner = document.getElementById('kbd-inner');
    var kbdBtn = document.getElementById('kbdBtn');
    var oskVisible = false;
    var kbdZoomPct = 100;

    function syncKbdWidth() {
      if (!oskVisible) return;
      var w = img.clientWidth;
      if (w > 0) {
        oskPanel.style.maxWidth = w + 'px';
        oskPanel.style.margin = '0 auto';
      }
    }
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
      {m:[['esc',41],['F1',58,0,'small'],['F2',59,0,'small'],['F3',60,0,'small'],['F4',61,0,'small'],['F5',62,0,'small'],['F6',63,0,'small'],['F7',64,0,'small'],['F8',65,0,'small'],['F9',66,0,'small'],['F10',67,0,'small'],['F11',68,0,'small'],['F12',69,0,'small'],['<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" fill="currentColor" viewBox="0 0 16 16"><path fill-rule="evenodd" d="M.5 6a.5.5 0 0 0-.488.608l1.652 7.434A2.5 2.5 0 0 0 4.104 16h5.792a2.5 2.5 0 0 0 2.44-1.958l.131-.59a3 3 0 0 0 1.3-5.854l.221-.99A.5.5 0 0 0 13.5 6zM13 12.5a2 2 0 0 1-.316-.025l.867-3.898A2.001 2.001 0 0 1 13 12.5M2.64 13.825 1.123 7h11.754l-1.517 6.825A1.5 1.5 0 0 1 9.896 15H4.104a1.5 1.5 0 0 1-1.464-1.175"/><path d="m4.4.8-.003.004-.014.019a4 4 0 0 0-.204.31 2 2 0 0 0-.141.267c-.026.06-.034.092-.037.103v.004a.6.6 0 0 0 .091.248c.075.133.178.272.308.445l.01.012c.118.158.26.347.37.543.112.2.22.455.22.745 0 .188-.065.368-.119.494a3 3 0 0 1-.202.388 5 5 0 0 1-.253.382l-.018.025-.005.008-.002.002A.5.5 0 0 1 3.6 4.2l.003-.004.014-.019a4 4 0 0 0 .204-.31 2 2 0 0 0 .141-.267c.026-.06.034-.092.037-.103a.6.6 0 0 0-.09-.252A4 4 0 0 0 3.6 2.8l-.01-.012a5 5 0 0 1-.37-.543A1.53 1.53 0 0 1 3 1.5c0-.188.065-.368.119-.494.059-.138.134-.274.202-.388a6 6 0 0 1 .253-.382l.025-.035A.5.5 0 0 1 4.4.8m3 0-.003.004-.014.019a4 4 0 0 0-.204.31 2 2 0 0 0-.141.267c-.026.06-.034.092-.037.103v.004a.6.6 0 0 0 .091.248c.075.133.178.272.308.445l.01.012c.118.158.26.347.37.543.112.2.22.455.22.745 0 .188-.065.368-.119.494a3 3 0 0 1-.202.388 5 5 0 0 1-.253.382l-.018.025-.005.008-.002.002A.5.5 0 0 1 6.6 4.2l.003-.004.014-.019a4 4 0 0 0 .204-.31 2 2 0 0 0 .141-.267c.026-.06.034-.092.037-.103a.6.6 0 0 0-.09-.252A4 4 0 0 0 6.6 2.8l-.01-.012a5 5 0 0 1-.37-.543A1.53 1.53 0 0 1 6 1.5c0-.188.065-.368.119-.494.059-.138.134-.274.202-.388a6 6 0 0 1 .253-.382l.025-.035A.5.5 0 0 1 7.4.8m3 0-.003.004-.014.019a4 4 0 0 0-.204.31 2 2 0 0 0-.141.267c-.026.06-.034.092-.037.103v.004a.6.6 0 0 0 .091.248c.075.133.178.272.308.445l.01.012c.118.158.26.347.37.543.112.2.22.455.22.745 0 .188-.065.368-.119.494a3 3 0 0 1-.202.388 5 5 0 0 1-.252.382l-.019.025-.005.008-.002.002A.5.5 0 0 1 9.6 4.2l.003-.004.014-.019a4 4 0 0 0 .204-.31 2 2 0 0 0 .141-.267c.026-.06.034-.092.037-.103a.6.6 0 0 0-.09-.252A4 4 0 0 0 9.6 2.8l-.01-.012a5 5 0 0 1-.37-.543A1.53 1.53 0 0 1 9 1.5c0-.188.065-.368.119-.494.059-.138.134-.274.202-.388a6 6 0 0 1 .253-.382l.025-.035A.5.5 0 0 1 10.4.8"/></svg>',249],['delete',76,0,'\u2326']], p:[['clear',83,0,'small'],['=',103],['/',84],['*',85]]},
      {m:[[String.fromCharCode(96),53,0,'~'],['1',30,0,'!'],['2',31,0,'@'],['3',32,0,'#'],['4',33,0,'$'],['5',34,0,'%'],['6',35,0,'^'],['7',36,0,'&'],['8',37,0,'*'],['9',38,0,'('],['0',39,0,')'],['-',45,0,'_'],['=',46,0,'+'],['delete',42,0,'\u232b'],['home',74,0,'small']], p:[['7',95],['8',96],['9',97],['-',86]]},
      {m:[['tab',43,0,'\u21e5'],['Q',20],['W',26],['E',8],['R',21],['T',23],['Y',28],['U',24],['I',12],['O',18],['P',19],['[',47,0,'{'],[']',48,0,'}'],['\\',49,0,'|'],['end',77,0,'small']], p:[['4',92],['5',93],['6',94],['+',87]]},
      {m:[['caps lock',57,0,'\u21ea'],['A',4],['S',22],['D',7],['F',9],['G',10],['H',11],['J',13],['K',14],['L',15],[';',51,0,':'],["'",52,0,'"'],['return',40,0,'\u21a9'],['page up',75,0,'small'],['page down',78,0,'small']], p:[['1',89],['2',90],['3',91],['enter',88,0,'\u21a9']]},
      {m:[['shift',225,0,'\u21e7'],['Z',29],['X',27],['C',6],['V',25],['B',5],['N',17],['M',16],[',',54,0,'<'],['.',55,0,'>'],['/',56,0,'?'],['shift',229,0,'\u21e7'],['',0],['\u2191',82],['',0]], p:[['0',98,83],['',0,0,'hide'],['.',99],['',0]]},
      {m:[['control',224,0,'\u2303'],['option',226,0,'\u2325'],['command',227,0,'\u2318'],['',44,255],['command',231,0,'\u2318'],['option',230,0,'\u2325'],['control',228,0,'\u2303'],['\u2190',80],['\u2193',81],['\u2192',79]], p:[['',0],['',0],['',0],['',0]]}
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
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
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
					// Device disconnected — close the stream so the
					// browser's <img> fires onerror and reconnects
					// when capture resumes.
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

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	frame := s.Source.Latest()
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
	frame := s.Source.LatestRaw()
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
	if s.Buf != nil {
		resp["quality"] = s.Buf.Quality()
	}
	if status == "ok" || status == "no_signal" {
		resp["device"] = s.Device
		resp["resolution"] = fmt.Sprintf("%dx%d", s.Buf.Width(), s.Buf.Height())
		resp["fps"] = s.Buf.FPS()
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
		resp["audio"] = map[string]interface{}{
			"sampleRate": p.SampleRate,
			"channels":   p.Channels,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(resp)
}

func (s *Server) handleAPISettings(w http.ResponseWriter, r *http.Request) {
	if s.Buf == nil {
		http.Error(w, "not available", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"quality":  s.Buf.Quality(),
			"fps":      s.Buf.FPS(),
			"width":    s.Buf.Width(),
			"height":   s.Buf.Height(),
			"autocrop": s.Buf.Autocrop(),
		})
	case http.MethodPut:
		var body struct {
			Quality  *int  `json:"quality"`
			FPS      *int  `json:"fps"`
			Width    *int  `json:"width"`
			Height   *int  `json:"height"`
			Autocrop *bool `json:"autocrop"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body.Quality != nil {
			s.Buf.SetQuality(*body.Quality)
		}
		if body.Autocrop != nil {
			s.Buf.SetAutocrop(*body.Autocrop)
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
		json.NewEncoder(w).Encode(map[string]interface{}{
			"quality":  s.Buf.Quality(),
			"fps":      s.Buf.FPS(),
			"width":    s.Buf.Width(),
			"height":   s.Buf.Height(),
			"autocrop": s.Buf.Autocrop(),
		})
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

func (s *Server) handleHIDReset(w http.ResponseWriter, r *http.Request) {
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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

<label><input id="autocrop" type="checkbox" checked> Auto-crop black borders</label>

<label for="zoom">Zoom: <span id="zval" class="val">100%</span></label>
<input id="zoom" type="range" min="50" max="400" value="100">

<div class="info" id="devinfo">Loading device info&hellip;</div>

<script>
(function(){
  var slider = document.getElementById('quality');
  var valSpan = document.getElementById('qval');
  var fpsSelect = document.getElementById('fps');
  var resSelect = document.getElementById('res');
  var autocropCheck = document.getElementById('autocrop');
  var zoomSlider = document.getElementById('zoom');
  var zvalSpan = document.getElementById('zval');
  var info = document.getElementById('devinfo');
  var timer = null;

  function applyToUI(d) {
    if (d.quality !== undefined) { slider.value = d.quality; valSpan.textContent = d.quality; }
    if (d.fps !== undefined) { fpsSelect.value = d.fps; }
    if (d.width !== undefined && d.height !== undefined) { resSelect.value = d.width + 'x' + d.height; }
    if (d.autocrop !== undefined) { autocropCheck.checked = d.autocrop; }
    if (d.zoom !== undefined) { zoomSlider.value = d.zoom; zvalSpan.textContent = d.zoom + '%'; }
  }

  (function loadSettings() {
    var saved = {};
    try { saved = JSON.parse(localStorage.getItem('roadie-settings')) || {}; } catch(e) {}
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

  autocropCheck.onchange = function(){
    sendSettings({autocrop: autocropCheck.checked});
  };

  zoomSlider.oninput = function(){
    zvalSpan.textContent = zoomSlider.value + '%';
    saveSettings({zoom: parseInt(zoomSlider.value)});
  };

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

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
  }, 100);

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
  setInterval(function() {
    if (scrollDirty) {
      send({cmd:'mouse_scroll', amount:pendingScroll});
      pendingScroll = 0;
      scrollDirty = false;
    }
  }, 50);

  function remapToAbsolute(px, py) {
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

  function updateCrosshair(px, py, x, y) {
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

  var trackpadPressed = false;
  trackpad.addEventListener('mousedown', function(e) {
    e.preventDefault();
    trackpadPressed = true;
    if (inputMode === 'mouse') {
      var btn = e.button === 2 ? 2 : e.button === 1 ? 4 : 1;
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
      var btn = e.button === 2 ? 2 : e.button === 1 ? 4 : 1;
      send({cmd:'mouse_release', buttons:btn});
    } else {
      var p = posFromEvent(e);
      send({cmd:'touch', contacts:[{id:0, tip:false, x:p.x, y:p.y}]});
      // Lift — send empty
      setTimeout(function(){ send({cmd:'touch', contacts:[]}); }, 20);
    }
  });

  trackpad.addEventListener('contextmenu', function(e) { e.preventDefault(); });

  // --- Scroll wheel (mouse mode) ---
  trackpad.addEventListener('wheel', function(e) {
    e.preventDefault();
    var amount;
    if (e.deltaMode === 1) {
      amount = Math.round(e.deltaY);
    } else {
      amount = Math.round(e.deltaY / 25);
    }
    amount = Math.max(-127, Math.min(127, amount));
    if (amount !== 0) {
      if (inputMode === 'mouse') {
        pendingScroll = amount;
        scrollDirty = true;
      }
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
	if s.HID != nil {
		status = string(s.HID.Status())
	}
	json.NewEncoder(w).Encode(map[string]string{"status": status})
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
