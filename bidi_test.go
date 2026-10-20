package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"net/http/httptest"
	"strings"
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

// An iPhone's absolute pointer spans its own screen, so a BiDi pixel maps
// within the phone's picture, not across the whole frame around it.
func TestBiDiToHIDScreenPointerSpace(t *testing.T) {
	buf := &FrameBuffer{}
	buf.SetWidth(1920)
	buf.SetHeight(1080)
	phone := image.Rect(735, 54, 1184, 1027) // a 449x973 iPhone in the frame
	tests := []struct {
		name           string
		crop           image.Rectangle
		pixelX, pixelY float64
		wantX, wantY   int
	}{
		{"top-left of the phone", phone, 0, 0, 0, 0},
		{"bottom-right of the phone", phone, 449, 973, 32767, 32767},
		{"a button near the top left", phone, 44, 85, 3211, 2863},
		// A crop tighter than the picture: still placed within the picture.
		{"tighter crop", image.Rect(735, 76, 1184, 1007), 0, 0, 0, 741},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{
				Source:   &fakeSource{cropRect: tt.crop, contentRect: phone},
				Buf:      buf,
				Platform: PlatformIOS,
			}
			gotX, gotY := s.bidiToHID(tt.pixelX, tt.pixelY)
			if abs(gotX-tt.wantX) > 1 || abs(gotY-tt.wantY) > 1 {
				t.Errorf("got (%d,%d), want (%d,%d)", gotX, gotY, tt.wantX, tt.wantY)
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
		{":", 51},       // Shift+;
		{"?", 56},       // Shift+/
		{"!", 30},       // Shift+1
		{"@", 31},       // Shift+2
		{"_", 45},       // Shift+-
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

func TestBiDiKeyNeedsShift(t *testing.T) {
	for value, want := range map[string]bool{
		"a": false, "A": true, ":": true, ";": false, "/": false, "?": true,
		"1": false, "!": true, " ": false, "\uE007": false, "\uE008": false,
	} {
		if got := bidiKeyNeedsShift(value); got != want {
			t.Errorf("bidiKeyNeedsShift(%q) = %v, want %v", value, got, want)
		}
	}
}

// keyHID records key presses and releases; the rest of HIDBackend is inert.
type keyHID struct{ log []string }

func (k *keyHID) Run()                       {}
func (k *keyHID) Shutdown()                  {}
func (k *keyHID) Backend() string            { return "test" }
func (k *keyHID) Status() HIDStatus          { return HIDConnected }
func (k *keyHID) BoardResponding() bool      { return true }
func (k *keyHID) Port() string               { return "" }
func (k *keyHID) Serial() string             { return "" }
func (k *keyHID) Type(string) error          { return nil }
func (k *keyHID) MouseMove(int, int) error   { return nil }
func (k *keyHID) MouseClick(int) error       { return nil }
func (k *keyHID) MousePress(int) error       { return nil }
func (k *keyHID) MouseRelease(int) error     { return nil }
func (k *keyHID) MouseScroll(int) error      { return nil }
func (k *keyHID) Touch([]TouchContact) error { return nil }
func (k *keyHID) SendAudio([]byte) error     { return nil }
func (k *keyHID) ResetHID() error            { return nil }
func (k *keyHID) ResetRelay() error          { return nil }
func (k *keyHID) KeyPress(c int) error       { k.log = append(k.log, fmt.Sprintf("+%d", c)); return nil }
func (k *keyHID) KeyRelease(c int) error     { k.log = append(k.log, fmt.Sprintf("-%d", c)); return nil }

// A character typed with Shift gets one around it; one the client already
// holds is left to the client.
func TestBiDiShiftedCharacters(t *testing.T) {
	press := func(s *Server, sess *bidiSession, typ, v string) {
		t.Helper()
		if err := s.executeBiDiAction(sess, "key", "", "kbd", bidiAction{Type: typ, Value: v}); err != nil {
			t.Fatal(err)
		}
	}

	hid := &keyHID{}
	s := &Server{HID: hid}
	sess := &bidiSession{}
	press(s, sess, "keyDown", ":")
	press(s, sess, "keyUp", ":")
	if got, want := strings.Join(hid.log, " "), "+225 +51 -51 -225"; got != want {
		t.Errorf("\":\": %s, want %s", got, want)
	}
	if len(sess.heldKeys) != 0 {
		t.Errorf("still held: %v", sess.heldKeys)
	}

	hid.log = nil
	press(s, sess, "keyDown", "\uE008") // the client holds Shift itself
	press(s, sess, "keyDown", "A")
	press(s, sess, "keyUp", "A")
	press(s, sess, "keyUp", "\uE008")
	if got, want := strings.Join(hid.log, " "), "+225 +4 -4 -225"; got != want {
		t.Errorf("Shift+A: %s, want %s", got, want)
	}

	// Let go of everything mid-character: the added Shift goes too.
	hid.log = nil
	press(s, sess, "keyDown", "?")
	s.releaseAllInput(sess)
	if got, want := strings.Join(hid.log, " "), "+225 +56 -56 -225"; got != want {
		t.Errorf("release all: %s, want %s", got, want)
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
