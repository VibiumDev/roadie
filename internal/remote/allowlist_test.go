package remote

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/VibiumDev/station"
)

// What a station may reach on this Roadie, by scope: input for anything
// that moves the device, admin for anything that changes it, bidi for
// the agent's session, view for the rest; and the local-only routes
// are not reachable through the station at all.
func TestRigAllowlistScopes(t *testing.T) {
	want := map[string]station.Scope{
		"/api/webrtc/signal": station.ScopeView, "/health": station.ScopeView, "/viewer/extras.js": station.ScopeView,
		"/stream": station.ScopeView, "/snapshot": station.ScopeView, "/audio": station.ScopeView, "/api/hid/status": station.ScopeView,
		"/api/hid/ws": station.ScopeInput, "/api/mic": station.ScopeInput, "/api/rotate": station.ScopeInput,
		"/api/hid/type": station.ScopeInput, "/api/hid/key": station.ScopeInput, "/api/hid/mouse/move": station.ScopeInput,
		"/api/hid/mouse/click": station.ScopeInput, "/api/hid/mouse/scroll": station.ScopeInput, "/api/hid/touch": station.ScopeInput,
		"/api/hid/release": station.ScopeInput,
		"/session":         station.ScopeBidi,
		"/api/settings":    station.ScopeAdmin, "/api/crop": station.ScopeAdmin, "/api/capture/reset": station.ScopeAdmin,
		"/api/usb/reset": station.ScopeAdmin, "/api/relay/reset": station.ScopeAdmin,
	}
	for path, scope := range want {
		methods := []string{http.MethodGet, http.MethodPost}
		if scope == station.ScopeView {
			methods = methods[:1] // the views are reads; a POST to one is something else
		}
		for _, method := range methods {
			rule, ok := rigAllowlist.Match(httptest.NewRequest(method, path, nil))
			if !ok {
				t.Errorf("%s %s is not on the allowlist", method, path)
				continue
			}
			if rule.Scope != scope {
				t.Errorf("%s %s needs %s, want %s", method, path, rule.Scope, scope)
			}
		}
	}
	// Status reads need no more than view, and a POST to it is not a read.
	if rule, ok := rigAllowlist.Match(httptest.NewRequest(http.MethodPost, "/api/hid/status", nil)); ok && rule.Scope == station.ScopeView {
		t.Error("POST /api/hid/status passes as a view")
	}
	// Local-only: pages, raw frames, the test tools and the sign-in never
	// come through the station.
	for _, path := range []string{"/", "/drive", "/view", "/grid", "/settings", "/raw-stream", "/raw-snapshot", "/test", "/soundcheck", "/gestures", "/login", "/logout", "/viewer/shell.js", "/api/webrtc", "/api"} {
		if rule, ok := rigAllowlist.Match(httptest.NewRequest(http.MethodGet, path, nil)); ok {
			t.Errorf("%s is reachable through the station (rule %+v)", path, rule)
		}
	}
	// Nothing on the list is reachable at view that changes the device.
	for _, rule := range rigAllowlist {
		if rule.Scope == station.ScopeView && (rule.Path == "/api/hid/" || rule.Path == "/api/mic" || rule.Path == "/api/settings" || rule.Path == "/api/rotate") {
			t.Errorf("%s is a view", rule.Path)
		}
	}
}
