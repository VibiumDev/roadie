package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// Server holds the state needed by HTTP handlers.
type Server struct {
	Source         FrameSource
	Device         string
	Width          int
	Height         int
	FPS            int
	Quality        int
	AudioBroadcast *AudioBroadcaster
}

// NewMux wires up all HTTP routes and returns a handler.
func NewMux(s *Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/view", s.handleView)
	mux.HandleFunc("/stream", s.handleStream)
	mux.HandleFunc("/snapshot", s.handleSnapshot)
	mux.HandleFunc("/health", s.handleHealth)
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
<li><a href="/stream">/stream</a> — raw MJPEG stream</li>
<li><a href="/snapshot">/snapshot</a> — grab a single frame (JPEG)</li>
<li><a href="/health">/health</a> — service status (JSON)</li>
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
	if status == "ok" || status == "no_signal" {
		resp["device"] = s.Device
		resp["resolution"] = fmt.Sprintf("%dx%d", s.Width, s.Height)
		resp["fps"] = s.FPS
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
