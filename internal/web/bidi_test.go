package web

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/VibiumDev/roadie/internal/capture"
	"github.com/VibiumDev/roadie/internal/hid"
	"github.com/VibiumDev/roadie/internal/platform"
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

	buf := &capture.FrameBuffer{}
	buf.SetQuality(80)
	buf.Update(capture.TestJPEG(320, 240))
	buf.SetStatus(capture.StatusConnected)
	buf.SetWidth(1920)
	buf.SetHeight(1080)
	s := &Server{
		Source: &fakeSource{
			frame:    capture.TestJPEG(320, 240),
			rawFrame: capture.TestJPEG(1920, 1080),
			status:   capture.StatusConnected,
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

	buf := &capture.FrameBuffer{}
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
			if hid.Abs(gotX-tt.wantHIDX) > 1 {
				t.Errorf("hidX: got %d, want %d", gotX, tt.wantHIDX)
			}
			if hid.Abs(gotY-tt.wantHIDY) > 1 {
				t.Errorf("hidY: got %d, want %d", gotY, tt.wantHIDY)
			}
		})
	}
}

// An iPhone's absolute pointer spans its own screen, so a BiDi pixel maps
// within the phone's picture, not across the whole frame around it.
func TestBiDiToHIDScreenPointerSpace(t *testing.T) {
	buf := &capture.FrameBuffer{}
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
				Platform: platform.PlatformIOS,
			}
			gotX, gotY := s.bidiToHID(tt.pixelX, tt.pixelY)
			if hid.Abs(gotX-tt.wantX) > 1 || hid.Abs(gotY-tt.wantY) > 1 {
				t.Errorf("got (%d,%d), want (%d,%d)", gotX, gotY, tt.wantX, tt.wantY)
			}
		})
	}
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

// A character typed with Shift gets one around it; one the client already
// holds is left to the client.
func TestBiDiShiftedCharacters(t *testing.T) {
	press := func(s *Server, sess *bidiSession, typ, v string) {
		t.Helper()
		if err := s.executeBiDiAction(sess, "key", "", "kbd", bidiAction{Type: typ, Value: v}); err != nil {
			t.Fatal(err)
		}
	}

	k := &keyHID{}
	s := &Server{HID: k}
	sess := &bidiSession{}
	press(s, sess, "keyDown", ":")
	press(s, sess, "keyUp", ":")
	if got, want := strings.Join(k.log, " "), "+225 +51 -51 -225"; got != want {
		t.Errorf("\":\": %s, want %s", got, want)
	}
	if len(sess.heldKeys) != 0 {
		t.Errorf("still held: %v", sess.heldKeys)
	}

	k.log = nil
	press(s, sess, "keyDown", "\uE008") // the client holds Shift itself
	press(s, sess, "keyDown", "A")
	press(s, sess, "keyUp", "A")
	press(s, sess, "keyUp", "\uE008")
	if got, want := strings.Join(k.log, " "), "+225 +4 -4 -225"; got != want {
		t.Errorf("Shift+A: %s, want %s", got, want)
	}

	// Let go of everything mid-character: the added Shift goes too.
	k.log = nil
	press(s, sess, "keyDown", "?")
	s.releaseAllInput(sess)
	if got, want := strings.Join(k.log, " "), "+225 +56 -56 -225"; got != want {
		t.Errorf("release all: %s, want %s", got, want)
	}
}

// A wheel source's scroll turns the wheel with the pointer where it says, a
// notch per bidiNotchShare-th of the viewport's height (at least one), and
// puts the pointer back. A sideways scroll, one from anywhere but the
// viewport, or one from a pointer source is refused.
func TestBiDiScroll(t *testing.T) {
	inner := &wheelHID{}
	h := hid.NewSettledHID(inner)
	h.Sleep = func(time.Duration) {}
	s := &Server{HID: h, Source: &capture.TimedSource{Clock: new(time.Time)}, Buf: &capture.FrameBuffer{}}
	h.MouseMove(100, 200)
	inner.log = nil
	scroll := func(src string, a bidiAction) error {
		a.Type = "scroll"
		return s.executeBiDiAction(&bidiSession{}, src, "", "wheel", a)
	}

	// A 1920x1080 viewport: 135 px is four notches; 5 px, less than one.
	for _, dy := range []float64{135, -5} {
		if err := scroll("wheel", bidiAction{X: 960, Y: 540, DeltaY: dy}); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"move 16384,16384", "wheel 4", "move 100,200", "move 16384,16384", "wheel -1", "move 100,200"}
	if got := inner.log; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("calls = %v, want %v", got, want)
	}

	// On an iPhone, notches enough for about the distance asked, and never
	// fewer than move it at all.
	ios := &Server{Platform: platform.PlatformIOS}
	for dy, want := range map[float64]int{540: 9, -100: -4, 10: 3, 0: 0} {
		if got := ios.bidiNotches(dy, 1080); got != want {
			t.Errorf("iPhone, %v px of 1080: %d notches, want %d", dy, got, want)
		}
	}

	inner.log = nil
	for name, a := range map[string]bidiAction{
		"sideways":         {DeltaX: 50, DeltaY: 10},
		"from the pointer": {DeltaY: 10, Origin: "pointer"},
	} {
		if scroll("wheel", a) == nil {
			t.Errorf("%s: not refused", name)
		}
	}
	if scroll("pointer", bidiAction{DeltaY: 10}) == nil {
		t.Error("a pointer source's scroll: not refused")
	}
	if len(inner.log) != 0 {
		t.Errorf("refused scrolls reached the target: %v", inner.log)
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

// On an iPhone a two-finger gesture is refused whole, before either finger
// goes down; one finger, or two anywhere else, is fine.
func TestBiDiRefusesTwoFingersBeforeRunning(t *testing.T) {
	touch := func(id string, down bool) bidiActionSource {
		acts := []bidiAction{{Type: "pointerMove", X: 10, Y: 10}}
		if down {
			acts = append(acts, bidiAction{Type: "pointerDown"}, bidiAction{Type: "pointerUp"})
		}
		return bidiActionSource{Type: "pointer", ID: id, Parameters: &bidiPointerParam{PointerType: "touch"}, Actions: acts}
	}
	mouse := bidiActionSource{Type: "pointer", ID: "mouse", Actions: []bidiAction{{Type: "pointerDown"}}}

	ios := &Server{Platform: platform.PlatformIOS}
	if err := ios.checkBiDiActions([]bidiActionSource{touch("f0", true), touch("f1", true)}); err != hid.ErrOneFinger {
		t.Errorf("two fingers on iOS: %v, want errOneFinger", err)
	}
	for name, srcs := range map[string][]bidiActionSource{
		"one finger":           {touch("f0", true)},
		"a finger and a hover": {touch("f0", true), touch("f1", false)},
		"a finger and a mouse": {touch("f0", true), mouse},
	} {
		if err := ios.checkBiDiActions(srcs); err != nil {
			t.Errorf("%s on iOS: %v", name, err)
		}
	}
	android := &Server{Platform: platform.PlatformAndroid}
	if err := android.checkBiDiActions([]bidiActionSource{touch("f0", true), touch("f1", true)}); err != nil {
		t.Errorf("two fingers on Android: %v", err)
	}
}

// A connection that dropped without session.end used to wedge BiDi for
// good: the close path held bidiSessionMu while releaseAllInput took it
// again, so every later session.new waited on it until a restart.
func TestBiDiDroppedConnectionFreesTheSession(t *testing.T) {
	resetBidiState()
	s, mux := newTestServer()
	s.HID = &keyHID{} // the close path releases input only with a HID
	ts := httptest.NewServer(mux)
	defer ts.Close()

	first := bidiDial(t, ts)
	resp := parseBidiResp(t, bidiSend(t, first, map[string]any{
		"id": 1, "method": "session.new", "params": map[string]any{"capabilities": map[string]any{}},
	}))
	if resp.Type != "success" {
		t.Fatalf("first session.new: %s: %s", resp.Type, resp.Message)
	}
	first.CloseNow() // dropped, no session.end

	// The server sees the drop a moment after the socket goes.
	second := bidiDial(t, ts)
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp = parseBidiResp(t, bidiSend(t, second, map[string]any{
			"id": 2, "method": "session.new", "params": map[string]any{"capabilities": map[string]any{}},
		}))
		if resp.Type == "success" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session.new after a dropped connection: %s: %s", resp.Type, resp.Message)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A second connection that was refused and then closes must not end the
// first connection's session.
func TestBiDiRefusedConnectionCloseKeepsTheSession(t *testing.T) {
	resetBidiState()
	s, mux := newTestServer()
	s.HID = &keyHID{}
	ts := httptest.NewServer(mux)
	defer ts.Close()

	first := bidiDial(t, ts)
	resp := parseBidiResp(t, bidiSend(t, first, map[string]any{
		"id": 1, "method": "session.new", "params": map[string]any{"capabilities": map[string]any{}},
	}))
	if resp.Type != "success" {
		t.Fatalf("first session.new: %s: %s", resp.Type, resp.Message)
	}
	second := bidiDial(t, ts)
	resp = parseBidiResp(t, bidiSend(t, second, map[string]any{
		"id": 2, "method": "session.new", "params": map[string]any{"capabilities": map[string]any{}},
	}))
	if resp.Type != "error" {
		t.Fatalf("second session.new should be refused, got %s", resp.Type)
	}
	second.CloseNow()
	time.Sleep(200 * time.Millisecond) // let the server handle the close

	// Only a live session may act; an empty action list is enough to ask.
	resp = parseBidiResp(t, bidiSend(t, first, map[string]any{
		"id": 3, "method": "input.performActions", "params": map[string]any{"context": "screen", "actions": []any{}},
	}))
	if resp.Type != "success" {
		t.Fatalf("the first session was ended by the refused connection's close: %s: %s", resp.Type, resp.Message)
	}
}

// A session that sends no command for bidiIdleTimeout is ended: the
// socket closes with a reason, and the device is free for the next one.
func TestBiDiIdleSessionEnds(t *testing.T) {
	resetBidiState()
	old := bidiIdleTimeout
	bidiIdleTimeout = 150 * time.Millisecond
	t.Cleanup(func() { bidiIdleTimeout = old })
	s, mux := newTestServer()
	s.HID = &keyHID{}
	ts := httptest.NewServer(mux)
	defer ts.Close()

	conn := bidiDial(t, ts)
	resp := parseBidiResp(t, bidiSend(t, conn, map[string]any{
		"id": 1, "method": "session.new", "params": map[string]any{"capabilities": map[string]any{}},
	}))
	if resp.Type != "success" {
		t.Fatalf("session.new: %s: %s", resp.Type, resp.Message)
	}
	// Commands keep it alive: four of them, each inside the timeout.
	for i := 0; i < 4; i++ {
		time.Sleep(60 * time.Millisecond)
		resp = parseBidiResp(t, bidiSend(t, conn, map[string]any{"id": 10 + i, "method": "session.status", "params": map[string]any{}}))
		if resp.Type != "success" {
			t.Fatalf("session.status %d: %s: %s", i, resp.Type, resp.Message)
		}
	}
	// Then silence: the server closes the socket with the reason.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("expected a close after the idle timeout, got %v", err)
	}
	if ce.Code != websocket.StatusGoingAway || !strings.HasPrefix(ce.Reason, "idle for") {
		t.Fatalf("close %d %q, want 1001 idle for ...", ce.Code, ce.Reason)
	}

	next := bidiDial(t, ts)
	resp = parseBidiResp(t, bidiSend(t, next, map[string]any{
		"id": 2, "method": "session.new", "params": map[string]any{"capabilities": map[string]any{}},
	}))
	if resp.Type != "success" {
		t.Fatalf("session.new after the idle end: %s: %s", resp.Type, resp.Message)
	}
}
