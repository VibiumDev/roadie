package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// In token mode every route but the sign-in's own is closed without the
// bearer: a browser is sent to sign in, an agent gets a 401, and nothing
// answers 2xx. With the bearer the quick routes answer.
func TestTokenModeGatesEveryRoute(t *testing.T) {
	a, _ := newTokenAuth(t)
	_, inner := newTestServer()
	h := a.Wrap(inner)
	routes := []string{
		"/", "/drive", "/view", "/grid", "/stream", "/snapshot", "/raw-stream", "/raw-snapshot", "/health", "/settings",
		"/api/settings", "/api/crop", "/audio", "/api/mic", "/test", "/soundcheck", "/gestures", "/api/capture/reset",
		"/api/hid/release", "/api/usb/reset", "/api/rotate", "/api/relay/reset", "/api/hid/type", "/api/hid/key",
		"/api/hid/mouse/move", "/api/hid/mouse/click", "/api/hid/mouse/scroll", "/api/hid/touch", "/api/hid/status",
		"/api/hid/ws", "/session", "/api/webrtc/signal", "/viewer/shell.js",
	}
	for _, path := range routes {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			// An agent: no Accept for HTML.
			r := httptest.NewRequest(method, path, strings.NewReader("{}"))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without the token (agent): %d", method, path, w.Code)
			}
			// A browser fetching a page is sent to sign in, with the
			// destination kept; a browser posting is simply refused.
			r = httptest.NewRequest(method, path, nil)
			r.Header.Set("Accept", "text/html,*/*")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if method == http.MethodGet && (w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/login?next=")) {
				t.Errorf("%s %s without the token (browser): %d %s", method, path, w.Code, w.Header().Get("Location"))
			}
			if method == http.MethodPost && w.Code != http.StatusUnauthorized && w.Code != http.StatusSeeOther {
				t.Errorf("%s %s without the token (browser): %d", method, path, w.Code)
			}
			// A wrong token is an agent without one.
			r = httptest.NewRequest(method, path, nil)
			r.Header.Set("Authorization", "Bearer roadie_local_wrong")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with a wrong token: %d", method, path, w.Code)
			}
		}
	}
	// The sign-in's own routes stay open.
	for _, path := range []string{"/login", "/favicon.png", "/apple-touch-icon.png"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code == http.StatusUnauthorized {
			t.Errorf("%s is closed", path)
		}
	}
	// With the token, the routes that answer at once do.
	for _, path := range []string{"/health", "/api/hid/status", "/snapshot", "/", "/drive", "/view", "/grid"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+a.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
			t.Errorf("%s with the token: %d", path, w.Code)
		}
	}
	// A token in the URL is not a token.
	r := httptest.NewRequest(http.MethodGet, "/health?token="+a.token, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a token in the query signed in: %d", w.Code)
	}
}
