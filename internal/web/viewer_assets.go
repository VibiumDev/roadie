package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/VibiumDev/roadie/internal/hid"
	"github.com/VibiumDev/roadie/internal/platform"
	"github.com/VibiumDev/station/viewer"

	_ "embed"
)

// The viewer is the Station viewer package's shell, so a station's page
// and this one are the same code with the same look; what a phone on a
// Roadie adds (its buttons, the capture settings, the crop, the resets)
// is the extras module below, which the shell loads here and which a
// station relays from here.

//go:embed view/extras.js
var extrasJS []byte

// handleViewerAsset serves the shell's files under /viewer/, and the
// extras with what they need to know about this host in front.
func (s *Server) handleViewerAsset(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/extras.js") {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		info, _ := json.Marshal(s.viewerInfo())
		fmt.Fprintf(w, "window.__roadieDevice=%s;\n", info)
		w.Write(extrasJS)
		return
	}
	http.StripPrefix("/viewer/", viewer.Assets()).ServeHTTP(w, r)
}

// viewerInfo is what the extras are told about this host.
func (s *Server) viewerInfo() map[string]any {
	info := map[string]any{}
	switch s.Platform {
	case platform.PlatformIOS, platform.PlatformAndroid, platform.PlatformMac, platform.PlatformWindows, platform.PlatformLinux:
		info["platform"] = string(s.Platform)
	}
	if s.HID != nil && s.HID.Backend() == hid.BackendLinuxGadget {
		info["hidBackend"] = "linux-gadget"
	}
	if d := s.usbDevices(); d != nil {
		info["usbDevices"] = d
	}
	return info
}

// handleWebRTCSignal is the viewer's signaling WebSocket.
func (s *Server) handleWebRTCSignal(w http.ResponseWriter, r *http.Request) {
	if s.RTC == nil {
		http.Error(w, "WebRTC is not available", http.StatusServiceUnavailable)
		return
	}
	s.RTC.ServeSignal(w, r)
}
