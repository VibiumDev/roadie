package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/VibiumDev/roadie/rtc"
	"html/template"
	"image"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/VibiumDev/roadie/audio/pcm"
	"github.com/VibiumDev/station/rig"
	"github.com/VibiumDev/station/viewer"
)

// Server holds the state needed by HTTP handlers.
type Server struct {
	Source         FrameSource
	Buf            *FrameBuffer
	RelayFPS       int // frame rate cap for the MJPEG stream through a station
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
	// Station is the rig side, while enrolled with one: see station.go.
	Station *rigRunner
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
	mux.HandleFunc("/viewer/", s.handleViewerAsset)
	mux.HandleFunc("/favicon.png", serveIcon)
	mux.HandleFunc("/apple-touch-icon.png", serveIcon)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	host, _ := os.Hostname()
	data := map[string]any{"Title": s.pageTitle(), "Name": s.Name, "Host": host, "Platform": "", "Input": s.effectiveInputMode(), "Stations": []map[string]any(nil)}
	if s.Platform != "" {
		data["Platform"] = string(s.Platform)
	}
	if s.Station != nil {
		if st, ok := s.Station.status()["stations"].([]map[string]any); ok {
			data["Stations"] = st
		}
	}
	pages.ExecuteTemplate(w, "index", data)
}

// The pages, in the look of Vibium's web style guide; the stylesheet and
// the theme switch (and the "?" help toggles) are the Station viewer
// package's, served under /viewer/, so a station's pages and these match.
var pages = template.Must(template.New("").Parse(`{{define "head"}}<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title><link rel="icon" type="image/png" href="/favicon.png"><link rel="apple-touch-icon" href="/apple-touch-icon.png">
<link rel="stylesheet" href="/viewer/pages.css"><script src="/viewer/pages.js"></script></head>
<body>
{{end}}
{{define "header"}}<header class="top"><div class="wrap"><a class="brand" href="/"><span class="wordmark">Vibium</span><span class="product">Roadie</span></a>
<nav class="pages"><a href="/view">View</a><a href="/settings">Settings</a><a href="/test">Test</a></nav><button id="themeBtn" class="iconbtn" type="button" aria-label="Theme"></button><span class="break"></span></div></header>
{{end}}
{{define "foot"}}</body></html>{{end}}
{{define "help"}}<button class="qhelp" type="button" aria-expanded="false" aria-controls="help-{{.}}" aria-label="What this is">?</button>{{end}}

{{define "index"}}{{template "head" .}}{{template "header" .}}<main class="wrap"><p class="lead">The target wired to this machine, live in the browser.</p>
<ul class="links">
<li><a href="/view">/view</a> <span>watch and drive the target: picture, sound, touch and keyboard</span></li>
<li><a href="/stream">/stream</a> <span>the MJPEG stream, cropped to the target's screen</span></li>
<li><a href="/snapshot">/snapshot</a> <span>one frame as a JPEG, cropped the same way</span></li>
<li><a href="/raw-stream">/raw-stream</a> <span>the capture device's whole frame, uncropped</span></li>
<li><a href="/raw-snapshot">/raw-snapshot</a> <span>one uncropped frame</span></li>
<li><a href="/health">/health</a> <span>service status as JSON</span></li>
<li><a href="/settings">/settings</a> <span>capture quality, the crop, the device's details</span></li>
<li><a href="/test">/test</a> <span>try the mouse and keyboard over USB</span></li>
</ul>
<h2 class="h2">This host</h2>
<div class="tablewrap"><table class="kv">
<tr><th>Host</th><td>{{.Host}}{{if and .Name (ne .Name "roadie")}} <span class="muted">{{.Name}}</span>{{end}}</td></tr>
<tr><th>Platform</th><td>{{if .Platform}}{{.Platform}}{{else}}<span class="muted">not set (--platform)</span>{{end}}</td></tr>
<tr><th>Pointer</th><td>{{if .Input}}{{.Input}}{{else}}<span class="muted">the viewer decides</span>{{end}}</td></tr>
<tr><th>Stations</th><td>{{if .Stations}}{{range .Stations}}<div>{{.name}} <span class="muted mono">{{.rig}}</span> {{if .online}}<span class="ok">online</span>{{else}}<span class="muted">offline</span>{{end}}</div>{{end}}{{else}}<span class="muted">none: <code>roadie enroll https://&lt;station&gt; --code …</code></span>{{end}}</td></tr>
</table></div>
</main>
{{template "foot" .}}{{end}}

{{define "settings"}}{{template "head" .}}{{template "header" .}}<main class="wrap">

<h2 class="h2">Picture {{template "help" "picture"}}</h2>
<div class="helptext" id="help-picture" hidden>
<p>What Roadie asks of the capture dongle and how it serves the MJPEG stream. Picture and Crop are the host's: saved there, used for everyone who views, and the same settings the viewer's menu changes. WebRTC uses the same capture with its own H.264 encoder, capped by the viewer's Bitrate slider.</p>
<p><b>Quality.</b> The JPEG quality of each MJPEG frame, 30 to 95. Higher is sharper and heavier on the link; 80 suits a LAN, lower helps a slow link. The viewer's Quality slider is this same setting.</p>
<p><b>Frames per second.</b> How many frames a second are asked of the dongle, for MJPEG and WebRTC alike. 30 is smooth; 10 or 15 spare the host and the link.</p>
<p><b>Capture size.</b> The frame size asked of the dongle. A phone's output is 1920x1080 whatever its screen, and the picture is cropped to the screen from that; a smaller size costs the host less and shows less.</p>
</div>
<div class="setting"><label for="quality">Quality <span class="val" id="qval"></span></label>
<input id="quality" type="range" min="30" max="95"></div>
<div class="setting"><label for="fps">Frames per second</label>
<select id="fps"><option value="10">10</option><option value="15">15</option><option value="20">20</option><option value="30">30</option></select></div>
<div class="setting"><label for="res">Capture size</label>
<select id="res"><option value="1920x1080">1080p</option><option value="1280x720">720p</option><option value="640x480">480p</option></select></div>

<h2 class="h2">Crop {{template "help" "crop"}}</h2>
<p class="helptext" id="help-crop" hidden>How the frame is cut down to the target's screen. Auto trims whatever is black at the edges, which a dark app can fool into shrinking the picture: show something bright to the screen's edges, such as a white page, and save the crop. Saved is then chosen and survives restarts; portrait and landscape each keep their own, and where there is none, Saved uses auto-crop. Off shows the whole frame.</p>
<div class="setting">
<label class="check"><input type="radio" name="crop" value="auto"> Auto: trim whatever is black at the edges</label>
<label class="check"><input type="radio" name="crop" value="off"> Off: show the whole frame</label>
<label class="check"><input type="radio" name="crop" value="saved"> Saved: the crop saved for the way the device is turned</label>
<div class="status" id="cropStatus">Loading&hellip;</div>
<p><button class="small" id="cropSave" type="button">Save current crop</button> <button class="small" id="cropClear" type="button">Clear saved crops</button></p></div>

<h2 class="h2">Viewer, in this browser {{template "help" "viewer"}}</h2>
<div class="helptext" id="help-viewer" hidden>
<p>Choices this browser keeps for /view, the same ones as the viewer's own menu, kept here and not on the host.</p>
<p><b>Zoom.</b> Fit scales the picture to the room it has, up or down, as the viewer's Fit does, and is the default on a phone or tablet. Otherwise the picture is shown at this share of its own size, 100% on a desktop.</p>
<p><b>Toolbar.</b> Show keeps the toolbar beside the picture. Auto-hide tucks it off its edge and leaves a grip; the grip, a tap at the edge or the mouse at the edge brings it back. Off hides it, and <code>?toolbar=show</code> in the address brings it back.</p>
</div>
<div class="setting"><span class="lbl">Zoom <span class="val" id="zval"></span></span>
<label class="check"><input type="checkbox" id="zoomFit"> Fit the room the picture has</label>
<input id="zoom" type="range" min="50" max="400" value="100" aria-label="Zoom"></div>
<div class="setting"><span class="lbl">Toolbar</span>
<label class="check"><input type="radio" name="toolbar" value="show"> Show</label>
<label class="check"><input type="radio" name="toolbar" value="auto"> Auto-hide: tucked off its edge, with a grip to bring it back</label>
<label class="check"><input type="radio" name="toolbar" value="off"> Off</label></div>

<h2 class="h2">This host {{template "help" "host"}}</h2>
<p class="helptext" id="help-host" hidden>What Roadie found on this machine: the capture device, how it captures (MJPEG or raw YUYV from the dongle), the size and rate it captures at, and whether a signal is coming in.</p>
<div class="tablewrap"><table class="kv" id="devinfo"><tr><td class="muted">Loading&hellip;</td></tr></table></div>
</main>
<script>
(function(){
  var slider = document.getElementById('quality');
  var valSpan = document.getElementById('qval');
  var fpsSelect = document.getElementById('fps');
  var resSelect = document.getElementById('res');
  var zoomSlider = document.getElementById('zoom');
  var zoomFit = document.getElementById('zoomFit');
  var zvalSpan = document.getElementById('zval');
  var info = document.getElementById('devinfo');
  var timer = null;

  function applyToUI(d) {
    if (d.quality !== undefined) { slider.value = d.quality; valSpan.textContent = d.quality; }
    if (d.fps !== undefined) { fpsSelect.value = d.fps; }
    if (d.width !== undefined && d.height !== undefined) { resSelect.value = d.width + 'x' + d.height; }
  }
  function showZoom(z) {
    var fit = z === 'fit';
    zoomFit.checked = fit;
    zoomSlider.disabled = fit;
    if (!fit && typeof z === 'number') zoomSlider.value = z;
    zvalSpan.textContent = fit ? 'fit' : zoomSlider.value + '%';
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
    showZoom(saved.zoom);
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

  // The viewer's zoom: fit, or a share of the picture's own size.
  zoomFit.onchange = function(){
    var z = zoomFit.checked ? 'fit' : parseInt(zoomSlider.value);
    showZoom(z);
    saveSettings({zoom: z});
  };
  zoomSlider.oninput = function(){
    showZoom(parseInt(zoomSlider.value));
    saveSettings({zoom: parseInt(zoomSlider.value)});
  };

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
    var lines = [];
    ['portrait', 'landscape'].forEach(function(o) {
      var c = d.saved && d.saved[o];
      lines.push(o + ': ' + (c
        ? c.width + '\u00d7' + c.height + ' at (' + c.x + ', ' + c.y + ')' + (d.mode === 'saved' && d.orientation === o ? ' \u2014 in use' : '')
        : 'none'));
    });
    var mode = {saved: 'the saved crop', auto: 'auto-crop', off: 'no crop'}[d.mode] || d.mode;
    lines.push('Device is ' + (d.orientation || 'turned an unknown way') + '; using ' + mode + '.');
    cropStatus.textContent = '';
    lines.forEach(function(l) { var div = document.createElement('div'); div.textContent = l; cropStatus.appendChild(div); });
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
    var rows = [['Device', d.device], ['Source', d.source_type], ['Resolution', d.resolution], ['FPS', d.fps], ['Status', d.status]];
    info.textContent = '';
    rows.forEach(function(row) {
      if (row[1] === undefined || row[1] === null || row[1] === '') return;
      var tr = document.createElement('tr'), th = document.createElement('th'), td = document.createElement('td');
      th.textContent = row[0]; td.textContent = String(row[1]);
      tr.appendChild(th); tr.appendChild(td); info.appendChild(tr);
    });
    if (!info.children.length) info.innerHTML = '<tr><td class="muted">No device info available</td></tr>';
  });
})();
</script>
{{template "foot" .}}{{end}}`))

func (s *Server) handleView(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	q := r.URL.Query()
	// Pointer mode: ?input= for this page load, otherwise the target's own
	// --input (or its platform's). Either beats the viewer's stored choice.
	mode := q.Get("input")
	if mode != "touch" && mode != "mouse" {
		mode = s.effectiveInputMode()
	}
	cfg := viewer.Config{Mode: "local", Name: s.pageTitle(), API: viewer.Local(), InputMode: mode,
		WebRTC: s.RTC != nil && s.RTC.Available(), Extras: "/viewer/extras.js", Info: s.viewerInfo()}
	// ?transport=webrtc or mjpeg sets the picture's transport for this
	// page load; the page remembers the menu's choice otherwise.
	if t := q.Get("transport"); cfg.WebRTC && (t == "webrtc" || t == "mjpeg") {
		cfg.Transport = t
	}
	// minimal=1 hides the toolbar so the page embeds cleanly in a frame
	// (see /grid); the controls stay in the DOM.
	viewer.Render(w, viewer.Page{Title: s.pageTitle(), Minimal: q.Get("minimal") == "1", Home: "/", HomeTitle: "Roadie", Config: cfg})
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

	// Through a station the stream is capped (--relay-fps), so a remote
	// viewer cannot eat the uplink; any viewer may ask for less with ?fps=.
	fps := s.Buf.FPS()
	if _, relayed := rig.GrantFrom(r.Context()); relayed && s.RelayFPS > 0 && s.RelayFPS < fps {
		fps = s.RelayFPS
	}
	if q, err := strconv.Atoi(r.URL.Query().Get("fps")); err == nil && q > 0 && q < fps {
		fps = q
	}
	interval := time.Duration(float64(time.Second) / float64(fps))

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
	if s.Station != nil {
		resp["station"] = s.Station.status()
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
	host, _ := os.Hostname()
	pages.ExecuteTemplate(w, "settings", map[string]any{"Title": "Settings · " + s.pageTitle(), "Name": s.Name, "Host": host})
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

	// Through a station the sound goes down to 16 kHz mono 16-bit (256
	// kbps), so a remote viewer cannot eat the uplink; ?format=s16-16k asks
	// for the same on the LAN.
	params := s.AudioBroadcast.Params()
	var down *pcm.Downsampler
	if _, relayed := rig.GrantFrom(r.Context()); relayed || r.URL.Query().Get("format") == "s16-16k" {
		down = pcm.NewDownsampler(params.SampleRate, 16000, params.Channels)
		params = AudioParams{SampleRate: 16000, Channels: 1, Format: "s16le"}
	}

	// Send audio params as first (text) message.
	paramsJSON, _ := json.Marshal(params)
	if err := conn.Write(ctx, websocket.MessageText, paramsJSON); err != nil {
		return
	}

	// Subscribe to audio chunks and forward as binary messages.
	ch, unsub := s.AudioBroadcast.Subscribe()
	defer unsub()

	var samples []int16
	var out []byte
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-ch:
			if !ok {
				return
			}
			if down != nil {
				samples = down.Process(samples[:0], pcm.Float32LE(chunk))
				if len(samples) == 0 {
					continue
				}
				out = pcm.Int16LE(out[:0], samples)
				chunk = out
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

// hidCommand runs one input command from a viewer, the JSON /api/hid/ws
// takes, whichever transport carried it: the WebSocket, or the "hid" data
// channel of a WebRTC session.
func (s *Server) hidCommand(data []byte) {
	if s.HID == nil {
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
		// Space "picture" says x and y (and the contacts) are fractions of
		// the picture a viewer sees, scaled to 0..32767, for a viewer that
		// knows nothing of the crop: the station's.
		Space string `json:"space,omitempty"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	if msg.Space == "picture" {
		msg.X, msg.Y = s.pictureToHID(msg.X, msg.Y)
		for i := range msg.Contacts {
			msg.Contacts[i].X, msg.Contacts[i].Y = s.pictureToHID(msg.Contacts[i].X, msg.Contacts[i].Y)
		}
	}
	// While Roadie turns the screen it clicks through AssistiveTouch's
	// menu, and a viewer's pointer moving under it would spoil that.
	// Viewers hold back once /health says so, a second later at most;
	// meanwhile only releases go through, so nothing stays held.
	if s.turn.rotating.Load() && !releasesOnly(msg.Cmd, msg.Contacts) {
		return
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
	case "tap":
		// A whole tap in one command, for a viewer on a link where the gap
		// between a touch and its lift would stretch: down at x, y, a
		// short hold, up.
		s.HID.Touch([]TouchContact{{ID: 0, Tip: true, X: msg.X, Y: msg.Y}})
		time.Sleep(60 * time.Millisecond)
		s.HID.Touch([]TouchContact{{ID: 0, Tip: false, X: msg.X, Y: msg.Y}})
		s.HID.Touch(nil)
	}
}

// pictureToHID turns a position given as a fraction of the picture a
// viewer sees (the crop, scaled to 0..32767) into the absolute units the
// target takes: the same arithmetic as the viewer page's remapToAbsolute,
// done here for viewers that do not know the crop.
func (s *Server) pictureToHID(x, y int) (int, int) {
	if s.Buf == nil || s.Source == nil {
		return x, y
	}
	clamp := func(f float64) float64 { return math.Max(0, math.Min(1, f)) }
	px, py := clamp(float64(x)/32767), clamp(float64(y)/32767)
	fullW, fullH := s.Buf.Width(), s.Buf.Height()
	crop := s.Source.CropRect()
	if s.Platform.PointerSpace() == "screen" {
		whole := crop == (image.Rectangle{}) || crop == image.Rect(0, 0, fullW, fullH)
		content := s.Source.ContentRect()
		if whole && content.Dx() > 0 && content.Dy() > 0 && fullW > 0 && fullH > 0 {
			fx := clamp((px*float64(fullW) - float64(content.Min.X)) / float64(content.Dx()))
			fy := clamp((py*float64(fullH) - float64(content.Min.Y)) / float64(content.Dy()))
			return int(math.Round(fx * 32767)), int(math.Round(fy * 32767))
		}
		return int(math.Round(px * 32767)), int(math.Round(py * 32767))
	}
	if crop.Dx() > 0 && crop.Dy() > 0 && fullW > 0 && fullH > 0 {
		ax := (float64(crop.Min.X) + px*float64(crop.Dx())) / float64(fullW)
		ay := (float64(crop.Min.Y) + py*float64(crop.Dy())) / float64(fullH)
		return int(math.Round(ax * 32767)), int(math.Round(ay * 32767))
	}
	return int(math.Round(px * 32767)), int(math.Round(py * 32767))
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
	lim := rig.LimiterFrom(ctx) // through a station: the hold's pace
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if lim != nil {
			if err := lim.Wait(ctx); err != nil {
				return
			}
		}
		s.hidCommand(data)
	}
}
