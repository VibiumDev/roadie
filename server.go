package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Server holds the state needed by HTTP handlers.
type Server struct {
	Source     FrameSource
	Device    string
	Width     int
	Height    int
	FPS       int
	Quality   int
}

// NewMux wires up all HTTP routes and returns a handler.
func NewMux(s *Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/view", s.handleView)
	mux.HandleFunc("/stream", s.handleStream)
	mux.HandleFunc("/snapshot", s.handleSnapshot)
	mux.HandleFunc("/health", s.handleHealth)
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
<head><title>Roadie</title></head>
<body style="margin:0; background:#000; display:flex; justify-content:center; align-items:center; height:100vh;">
  <img id="feed" style="max-width:100%; max-height:100vh; display:none;">
  <div id="overlay" style="display:flex; position:fixed; inset:0; background:rgba(0,0,0,0.85); color:#fff; font-family:monospace; font-size:1.2em; justify-content:center; align-items:center; text-align:center; z-index:10;">
    Connecting&hellip;
  </div>
  <script>
  (function(){
    var img = document.getElementById('feed');
    var overlay = document.getElementById('overlay');
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
      }).catch(function(){ showOverlay(); });
    }
    poll();
    setInterval(poll, 3000);
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
	json.NewEncoder(w).Encode(resp)
}
