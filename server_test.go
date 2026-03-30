package main

import (
	"context"
	"encoding/json"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeSource struct {
	frame    []byte
	rawFrame []byte
	cropRect image.Rectangle
	status   CaptureStatus
}

func (f *fakeSource) Latest() []byte            { return f.frame }
func (f *fakeSource) LatestRaw() []byte          { return f.rawFrame }
func (f *fakeSource) Status() CaptureStatus      { return f.status }
func (f *fakeSource) CropRect() image.Rectangle  { return f.cropRect }

func newTestServer() (*Server, http.Handler) {
	buf := &FrameBuffer{}
	buf.SetQuality(80)
	buf.Update(testJPEG(320, 240))
	buf.SetStatus(StatusConnected)
	buf.SetFPS(30)
	buf.SetWidth(1920)
	buf.SetHeight(1080)
	s := &Server{
		Source: &fakeSource{
			frame:    testJPEG(320, 240),
			rawFrame: testJPEG(1920, 1080),
			status:   StatusConnected,
		},
		Buf:        buf,
		Device:     "Test Device",
		SourceType: "hardware",
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
	for _, link := range []string{"/view", "/stream", "/snapshot", "/raw-stream", "/raw-snapshot", "/health", "/settings"} {
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
	buf := &FrameBuffer{}
	buf.SetFPS(30)
	s := &Server{Source: &fakeSource{status: StatusConnected}, Buf: buf}
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
			buf := &FrameBuffer{}
			buf.SetFPS(30)
			buf.SetWidth(1920)
			buf.SetHeight(1080)
			s := &Server{
				Source: &fakeSource{status: tt.status},
				Buf:    buf,
				Device: "Test Device",
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

func TestRawSnapshotHandler(t *testing.T) {
	_, mux := newTestServer()
	code, hdr, body := get(t, mux, "/raw-snapshot")

	if code != 200 {
		t.Errorf("expected 200, got %d", code)
	}
	if ct := hdr.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("expected image/jpeg, got %s", ct)
	}
	if len(body) < 2 || body[0] != 0xFF || body[1] != 0xD8 {
		t.Error("raw snapshot is not a valid JPEG")
	}
}

func TestRawStreamHandler(t *testing.T) {
	_, mux := newTestServer()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/raw-stream", nil).WithContext(ctx))
		close(done)
	}()
	<-done

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(resp.Header.Get("Content-Type"), "multipart/x-mixed-replace") {
		t.Errorf("expected multipart/x-mixed-replace, got %s", resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), "--frame") {
		t.Error("raw stream response missing MJPEG boundary")
	}
}

func TestHealthCropRect(t *testing.T) {
	buf := &FrameBuffer{}
	buf.SetFPS(30)
	buf.SetWidth(1920)
	buf.SetHeight(1080)
	s := &Server{
		Source: &fakeSource{
			status:   StatusConnected,
			cropRect: image.Rect(520, 0, 1400, 1080),
		},
		Buf:    buf,
		Device: "Test Device",
	}
	_, _, body := get(t, NewMux(s), "/health")

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	crop, ok := data["crop"].(map[string]interface{})
	if !ok {
		t.Fatal("expected crop field in health response")
	}
	if crop["x"] != float64(520) || crop["width"] != float64(880) {
		t.Errorf("unexpected crop values: %v", crop)
	}
}

func TestHealthSourceType(t *testing.T) {
	tests := []struct {
		name       string
		sourceType string
		want       string
	}{
		{"hardware", "hardware", "hardware"},
		{"http", "http", "http"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &FrameBuffer{}
			buf.SetFPS(30)
			buf.SetWidth(1920)
			buf.SetHeight(1080)
			s := &Server{
				Source:     &fakeSource{status: StatusConnected},
				Buf:        buf,
				Device:     "Test Device",
				SourceType: tt.sourceType,
			}
			_, _, body := get(t, NewMux(s), "/health")

			var data map[string]interface{}
			if err := json.Unmarshal([]byte(body), &data); err != nil {
				t.Fatalf("bad JSON: %v", err)
			}
			if data["source_type"] != tt.want {
				t.Errorf("source_type = %v, want %q", data["source_type"], tt.want)
			}
		})
	}
}

func TestAPISettingsGet(t *testing.T) {
	_, mux := newTestServer()
	code, _, body := get(t, mux, "/api/settings")
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if data["quality"] != float64(80) {
		t.Errorf("quality = %v, want 80", data["quality"])
	}
}

func TestAPISettingsPut(t *testing.T) {
	s, mux := newTestServer()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"quality":50}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if data["quality"] != float64(50) {
		t.Errorf("response quality = %v, want 50", data["quality"])
	}
	if got := s.Buf.Quality(); got != 50 {
		t.Errorf("buf quality = %d, want 50", got)
	}
}

func TestSettingsPage(t *testing.T) {
	_, mux := newTestServer()
	code, hdr, body := get(t, mux, "/settings")
	if code != 200 {
		t.Errorf("expected 200, got %d", code)
	}
	if !strings.Contains(hdr.Get("Content-Type"), "text/html") {
		t.Errorf("expected text/html, got %s", hdr.Get("Content-Type"))
	}
	for _, want := range []string{"Settings", "/api/settings", `type="range"`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %q", want)
		}
	}
}

func TestNotFound(t *testing.T) {
	_, mux := newTestServer()
	code, _, _ := get(t, mux, "/nonexistent")
	if code != 404 {
		t.Errorf("expected 404, got %d", code)
	}
}
