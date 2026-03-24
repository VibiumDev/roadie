package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeSource struct {
	frame  []byte
	status CaptureStatus
}

func (f *fakeSource) Latest() []byte       { return f.frame }
func (f *fakeSource) Status() CaptureStatus { return f.status }

func newTestServer() (*Server, http.Handler) {
	s := &Server{
		Source:  &fakeSource{frame: testJPEG(320, 240), status: StatusConnected},
		Device:  "Test Device",
		Width:   1920,
		Height:  1080,
		FPS:     30,
		Quality: 80,
	}
	return s, NewMux(s)
}

// get is a test helper that performs a GET and returns status, headers, and body.
func get(t *testing.T, mux http.Handler, path string) (int, http.Header, string) {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body)
}

func TestIndexHandler(t *testing.T) {
	_, mux := newTestServer()
	code, hdr, body := get(t, mux, "/")

	if code != 200 {
		t.Errorf("expected 200, got %d", code)
	}
	if !strings.Contains(hdr.Get("Content-Type"), "text/html") {
		t.Errorf("expected text/html, got %s", hdr.Get("Content-Type"))
	}
	for _, link := range []string{"/view", "/stream", "/snapshot", "/health"} {
		if !strings.Contains(body, link) {
			t.Errorf("index page missing link to %s", link)
		}
	}
}

func TestViewHandler(t *testing.T) {
	_, mux := newTestServer()
	_, _, body := get(t, mux, "/view")

	for _, want := range []string{`id="feed"`, `/health`, `overlay`} {
		if !strings.Contains(body, want) {
			t.Errorf("view page missing %q", want)
		}
	}
}

func TestSnapshotHandler(t *testing.T) {
	_, mux := newTestServer()
	code, hdr, body := get(t, mux, "/snapshot")

	if code != 200 {
		t.Errorf("expected 200, got %d", code)
	}
	if ct := hdr.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("expected image/jpeg, got %s", ct)
	}
	if len(body) < 2 || body[0] != 0xFF || body[1] != 0xD8 {
		t.Error("snapshot is not a valid JPEG")
	}
}

func TestSnapshotNoFrame(t *testing.T) {
	s := &Server{Source: &fakeSource{status: StatusConnected}, FPS: 30}
	code, _, _ := get(t, NewMux(s), "/snapshot")
	if code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", code)
	}
}

func TestStreamHandler(t *testing.T) {
	_, mux := newTestServer()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/stream", nil).WithContext(ctx))
		close(done)
	}()
	<-done

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(resp.Header.Get("Content-Type"), "multipart/x-mixed-replace") {
		t.Errorf("expected multipart/x-mixed-replace, got %s", resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), "--frame") {
		t.Error("stream response missing MJPEG boundary")
	}
}

func TestHealthStatus(t *testing.T) {
	tests := []struct {
		name       string
		status     CaptureStatus
		wantStatus string
		wantDevice bool
	}{
		{"connected", StatusConnected, "ok", true},
		{"disconnected", StatusDisconnected, "disconnected", false},
		{"connecting", StatusConnecting, "connecting", false},
		{"no_signal", StatusNoSignal, "no_signal", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{
				Source: &fakeSource{status: tt.status},
				Device: "Test Device", Width: 1920, Height: 1080, FPS: 30,
			}
			_, _, body := get(t, NewMux(s), "/health")

			var data map[string]interface{}
			if err := json.Unmarshal([]byte(body), &data); err != nil {
				t.Fatalf("bad JSON: %v", err)
			}
			if data["status"] != tt.wantStatus {
				t.Errorf("expected status %q, got %v", tt.wantStatus, data["status"])
			}
			_, hasDevice := data["device"]
			if hasDevice != tt.wantDevice {
				t.Errorf("device field present=%v, want %v", hasDevice, tt.wantDevice)
			}
		})
	}
}

func TestNotFound(t *testing.T) {
	_, mux := newTestServer()
	code, _, _ := get(t, mux, "/nonexistent")
	if code != 404 {
		t.Errorf("expected 404, got %d", code)
	}
}
