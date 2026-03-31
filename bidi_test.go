package main

import (
	"context"
	"encoding/json"
	"image"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// bidiDial connects a WebSocket client to /session on the test server.
func bidiDial(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, ts.URL+"/session", nil)
	if err != nil {
		t.Fatalf("dial /session: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

// bidiSend sends a BiDi command and reads the response.
func bidiSend(t *testing.T, conn *websocket.Conn, msg any) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, resp, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return resp
}

// bidiResp is a generic BiDi response envelope.
type bidiResp struct {
	Type    string          `json:"type"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
	Message string          `json:"message,omitempty"`
}

func parseBidiResp(t *testing.T, data json.RawMessage) bidiResp {
	t.Helper()
	var r bidiResp
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	return r
}

// resetBidiState cleans up global BiDi state between tests.
func resetBidiState() {
	bidiSessionMu.Lock()
	bidiActive = nil
	bidiSessionMu.Unlock()
	touchSourceMu.Lock()
	touchSourceMap = map[string]int{}
	touchSourceCount = 0
	touchSourceMu.Unlock()
}

func TestBiDiSessionNew(t *testing.T) {
	resetBidiState()
	_, mux := newTestServer()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := bidiDial(t, ts)
	resp := parseBidiResp(t, bidiSend(t, conn, map[string]any{
		"id": 1, "method": "session.new", "params": map[string]any{"capabilities": map[string]any{}},
	}))

	if resp.Type != "success" {
		t.Fatalf("expected success, got %s: %s", resp.Type, resp.Message)
	}
	if resp.ID != 1 {
		t.Errorf("expected id 1, got %d", resp.ID)
	}

	var result struct {
		SessionID    string         `json:"sessionId"`
		Capabilities map[string]any `json:"capabilities"`
	}
	json.Unmarshal(resp.Result, &result)

	if result.SessionID == "" {
		t.Error("expected non-empty sessionId")
	}
	if result.Capabilities["browserName"] != "roadie" {
		t.Errorf("expected browserName=roadie, got %v", result.Capabilities["browserName"])
	}
}

func TestBiDiSessionNewRejectsSecond(t *testing.T) {
	resetBidiState()
	_, mux := newTestServer()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := bidiDial(t, ts)

	// First session succeeds.
	resp := parseBidiResp(t, bidiSend(t, conn, map[string]any{
		"id": 1, "method": "session.new", "params": map[string]any{},
	}))
	if resp.Type != "success" {
		t.Fatalf("first session.new failed: %s", resp.Message)
	}

	// Second session on same connection should fail.
	resp = parseBidiResp(t, bidiSend(t, conn, map[string]any{
		"id": 2, "method": "session.new", "params": map[string]any{},
	}))
	if resp.Type != "error" {
		t.Fatalf("expected error for second session.new, got %s", resp.Type)
	}
	if resp.Error != "session not created" {
		t.Errorf("expected 'session not created' error, got %q", resp.Error)
	}
}

func TestBiDiSessionStatus(t *testing.T) {
	resetBidiState()
	_, mux := newTestServer()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := bidiDial(t, ts)
	resp := parseBidiResp(t, bidiSend(t, conn, map[string]any{
		"id": 1, "method": "session.status", "params": map[string]any{},
	}))

	if resp.Type != "success" {
		t.Fatalf("expected success, got %s", resp.Type)
	}

	var result struct {
		Ready   bool   `json:"ready"`
		Message string `json:"message"`
	}
	json.Unmarshal(resp.Result, &result)

	// No HID controller in test server, so not fully ready.
	if result.Ready {
		t.Error("expected ready=false (no HID in test server)")
	}
}

func TestBiDiSessionEnd(t *testing.T) {
	resetBidiState()
	_, mux := newTestServer()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := bidiDial(t, ts)

	// Create session.
	bidiSend(t, conn, map[string]any{
		"id": 1, "method": "session.new", "params": map[string]any{},
	})

	// End it.
	resp := parseBidiResp(t, bidiSend(t, conn, map[string]any{
		"id": 2, "method": "session.end", "params": map[string]any{},
	}))
	if resp.Type != "success" {
		t.Fatalf("expected success, got %s: %s", resp.Type, resp.Message)
	}

	// Should be able to create a new session now.
	resp = parseBidiResp(t, bidiSend(t, conn, map[string]any{
		"id": 3, "method": "session.new", "params": map[string]any{},
	}))
	if resp.Type != "success" {
		t.Fatalf("expected success after session.end, got %s: %s", resp.Type, resp.Message)
	}
}

func TestBiDiGetTree(t *testing.T) {
	resetBidiState()
	_, mux := newTestServer()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := bidiDial(t, ts)
	resp := parseBidiResp(t, bidiSend(t, conn, map[string]any{
		"id": 1, "method": "browsingContext.getTree", "params": map[string]any{},
	}))

	if resp.Type != "success" {
		t.Fatalf("expected success, got %s", resp.Type)
	}

	var result struct {
		Contexts []struct {
			Context string `json:"context"`
		} `json:"contexts"`
	}
	json.Unmarshal(resp.Result, &result)

	if len(result.Contexts) != 1 {
		t.Fatalf("expected 1 context, got %d", len(result.Contexts))
	}
	if result.Contexts[0].Context != "screen" {
		t.Errorf("expected context='screen', got %q", result.Contexts[0].Context)
	}
}

func TestBiDiCaptureScreenshot(t *testing.T) {
	resetBidiState()
	_, mux := newTestServer()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := bidiDial(t, ts)
	resp := parseBidiResp(t, bidiSend(t, conn, map[string]any{
		"id": 1, "method": "browsingContext.captureScreenshot", "params": map[string]any{"context": "screen"},
	}))

	if resp.Type != "success" {
		t.Fatalf("expected success, got %s: %s", resp.Type, resp.Message)
	}

	var result struct {
		Data string `json:"data"`
	}
	json.Unmarshal(resp.Result, &result)

	if result.Data == "" {
		t.Error("expected non-empty base64 data")
	}
}

func TestBiDiGetViewport(t *testing.T) {
	resetBidiState()
	_, mux := newTestServer()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := bidiDial(t, ts)
	resp := parseBidiResp(t, bidiSend(t, conn, map[string]any{
		"id": 1, "method": "roadie:screen.getViewport", "params": map[string]any{},
	}))

	if resp.Type != "success" {
		t.Fatalf("expected success, got %s: %s", resp.Type, resp.Message)
	}

	var result struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	json.Unmarshal(resp.Result, &result)

	if result.Width != 1920 {
		t.Errorf("expected width 1920, got %d", result.Width)
	}
	if result.Height != 1080 {
		t.Errorf("expected height 1080, got %d", result.Height)
	}
}

func TestBiDiGetViewportWithCrop(t *testing.T) {
	resetBidiState()

	buf := &FrameBuffer{}
	buf.SetQuality(80)
	buf.Update(testJPEG(320, 240))
	buf.SetStatus(StatusConnected)
	buf.SetWidth(1920)
	buf.SetHeight(1080)
	s := &Server{
		Source: &fakeSource{
			frame:    testJPEG(320, 240),
			rawFrame: testJPEG(1920, 1080),
			status:   StatusConnected,
			cropRect: image.Rect(240, 0, 1680, 1080),
		},
		Buf:        buf,
		Device:     "Test Device",
		SourceType: "hardware",
	}
	mux := NewMux(s)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := bidiDial(t, ts)
	resp := parseBidiResp(t, bidiSend(t, conn, map[string]any{
		"id": 1, "method": "roadie:screen.getViewport", "params": map[string]any{},
	}))

	var result struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	json.Unmarshal(resp.Result, &result)

	if result.Width != 1440 {
		t.Errorf("expected cropped width 1440, got %d", result.Width)
	}
	if result.Height != 1080 {
		t.Errorf("expected cropped height 1080, got %d", result.Height)
	}
}

func TestBiDiUnknownMethod(t *testing.T) {
	resetBidiState()
	_, mux := newTestServer()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := bidiDial(t, ts)
	resp := parseBidiResp(t, bidiSend(t, conn, map[string]any{
		"id": 1, "method": "script.evaluate", "params": map[string]any{},
	}))

	if resp.Type != "error" {
		t.Fatalf("expected error, got %s", resp.Type)
	}
	if resp.Error != "unknown command" {
		t.Errorf("expected 'unknown command', got %q", resp.Error)
	}
}

func TestBiDiCoordinateTranslation(t *testing.T) {
	resetBidiState()

	buf := &FrameBuffer{}
	buf.SetWidth(1920)
	buf.SetHeight(1080)

	tests := []struct {
		name     string
		crop     image.Rectangle
		pixelX   float64
		pixelY   float64
		wantHIDX int
		wantHIDY int
	}{
		{
			name:     "no crop, top-left",
			crop:     image.Rectangle{},
			pixelX:   0,
			pixelY:   0,
			wantHIDX: 0,
			wantHIDY: 0,
		},
		{
			name:     "no crop, center",
			crop:     image.Rectangle{},
			pixelX:   960,
			pixelY:   540,
			wantHIDX: 16383,
			wantHIDY: 16383,
		},
		{
			name:     "no crop, bottom-right",
			crop:     image.Rectangle{},
			pixelX:   1920,
			pixelY:   1080,
			wantHIDX: 32767,
			wantHIDY: 32767,
		},
		{
			name:     "with crop offset",
			crop:     image.Rect(240, 0, 1680, 1080),
			pixelX:   0,
			pixelY:   0,
			wantHIDX: 4096, // 240/1920 * 32767 ≈ 4096
			wantHIDY: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{
				Source: &fakeSource{cropRect: tt.crop},
				Buf:    buf,
			}
			gotX, gotY := s.bidiToHID(tt.pixelX, tt.pixelY)
			if abs(gotX-tt.wantHIDX) > 1 {
				t.Errorf("hidX: got %d, want %d", gotX, tt.wantHIDX)
			}
			if abs(gotY-tt.wantHIDY) > 1 {
				t.Errorf("hidY: got %d, want %d", gotY, tt.wantHIDY)
			}
		})
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func TestBiDiKeyMapping(t *testing.T) {
	tests := []struct {
		value string
		want  int
	}{
		{"a", 4},
		{"z", 29},
		{"A", 4},
		{"1", 30},
		{"0", 39},
		{" ", 44},
		{"\uE006", 40},  // Return
		{"\uE007", 40},  // Enter
		{"\uE003", 42},  // Backspace
		{"\uE008", 225}, // Shift
		{"\uE009", 224}, // Control
		{"\uE00A", 226}, // Alt
		{"\uE03D", 227}, // Meta
		{"\uE012", 80},  // Arrow Left
		{"\uE015", 81},  // Arrow Down
		{"\uE031", 58},  // F1
		{"\uE03C", 69},  // F12
	}

	for _, tt := range tests {
		code, ok := bidiKeyToHID(tt.value)
		if !ok {
			t.Errorf("key %q not found", tt.value)
			continue
		}
		if code != tt.want {
			t.Errorf("key %q: got %d, want %d", tt.value, code, tt.want)
		}
	}

	// Unknown key should return false.
	_, ok := bidiKeyToHID("\uEFFF")
	if ok {
		t.Error("expected unknown key to return false")
	}
}

func TestBiDiButtonMapping(t *testing.T) {
	if bidiButtonToMask(0) != 1 {
		t.Error("button 0 should map to mask 1 (left)")
	}
	if bidiButtonToMask(1) != 4 {
		t.Error("button 1 should map to mask 4 (middle)")
	}
	if bidiButtonToMask(2) != 2 {
		t.Error("button 2 should map to mask 2 (right)")
	}
}

func TestBiDiSessionCleanupOnDisconnect(t *testing.T) {
	resetBidiState()
	_, mux := newTestServer()
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Connect and create a session.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, ts.URL+"/session", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	data, _ := json.Marshal(map[string]any{
		"id": 1, "method": "session.new", "params": map[string]any{},
	})
	conn.Write(ctx, websocket.MessageText, data)
	conn.Read(ctx) // read response

	// Verify session exists.
	bidiSessionMu.Lock()
	if bidiActive == nil {
		bidiSessionMu.Unlock()
		t.Fatal("expected active session")
	}
	bidiSessionMu.Unlock()

	// Disconnect.
	conn.Close(websocket.StatusNormalClosure, "bye")

	// Give the handler goroutine time to clean up.
	time.Sleep(100 * time.Millisecond)

	// Session should be cleaned up.
	bidiSessionMu.Lock()
	defer bidiSessionMu.Unlock()
	if bidiActive != nil {
		t.Error("expected session to be cleaned up on disconnect")
	}
}
