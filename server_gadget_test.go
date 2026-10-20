package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseHIDBackend(t *testing.T) {
	for in, want := range map[string]string{
		"": BackendRP2040, "rp2040": BackendRP2040, "RP2040": BackendRP2040,
		"linux-gadget": BackendLinuxGadget, " gadget ": BackendLinuxGadget,
	} {
		if got, err := ParseHIDBackend(in); err != nil || got != want {
			t.Errorf("ParseHIDBackend(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseHIDBackend("usbip"); err == nil {
		t.Error("unknown backend accepted")
	}
}

// The existing handlers drive the gadget unchanged: the frontend and the
// API do not know which backend is behind them.
func TestServerDrivesGadgetBackend(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	s, _ := newTestServer()
	s.HID = g
	mux := NewMux(s)

	post := func(path, body string) int {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return w.Code
	}
	if c := post("/api/hid/key", `{"keycode":4,"action":"click"}`); c != 200 {
		t.Fatalf("key click: %d", c)
	}
	if got := fg.host.got(fg.nodes["keyboard"]); len(got) != 2 || got[0][2] != 4 || got[1][2] != 0 {
		t.Errorf("keyboard saw % x", got)
	}
	if c := post("/api/hid/touch", `{"contacts":[{"id":0,"tip":true,"x":100,"y":200}]}`); c != 200 {
		t.Fatalf("touch: %d", c)
	}
	if c := post("/api/hid/release", ``); c != 200 {
		t.Fatalf("release: %d", c)
	}
	if r := fg.host.last(fg.nodes["touch"]); r[1] != 0 {
		t.Errorf("finger still down after /api/hid/release: % x", r)
	}
	if c := post("/api/hid/type", `{"text":"naïve"}`); c != 500 {
		t.Errorf("untypeable text: status %d, want 500", c)
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/hid/status", nil))
	var st map[string]string
	json.Unmarshal(w.Body.Bytes(), &st)
	want := map[string]string{"status": "connected", "backend": "linux-gadget",
		"usb_state": "configured", "port": fakeUDC, "serial": "10000000ff2137f1"}
	for k, v := range want {
		if st[k] != v {
			t.Errorf("/api/hid/status %s = %q, want %q (all: %v)", k, st[k], v, st)
		}
	}
	if _, ok := st["board"]; ok {
		t.Error("board reported not answering with the phone attached")
	}

	fg.setState("not attached")
	g.refresh()
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if !bytes.Contains(w.Body.Bytes(), []byte(`"hid_board": "not_answering"`)) {
		t.Errorf("/health does not say the phone is gone: %s", w.Body)
	}
}

func TestHIDStatusNamesRP2040Backend(t *testing.T) {
	s, mux := newTestServer()
	s.HID = newTestHID(&mockPort{})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/hid/status", nil))
	var st map[string]string
	json.Unmarshal(w.Body.Bytes(), &st)
	if st["backend"] != "rp2040" || st["status"] != "connected" {
		t.Errorf("/api/hid/status = %v", st)
	}
	if _, ok := st["usb_state"]; ok {
		t.Error("usb_state reported for the relay backend")
	}
}
