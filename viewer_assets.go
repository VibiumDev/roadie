package main

import (
	_ "embed"
	"net/http"
)

// The viewer's WebRTC module, loaded by /view?transport=webrtc. It lives in
// its own file so the station can serve the same code later.
//
//go:embed viewer/webrtc.js
var viewerWebRTCJS []byte

func (s *Server) handleViewerJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(viewerWebRTCJS)
}

// handleWebRTCSignal is the viewer's signaling WebSocket.
func (s *Server) handleWebRTCSignal(w http.ResponseWriter, r *http.Request) {
	if s.RTC == nil {
		http.Error(w, "WebRTC is not available", http.StatusServiceUnavailable)
		return
	}
	s.RTC.ServeSignal(w, r)
}
