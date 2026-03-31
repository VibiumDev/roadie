package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"log"
	"math"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

// BiDi message types.

type bidiCommand struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type bidiResult struct {
	Type   string `json:"type"`
	ID     int    `json:"id"`
	Result any    `json:"result"`
}

type bidiErrorResp struct {
	Type    string `json:"type"`
	ID      int    `json:"id"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

// bidiPointerPos tracks the current position of a pointer source.
type bidiPointerPos struct {
	x, y float64
}

// bidiSession tracks state for an active BiDi session.
type bidiSession struct {
	id          string
	heldKeys    []int
	heldButtons int
	touchState  [2]*TouchContact
	pointerPos  map[string]*bidiPointerPos // keyed by source ID
}

// bidiSessionMu guards the active session on the Server.
// We store the session pointer and mutex here to avoid adding fields to Server.
var (
	bidiSessionMu sync.Mutex
	bidiActive    *bidiSession
)

func (s *Server) handleBiDi(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("bidi websocket accept: %v", err)
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			// Connection closed — clean up session if this connection owns it.
			bidiSessionMu.Lock()
			if bidiActive != nil {
				s.releaseAllInput(bidiActive)
				bidiActive = nil
			}
			bidiSessionMu.Unlock()
			return
		}

		var cmd bidiCommand
		if err := json.Unmarshal(data, &cmd); err != nil {
			writeJSON(conn, ctx, bidiErrorResp{
				Type:    "error",
				ID:      0,
				Error:   "invalid argument",
				Message: "invalid JSON",
			})
			continue
		}

		switch cmd.Method {
		case "session.status":
			s.bidiSessionStatus(conn, ctx, cmd)
		case "session.new":
			s.bidiSessionNew(conn, ctx, cmd)
		case "session.end":
			s.bidiSessionEnd(conn, ctx, cmd)
		case "browsingContext.getTree":
			s.bidiGetTree(conn, ctx, cmd)
		case "browsingContext.captureScreenshot":
			s.bidiCaptureScreenshot(conn, ctx, cmd)
		case "roadie:screen.getViewport":
			s.bidiGetViewport(conn, ctx, cmd)
		case "input.performActions":
			s.bidiPerformActions(conn, ctx, cmd)
		case "input.releaseActions":
			s.bidiReleaseActions(conn, ctx, cmd)
		default:
			writeJSON(conn, ctx, bidiErrorResp{
				Type:    "error",
				ID:      cmd.ID,
				Error:   "unknown command",
				Message: fmt.Sprintf("unsupported method: %s", cmd.Method),
			})
		}
	}
}

// session.status

func (s *Server) bidiSessionStatus(conn *websocket.Conn, ctx context.Context, cmd bidiCommand) {
	ready := s.Source.Status() == StatusConnected && s.HID != nil && s.HID.Status() == HIDConnected
	msg := "ready"
	if !ready {
		parts := []string{}
		if s.Source.Status() != StatusConnected {
			parts = append(parts, fmt.Sprintf("capture: %s", s.Source.Status()))
		}
		if s.HID == nil || s.HID.Status() != HIDConnected {
			parts = append(parts, "hid: disconnected")
		}
		msg = strings.Join(parts, ", ")
	}
	writeJSON(conn, ctx, bidiResult{
		Type: "success",
		ID:   cmd.ID,
		Result: map[string]any{
			"ready":   ready,
			"message": msg,
		},
	})
}

// session.new

func (s *Server) bidiSessionNew(conn *websocket.Conn, ctx context.Context, cmd bidiCommand) {
	bidiSessionMu.Lock()
	defer bidiSessionMu.Unlock()

	if bidiActive != nil {
		writeJSON(conn, ctx, bidiErrorResp{
			Type:    "error",
			ID:      cmd.ID,
			Error:   "session not created",
			Message: "a session is already active",
		})
		return
	}

	id := randomHex(16)
	bidiActive = &bidiSession{id: id, pointerPos: map[string]*bidiPointerPos{}}

	caps := map[string]any{
		"acceptInsecureCerts": false,
		"browserName":         "roadie",
		"platformName":        runtime.GOOS,
		"setWindowRect":       false,
	}
	if s.HID != nil {
		caps["roadie:hidStatus"] = string(s.HID.Status())
	} else {
		caps["roadie:hidStatus"] = "unavailable"
	}
	caps["roadie:sourceType"] = s.SourceType
	caps["roadie:device"] = s.Device
	caps["roadie:resolution"] = fmt.Sprintf("%dx%d", s.Buf.Width(), s.Buf.Height())
	caps["roadie:viewport"] = s.viewportSize()

	writeJSON(conn, ctx, bidiResult{
		Type: "success",
		ID:   cmd.ID,
		Result: map[string]any{
			"sessionId":    id,
			"capabilities": caps,
		},
	})
}

// session.end

func (s *Server) bidiSessionEnd(conn *websocket.Conn, ctx context.Context, cmd bidiCommand) {
	bidiSessionMu.Lock()
	sess := bidiActive
	bidiActive = nil
	bidiSessionMu.Unlock()

	if sess != nil {
		s.releaseAllInput(sess)
	}

	writeJSON(conn, ctx, bidiResult{
		Type:   "success",
		ID:     cmd.ID,
		Result: map[string]any{},
	})
}

// browsingContext.getTree

func (s *Server) bidiGetTree(conn *websocket.Conn, ctx context.Context, cmd bidiCommand) {
	writeJSON(conn, ctx, bidiResult{
		Type: "success",
		ID:   cmd.ID,
		Result: map[string]any{
			"contexts": []map[string]any{
				{
					"context":  "screen",
					"url":      "",
					"children": []any{},
				},
			},
		},
	})
}

// roadie:screen.getViewport

func (s *Server) bidiGetViewport(conn *websocket.Conn, ctx context.Context, cmd bidiCommand) {
	writeJSON(conn, ctx, bidiResult{
		Type:   "success",
		ID:     cmd.ID,
		Result: s.viewportSize(),
	})
}

// browsingContext.captureScreenshot

func (s *Server) bidiCaptureScreenshot(conn *websocket.Conn, ctx context.Context, cmd bidiCommand) {
	frame := s.Source.Latest()
	if frame == nil {
		writeJSON(conn, ctx, bidiErrorResp{
			Type:    "error",
			ID:      cmd.ID,
			Error:   "unable to capture screenshot",
			Message: "no frame available",
		})
		return
	}

	viewport := s.viewportSize()
	writeJSON(conn, ctx, bidiResult{
		Type: "success",
		ID:   cmd.ID,
		Result: map[string]any{
			"data":            base64.StdEncoding.EncodeToString(frame),
			"roadie:viewport": viewport,
		},
	})
}

// input.performActions

type bidiActionSource struct {
	Type       string            `json:"type"`
	ID         string            `json:"id"`
	Parameters *bidiPointerParam `json:"parameters,omitempty"`
	Actions    []bidiAction      `json:"actions"`
}

type bidiPointerParam struct {
	PointerType string `json:"pointerType,omitempty"` // "mouse" or "touch"
}

type bidiAction struct {
	Type     string  `json:"type"`               // pointerMove, pointerDown, pointerUp, keyDown, keyUp, pause
	X        float64 `json:"x,omitempty"`         // pixel coords for pointer
	Y        float64 `json:"y,omitempty"`         // pixel coords for pointer
	Button   int     `json:"button,omitempty"`    // 0=left, 1=middle, 2=right
	Value    string  `json:"value,omitempty"`     // key value for keyDown/keyUp
	Duration int     `json:"duration,omitempty"`  // pause duration in ms
}

func (s *Server) bidiPerformActions(conn *websocket.Conn, ctx context.Context, cmd bidiCommand) {
	if s.HID == nil {
		writeJSON(conn, ctx, bidiErrorResp{
			Type:    "error",
			ID:      cmd.ID,
			Error:   "unable to set cookie",
			Message: "HID not available",
		})
		return
	}

	bidiSessionMu.Lock()
	sess := bidiActive
	bidiSessionMu.Unlock()
	if sess == nil {
		writeJSON(conn, ctx, bidiErrorResp{
			Type:    "error",
			ID:      cmd.ID,
			Error:   "no such session",
			Message: "no active session",
		})
		return
	}

	var params struct {
		Actions []bidiActionSource `json:"actions"`
	}
	if err := json.Unmarshal(cmd.Params, &params); err != nil {
		writeJSON(conn, ctx, bidiErrorResp{
			Type:    "error",
			ID:      cmd.ID,
			Error:   "invalid argument",
			Message: "invalid actions params",
		})
		return
	}

	// Find the max number of ticks across all sources.
	maxTicks := 0
	for _, src := range params.Actions {
		if len(src.Actions) > maxTicks {
			maxTicks = len(src.Actions)
		}
	}

	// Execute tick by tick.
	for tick := 0; tick < maxTicks; tick++ {
		for _, src := range params.Actions {
			if tick >= len(src.Actions) {
				continue
			}
			action := src.Actions[tick]
			ptrType := "mouse"
			if src.Parameters != nil && src.Parameters.PointerType != "" {
				ptrType = src.Parameters.PointerType
			}

			if err := s.executeBiDiAction(sess, src.Type, ptrType, src.ID, action); err != nil {
				writeJSON(conn, ctx, bidiErrorResp{
					Type:    "error",
					ID:      cmd.ID,
					Error:   "unknown error",
					Message: err.Error(),
				})
				return
			}
		}
	}

	writeJSON(conn, ctx, bidiResult{
		Type:   "success",
		ID:     cmd.ID,
		Result: map[string]any{},
	})
}

func (s *Server) executeBiDiAction(sess *bidiSession, srcType, ptrType, srcID string, action bidiAction) error {
	switch action.Type {
	case "pause":
		if action.Duration > 0 {
			time.Sleep(time.Duration(action.Duration) * time.Millisecond)
		}
		return nil

	case "pointerMove":
		// Get starting position for interpolation.
		bidiSessionMu.Lock()
		prev := sess.pointerPos[srcID]
		bidiSessionMu.Unlock()
		startX, startY := 0.0, 0.0
		if prev != nil {
			startX, startY = prev.x, prev.y
		}
		endX, endY := action.X, action.Y

		// Store the final position for this source — pointerDown reads it later.
		bidiSessionMu.Lock()
		sess.pointerPos[srcID] = &bidiPointerPos{x: endX, y: endY}
		bidiSessionMu.Unlock()

		// If duration > 0, interpolate the move over time.
		if action.Duration > 0 {
			return s.interpolateMove(sess, ptrType, srcID, startX, startY, endX, endY, action.Duration)
		}

		hidX, hidY := s.bidiToHID(endX, endY)
		if ptrType == "touch" {
			contactID := s.touchContactID(srcID)
			bidiSessionMu.Lock()
			tc := sess.touchState[contactID]
			if tc != nil && tc.Tip {
				tc.X = hidX
				tc.Y = hidY
				contacts := s.activeContacts(sess)
				bidiSessionMu.Unlock()
				return s.HID.Touch(contacts)
			}
			bidiSessionMu.Unlock()
			return nil
		}
		return s.HID.MouseMove(hidX, hidY)

	case "pointerDown":
		// Use stored position from the last pointerMove for this source.
		bidiSessionMu.Lock()
		pos := sess.pointerPos[srcID]
		bidiSessionMu.Unlock()
		px, py := 0.0, 0.0
		if pos != nil {
			px, py = pos.x, pos.y
		}

		if ptrType == "touch" {
			contactID := s.touchContactID(srcID)
			hidX, hidY := s.bidiToHID(px, py)
			bidiSessionMu.Lock()
			sess.touchState[contactID] = &TouchContact{
				ID:  contactID,
				Tip: true,
				X:   hidX,
				Y:   hidY,
			}
			contacts := s.activeContacts(sess)
			bidiSessionMu.Unlock()
			return s.HID.Touch(contacts)
		}
		btn := bidiButtonToMask(action.Button)
		bidiSessionMu.Lock()
		sess.heldButtons |= btn
		bidiSessionMu.Unlock()
		return s.HID.MousePress(btn)

	case "pointerUp":
		if ptrType == "touch" {
			contactID := s.touchContactID(srcID)
			bidiSessionMu.Lock()
			if sess.touchState[contactID] != nil {
				sess.touchState[contactID].Tip = false
			}
			contacts := s.activeContacts(sess)
			bidiSessionMu.Unlock()
			if err := s.HID.Touch(contacts); err != nil {
				return err
			}
			bidiSessionMu.Lock()
			sess.touchState[contactID] = nil
			bidiSessionMu.Unlock()
			return nil
		}
		btn := bidiButtonToMask(action.Button)
		bidiSessionMu.Lock()
		sess.heldButtons &^= btn
		bidiSessionMu.Unlock()
		return s.HID.MouseRelease(btn)

	case "keyDown":
		keycode, ok := bidiKeyToHID(action.Value)
		if !ok {
			return fmt.Errorf("unsupported key: %q", action.Value)
		}
		bidiSessionMu.Lock()
		sess.heldKeys = append(sess.heldKeys, keycode)
		bidiSessionMu.Unlock()
		return s.HID.KeyPress(keycode)

	case "keyUp":
		keycode, ok := bidiKeyToHID(action.Value)
		if !ok {
			return fmt.Errorf("unsupported key: %q", action.Value)
		}
		bidiSessionMu.Lock()
		for i, k := range sess.heldKeys {
			if k == keycode {
				sess.heldKeys = append(sess.heldKeys[:i], sess.heldKeys[i+1:]...)
				break
			}
		}
		bidiSessionMu.Unlock()
		return s.HID.KeyRelease(keycode)

	default:
		return fmt.Errorf("unsupported action type: %s", action.Type)
	}
}

// input.releaseActions

// interpolateMove moves a pointer from (startX, startY) to (endX, endY) over
// duration milliseconds, sending intermediate HID reports at ~60Hz.
func (s *Server) interpolateMove(sess *bidiSession, ptrType, srcID string, startX, startY, endX, endY float64, durationMs int) error {
	const stepInterval = 16 * time.Millisecond // ~60Hz
	total := time.Duration(durationMs) * time.Millisecond
	steps := int(total / stepInterval)
	if steps < 1 {
		steps = 1
	}

	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		curX := startX + (endX-startX)*t
		curY := startY + (endY-startY)*t
		hidX, hidY := s.bidiToHID(curX, curY)

		if ptrType == "touch" {
			contactID := s.touchContactID(srcID)
			bidiSessionMu.Lock()
			tc := sess.touchState[contactID]
			if tc != nil && tc.Tip {
				tc.X = hidX
				tc.Y = hidY
				contacts := s.activeContacts(sess)
				bidiSessionMu.Unlock()
				if err := s.HID.Touch(contacts); err != nil {
					return err
				}
			} else {
				bidiSessionMu.Unlock()
			}
		} else {
			if err := s.HID.MouseMove(hidX, hidY); err != nil {
				return err
			}
		}

		if i < steps {
			time.Sleep(stepInterval)
		}
	}
	return nil
}

func (s *Server) bidiReleaseActions(conn *websocket.Conn, ctx context.Context, cmd bidiCommand) {
	bidiSessionMu.Lock()
	sess := bidiActive
	bidiSessionMu.Unlock()

	if sess != nil {
		s.releaseAllInput(sess)
	}

	writeJSON(conn, ctx, bidiResult{
		Type:   "success",
		ID:     cmd.ID,
		Result: map[string]any{},
	})
}

func (s *Server) releaseAllInput(sess *bidiSession) {
	if s.HID == nil {
		return
	}

	bidiSessionMu.Lock()
	keys := make([]int, len(sess.heldKeys))
	copy(keys, sess.heldKeys)
	sess.heldKeys = nil
	buttons := sess.heldButtons
	sess.heldButtons = 0
	var touches [2]*TouchContact
	copy(touches[:], sess.touchState[:])
	sess.touchState = [2]*TouchContact{}
	bidiSessionMu.Unlock()

	for i := len(keys) - 1; i >= 0; i-- {
		s.HID.KeyRelease(keys[i])
	}
	if buttons != 0 {
		s.HID.MouseRelease(buttons)
	}
	hasTouch := false
	for _, tc := range touches {
		if tc != nil {
			hasTouch = true
			break
		}
	}
	if hasTouch {
		s.HID.Touch([]TouchContact{})
	}
}

// viewportSize returns the current viewport (cropped image) dimensions.
func (s *Server) viewportSize() map[string]int {
	crop := s.Source.CropRect()
	if crop == (image.Rectangle{}) {
		return map[string]int{"width": s.Buf.Width(), "height": s.Buf.Height()}
	}
	return map[string]int{"width": crop.Dx(), "height": crop.Dy()}
}

// Coordinate translation: BiDi pixel coords → HID 0-32767.

func (s *Server) bidiToHID(pixelX, pixelY float64) (int, int) {
	crop := s.Source.CropRect()
	captureW := float64(s.Buf.Width())
	captureH := float64(s.Buf.Height())

	// If no crop, treat crop as the full frame.
	if crop == (image.Rectangle{}) {
		crop = image.Rect(0, 0, int(captureW), int(captureH))
	}

	// BiDi coords are relative to the cropped viewport.
	// Scale pixel position within the crop to full capture coords.
	fullX := float64(crop.Min.X) + pixelX*float64(crop.Dx())/float64(crop.Dx())
	fullY := float64(crop.Min.Y) + pixelY*float64(crop.Dy())/float64(crop.Dy())
	// Simplifies to: fullX = crop.Min.X + pixelX, fullY = crop.Min.Y + pixelY

	hidX := int(math.Round(fullX / captureW * 32767))
	hidY := int(math.Round(fullY / captureH * 32767))

	return clampInt(hidX, 0, 32767), clampInt(hidY, 0, 32767)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Touch contact helpers.

// touchContactID maps a pointer source ID to a contact index (0 or 1).
// First touch source seen gets 0, second gets 1.
var (
	touchSourceMap   = map[string]int{}
	touchSourceMu    sync.Mutex
	touchSourceCount int
)

func (s *Server) touchContactID(srcID string) int {
	touchSourceMu.Lock()
	defer touchSourceMu.Unlock()
	if id, ok := touchSourceMap[srcID]; ok {
		return id
	}
	id := touchSourceCount % 2
	touchSourceMap[srcID] = id
	touchSourceCount++
	return id
}

func (s *Server) activeContacts(sess *bidiSession) []TouchContact {
	// Caller must hold bidiSessionMu.
	var contacts []TouchContact
	for _, tc := range sess.touchState {
		if tc != nil {
			contacts = append(contacts, *tc)
		}
	}
	return contacts
}

// BiDi button index to USB HID button mask.

func bidiButtonToMask(button int) int {
	switch button {
	case 0:
		return 1 // left
	case 1:
		return 4 // middle
	case 2:
		return 2 // right
	default:
		return 1
	}
}

// BiDi key value to USB HID keycode.

func bidiKeyToHID(value string) (int, bool) {
	if code, ok := bidiSpecialKeys[value]; ok {
		return code, true
	}
	// Printable ASCII single character.
	if utf8.RuneCountInString(value) == 1 {
		r := []rune(value)[0]
		if code, ok := bidiPrintableKeys[r]; ok {
			return code, true
		}
	}
	return 0, false
}

// Printable character to HID keycode (lowercase; shift handling is done via modifier keys).
var bidiPrintableKeys = map[rune]int{
	'a': 4, 'b': 5, 'c': 6, 'd': 7, 'e': 8, 'f': 9, 'g': 10, 'h': 11,
	'i': 12, 'j': 13, 'k': 14, 'l': 15, 'm': 16, 'n': 17, 'o': 18, 'p': 19,
	'q': 20, 'r': 21, 's': 22, 't': 23, 'u': 24, 'v': 25, 'w': 26, 'x': 27,
	'y': 28, 'z': 29,
	'A': 4, 'B': 5, 'C': 6, 'D': 7, 'E': 8, 'F': 9, 'G': 10, 'H': 11,
	'I': 12, 'J': 13, 'K': 14, 'L': 15, 'M': 16, 'N': 17, 'O': 18, 'P': 19,
	'Q': 20, 'R': 21, 'S': 22, 'T': 23, 'U': 24, 'V': 25, 'W': 26, 'X': 27,
	'Y': 28, 'Z': 29,
	'1': 30, '2': 31, '3': 32, '4': 33, '5': 34,
	'6': 35, '7': 36, '8': 37, '9': 38, '0': 39,
	' ':  44,
	'-':  45, '=': 46, '[': 47, ']': 48, '\\': 49,
	';':  51, '\'': 52, '`': 53, ',': 54, '.': 55, '/': 56,
}

// BiDi special key values (Unicode Private Use Area) to HID keycodes.
// See: https://w3c.github.io/webdriver/#keyboard-actions
var bidiSpecialKeys = map[string]int{
	"\uE000": 0,   // Unidentified (null)
	"\uE001": 0,   // Cancel
	"\uE002": 0,   // Help
	"\uE003": 42,  // Backspace
	"\uE004": 43,  // Tab
	"\uE005": 0,   // Clear
	"\uE006": 40,  // Return
	"\uE007": 40,  // Enter
	"\uE008": 225, // Shift (left)
	"\uE009": 224, // Control (left)
	"\uE00A": 226, // Alt (left)
	"\uE00B": 0,   // Pause
	"\uE00C": 41,  // Escape
	"\uE00D": 44,  // Space
	"\uE00E": 75,  // Page Up
	"\uE00F": 78,  // Page Down
	"\uE010": 77,  // End
	"\uE011": 74,  // Home
	"\uE012": 80,  // Arrow Left
	"\uE013": 82,  // Arrow Up
	"\uE014": 79,  // Arrow Right
	"\uE015": 81,  // Arrow Down
	"\uE016": 73,  // Insert
	"\uE017": 76,  // Delete
	"\uE018": 51,  // Semicolon
	"\uE019": 46,  // Equals
	"\uE01A": 98,  // Numpad 0
	"\uE01B": 89,  // Numpad 1
	"\uE01C": 90,  // Numpad 2
	"\uE01D": 91,  // Numpad 3
	"\uE01E": 92,  // Numpad 4
	"\uE01F": 93,  // Numpad 5
	"\uE020": 94,  // Numpad 6
	"\uE021": 95,  // Numpad 7
	"\uE022": 96,  // Numpad 8
	"\uE023": 97,  // Numpad 9
	"\uE024": 85,  // Numpad Multiply
	"\uE025": 87,  // Numpad Add
	"\uE026": 133, // Numpad Separator
	"\uE027": 86,  // Numpad Subtract
	"\uE028": 99,  // Numpad Decimal
	"\uE029": 84,  // Numpad Divide
	"\uE031": 58,  // F1
	"\uE032": 59,  // F2
	"\uE033": 60,  // F3
	"\uE034": 61,  // F4
	"\uE035": 62,  // F5
	"\uE036": 63,  // F6
	"\uE037": 64,  // F7
	"\uE038": 65,  // F8
	"\uE039": 66,  // F9
	"\uE03A": 67,  // F10
	"\uE03B": 68,  // F11
	"\uE03C": 69,  // F12
	"\uE03D": 227, // Meta / GUI (left)
	"\uE040": 0,   // ZenkakuHankaku
	"\uE050": 229, // Shift (right)
	"\uE051": 228, // Control (right)
	"\uE052": 230, // Alt (right)
	"\uE053": 231, // Meta / GUI (right)
}

// Helpers.

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func writeJSON(conn *websocket.Conn, ctx context.Context, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("bidi marshal: %v", err)
		return
	}
	conn.Write(ctx, websocket.MessageText, data)
}
