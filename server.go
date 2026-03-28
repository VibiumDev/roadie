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
	Width          int
	Height         int
	FPS            int
	AudioBroadcast *AudioBroadcaster
	SourceType     string // "hardware" or "http"
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
</ul>
</body>
</html>`)
}

func (s *Server) handleView(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Roadie</title><link rel="icon" href="data:,"></head>
<body style="margin:0; background:#000; display:flex; justify-content:center; align-items:center; height:100vh;">
  <img id="feed" style="max-width:100%; max-height:100vh; display:none;">
  <div id="overlay" style="display:flex; position:fixed; inset:0; background:rgba(0,0,0,0.85); color:#fff; font-family:monospace; font-size:1.2em; justify-content:center; align-items:center; text-align:center; z-index:10;">
    Connecting&hellip;
  </div>
  <button id="unmute" style="position:fixed; bottom:20px; right:20px; z-index:20; background:rgba(0,0,0,0.6); border:1px solid rgba(255,255,255,0.2); border-radius:8px; padding:10px 14px; font-size:1.4em; cursor:pointer; display:none; line-height:1;" title="Toggle audio">
    &#x1F507;
  </button>
  <div id="qpanel" style="position:fixed; bottom:20px; left:20px; z-index:20; display:flex; align-items:center; gap:8px;">
    <button id="qbtn" style="background:rgba(0,0,0,0.6); border:1px solid rgba(255,255,255,0.2); border-radius:8px; padding:10px 14px; font-size:1.4em; cursor:pointer; line-height:1;" title="Quality">&#x2699;</button>
    <div id="qslider" style="display:none; background:rgba(0,0,0,0.6); border:1px solid rgba(255,255,255,0.2); border-radius:8px; padding:8px 12px; align-items:center; gap:8px;">
      <input id="qrange" type="range" min="30" max="95" style="width:120px; vertical-align:middle;">
      <span id="qval" style="color:#fff; font-family:monospace; font-size:0.9em; min-width:2em; text-align:right;"></span>
    </div>
  </div>
  <script>
  (function(){
    var img = document.getElementById('feed');
    var overlay = document.getElementById('overlay');
    var unmuteBtn = document.getElementById('unmute');
    var wasOk = false;

    function showOverlay(msg) {
      overlay.textContent = msg || 'Disconnected \u2014 waiting for capture device\u2026';
      overlay.style.display = 'flex';
      img.style.display = 'none';
      wasOk = false;
    }
    function hideOverlay() {
      overlay.style.display = 'none';
      if (!wasOk) {
        img.src = '/stream?' + Date.now();
        img.style.display = '';
        wasOk = true;
      }
    }

    img.onerror = function() { showOverlay(); };

    function poll() {
      fetch('/health').then(function(r){ return r.json(); }).then(function(data){
        if (data.status === 'ok') { hideOverlay(); }
        else if (data.status === 'no_signal') { showOverlay('No signal \u2014 check HDMI connection to capture device'); }
        else if (data.status === 'connecting') { showOverlay('Connecting\u2026'); }
        else { showOverlay(); }
        // Show unmute button when audio is available.
        if (data.audio) { unmuteBtn.style.display = ''; }
      }).catch(function(){ showOverlay(); });
    }
    poll();
    setInterval(poll, 3000);

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

    // --- Quality slider ---
    var qbtn = document.getElementById('qbtn');
    var qslider = document.getElementById('qslider');
    var qrange = document.getElementById('qrange');
    var qval = document.getElementById('qval');
    var qTimer = null;
    var qHideTimer = null;

    fetch('/api/settings').then(function(r){ return r.json(); }).then(function(d){
      qrange.value = d.quality;
      qval.textContent = d.quality;
    });

    qbtn.onclick = function() {
      var vis = qslider.style.display !== 'none';
      qslider.style.display = vis ? 'none' : 'flex';
      if (!vis) scheduleHide();
    };

    function scheduleHide() {
      clearTimeout(qHideTimer);
      qHideTimer = setTimeout(function(){ qslider.style.display = 'none'; }, 4000);
    }

    qrange.oninput = function() {
      qval.textContent = qrange.value;
      clearTimeout(qTimer);
      scheduleHide();
      qTimer = setTimeout(function(){
        fetch('/api/settings', {
          method: 'PUT',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({quality: parseInt(qrange.value)})
        });
      }, 300);
    };
  })();
  </script>
</body>
</html>`)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-cache")

	interval := time.Duration(float64(time.Second) / float64(s.FPS))

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

	interval := time.Duration(float64(time.Second) / float64(s.FPS))

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
		resp["resolution"] = fmt.Sprintf("%dx%d", s.Width, s.Height)
		resp["fps"] = s.FPS
	}
	cropRect := s.Source.CropRect()
	if cropRect != (image.Rectangle{}) && cropRect != image.Rect(0, 0, s.Width, s.Height) {
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
		})
	case http.MethodPut:
		var body struct {
			Quality int `json:"quality"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s.Buf.SetQuality(body.Quality)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"quality": s.Buf.Quality(),
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

<div class="info" id="devinfo">Loading device info&hellip;</div>

<script>
(function(){
  var slider = document.getElementById('quality');
  var valSpan = document.getElementById('qval');
  var info = document.getElementById('devinfo');
  var timer = null;

  fetch('/api/settings').then(function(r){ return r.json(); }).then(function(d){
    slider.value = d.quality;
    valSpan.textContent = d.quality;
  });

  slider.oninput = function(){
    valSpan.textContent = slider.value;
    clearTimeout(timer);
    timer = setTimeout(function(){
      fetch('/api/settings', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({quality: parseInt(slider.value)})
      });
    }, 300);
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
