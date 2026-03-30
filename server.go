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
	mux.HandleFunc("/api/hid/type", s.handleHIDType)
	mux.HandleFunc("/api/hid/key", s.handleHIDKey)
	mux.HandleFunc("/api/hid/mouse/move", s.handleHIDMouseMove)
	mux.HandleFunc("/api/hid/mouse/click", s.handleHIDMouseClick)
	mux.HandleFunc("/api/hid/mouse/scroll", s.handleHIDMouseScroll)
	mux.HandleFunc("/api/hid/touch", s.handleHIDTouch)
	mux.HandleFunc("/api/hid/status", s.handleHIDStatus)
	mux.HandleFunc("/api/hid/ws", s.handleHIDWebSocket)
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
<head><title>Roadie</title><link rel="icon" href="data:,"></head>
<body style="margin:0; background:#000; display:flex; height:100vh;">
  <div id="overlay" style="display:flex; position:fixed; inset:0; background:rgba(0,0,0,0.85); color:#fff; font-family:monospace; font-size:1.2em; justify-content:center; align-items:center; text-align:center; z-index:100;">
    Connecting&hellip;
  </div>
  <div id="viewer" style="position:relative; flex-shrink:0;">
    <img id="feed" draggable="false" oncontextmenu="return false" style="max-width:100%; max-height:100vh; display:none; touch-action:none; cursor:crosshair;">
  </div>
  <div id="toolbar" style="position:relative; display:flex; flex-direction:column; gap:4px; padding:8px 6px;">
    <button id="qbtn" style="width:36px; height:36px; background:rgba(50,50,50,0.9); border:1px solid rgba(255,255,255,0.15); border-radius:6px; font-size:1.1em; cursor:pointer; line-height:1; padding:0; display:flex; align-items:center; justify-content:center;" title="Settings">&#x2699;&#xFE0F;</button>
    <button id="unmute" style="width:36px; height:36px; background:rgba(50,50,50,0.9); border:1px solid rgba(255,255,255,0.15); border-radius:6px; font-size:1.1em; cursor:pointer; line-height:1; padding:0; display:none; align-items:center; justify-content:center;" title="Toggle audio">&#x1F507;</button>
    <div id="qslider" style="display:none; position:absolute; top:8px; right:calc(100% - 2px); z-index:21; background:rgba(50,50,50,0.95); border:1px solid rgba(255,255,255,0.15); border-radius:8px; padding:10px 14px; color:#fff; font-family:monospace; font-size:0.85em; white-space:nowrap;">
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

    img.onload = function() { tryHideOverlay(); };
    img.onerror = function() { showOverlay(); };

    function poll() {
      fetch('/health').then(function(r){ return r.json(); }).then(function(data){
        if (data.status === 'ok') {
          healthOk = true;
          startStream();
          tryHideOverlay();
        } else {
          healthOk = false;
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

    unmuteBtn.onclick = function() {
      muted = !muted;
      if (muted) {
        unmuteBtn.innerHTML = '&#x1F507;';
        stopAudio();
      } else {
        unmuteBtn.innerHTML = '&#x1F50A;';
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
    var settingsTimer = null;
    var qHideTimer = null;

    fetch('/api/settings').then(function(r){ return r.json(); }).then(function(d){
      qrange.value = d.quality;
      qval.textContent = d.quality;
      fpsSelect.value = d.fps;
      resSelect.value = d.width + 'x' + d.height;
    });

    qbtn.onclick = function() {
      var vis = qslider.style.display !== 'none';
      qslider.style.display = vis ? 'none' : 'block';
      if (!vis) scheduleHide();
    };

    function scheduleHide() {
      clearTimeout(qHideTimer);
      qHideTimer = setTimeout(function(){ qslider.style.display = 'none'; }, 4000);
    }

    function sendSettings(obj) {
      clearTimeout(settingsTimer);
      scheduleHide();
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

    var toolbar = document.getElementById('toolbar');

    document.addEventListener('keydown', function(e) {
      if (toolbar.contains(e.target)) return;
      var hid = KEY_MAP[e.code];
      if (hid !== undefined) {
        e.preventDefault();
        hidSend({cmd:'key_press', keycode:hid});
      }
    });

    document.addEventListener('keyup', function(e) {
      if (toolbar.contains(e.target)) return;
      var hid = KEY_MAP[e.code];
      if (hid !== undefined) {
        e.preventDefault();
        hidSend({cmd:'key_release', keycode:hid});
      }
    });
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

	for {
		select {
		case <-r.Context().Done():
			return
		default:
			frame := s.Source.Latest()
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
	json.NewEncoder(w).Encode(resp)
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
			"quality": s.Buf.Quality(),
			"fps":     s.Buf.FPS(),
			"width":   s.Buf.Width(),
			"height":  s.Buf.Height(),
		})
	case http.MethodPut:
		var body struct {
			Quality *int `json:"quality"`
			FPS     *int `json:"fps"`
			Width   *int `json:"width"`
			Height  *int `json:"height"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body.Quality != nil {
			s.Buf.SetQuality(*body.Quality)
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
			"quality": s.Buf.Quality(),
			"fps":     s.Buf.FPS(),
			"width":   s.Buf.Width(),
			"height":  s.Buf.Height(),
		})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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

<div class="info" id="devinfo">Loading device info&hellip;</div>

<script>
(function(){
  var slider = document.getElementById('quality');
  var valSpan = document.getElementById('qval');
  var fpsSelect = document.getElementById('fps');
  var resSelect = document.getElementById('res');
  var info = document.getElementById('devinfo');
  var timer = null;

  fetch('/api/settings').then(function(r){ return r.json(); }).then(function(d){
    slider.value = d.quality;
    valSpan.textContent = d.quality;
    fpsSelect.value = d.fps;
    resSelect.value = d.width + 'x' + d.height;
  });

  function sendSettings(obj) {
    clearTimeout(timer);
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
