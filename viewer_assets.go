package main

import (
	"net/http"

	"github.com/VibiumDev/station/viewer"
)

// The viewer's WebRTC module, loaded by /view. It is the Station viewer
// package's copy, so a station's page and this one run the same code.
var viewerWebRTCJS = viewer.WebRTCJS

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
