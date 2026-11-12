package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
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

	"github.com/VibiumDev/roadie/internal/capture"
	"github.com/VibiumDev/roadie/internal/hid"
	"github.com/VibiumDev/roadie/internal/platform"
	"github.com/VibiumDev/station/rig"
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
	conn        *websocket.Conn // the connection that made it
	heldKeys    []int
	heldButtons int
	// shiftFor marks keys whose character needed a Shift this session
	// pressed for it (":" is Shift+";"), so keyUp lets go of that too.
	shiftFor   map[int]bool
	touchState [2]*hid.TouchContact
	pointerPos map[string]*bidiPointerPos // keyed by source ID
}

// bidiSessionMu guards the active session on the Server.
// We store the session pointer and mutex here to avoid adding fields to Server.
var (
	bidiSessionMu sync.Mutex
	bidiActive    *bidiSession
)

// bidiIdleTimeout ends a BiDi connection that sends no command for this
// long, so a session left open, say a CLI never stopped, does not keep the
// device for good. Through a station, the socket's close releases the hold.
var bidiIdleTimeout = 5 * time.Minute

// bidiDrop ends the session a connection owns, if it owns one, and
// releases what it held. releaseAllInput takes the mutex itself, so it
// runs after the unlock; holding it across the call deadlocked every
// later session.new until a restart.
func (s *Server) bidiDrop(conn *websocket.Conn) {
	bidiSessionMu.Lock()
	sess := bidiActive
	if sess != nil && sess.conn == conn {
		bidiActive = nil
	} else {
		sess = nil
	}
	bidiSessionMu.Unlock()
	if sess != nil {
		s.releaseAllInput(sess)
	}
}

func (s *Server) handleBiDi(w http.ResponseWriter, r *http.Request) {
	// The default same-origin check, as for /api/mic: a page on another
	// origin must not drive the target or read its screen.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Printf("bidi websocket accept: %v", err)
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(bidiReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sound := &bidiAudio{}
	defer sound.close()
	lim := rig.LimiterFrom(ctx) // through a station: the hold's pace
	idle := time.AfterFunc(bidiIdleTimeout, func() {
		log.Printf("bidi: no command for %s, closing the session", bidiIdleTimeout)
		// The session goes before the socket does: a client that opens a
		// new one the moment it sees the close must not find the old one
		// still active.
		s.bidiDrop(conn)
		conn.Close(websocket.StatusGoingAway, fmt.Sprintf("idle for %s", bidiIdleTimeout))
	})
	defer idle.Stop()
	for {
		_, data, err := conn.Read(ctx)
		if err == nil {
			idle.Reset(bidiIdleTimeout)
		}
		if err == nil && lim != nil {
			err = lim.Wait(ctx)
		}
		if err != nil {
			// Connection closed: end the session if this connection owns it.
			s.bidiDrop(conn)
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
		case "roadie:audio.clock":
			s.bidiAudioClock(conn, ctx, cmd)
		case "roadie:audio.play":
			s.bidiAudioPlay(conn, ctx, cmd, sound)
		case "roadie:audio.capture.start":
			s.bidiAudioCaptureStart(conn, ctx, cmd, sound)
		case "roadie:audio.capture.stop":
			s.bidiAudioCaptureStop(conn, ctx, cmd, sound)
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
	ready := s.Source.Status() == capture.StatusConnected && s.HID != nil && s.HID.Status() == hid.HIDConnected
	msg := "ready"
	if !ready {
		parts := []string{}
		if s.Source.Status() != capture.StatusConnected {
			parts = append(parts, fmt.Sprintf("capture: %s", s.Source.Status()))
		}
		if s.HID == nil || s.HID.Status() != hid.HIDConnected {
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
	bidiActive = &bidiSession{id: id, conn: conn, pointerPos: map[string]*bidiPointerPos{}}

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
	caps["roadie:audio"] = s.audioCaps()

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
	// Straight after input, show the screen once it has caught up with it.
	if h, ok := s.HID.(interface{ LastInput() time.Time }); ok && frame != nil {
		frame = capture.SettledFrame(ctx, s.Source, h.LastInput(), capture.RealSettleClock)
	}
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
	Type     string  `json:"type"`               // pointerMove, pointerDown, pointerUp, keyDown, keyUp, scroll, pause
	X        float64 `json:"x,omitempty"`        // pixel coords for pointer and scroll
	Y        float64 `json:"y,omitempty"`        // pixel coords for pointer and scroll
	Button   int     `json:"button,omitempty"`   // 0=left, 1=middle, 2=right
	Value    string  `json:"value,omitempty"`    // key value for keyDown/keyUp
	Duration int     `json:"duration,omitempty"` // pause duration in ms
	DeltaX   float64 `json:"deltaX,omitempty"`   // scroll, in pixels
	DeltaY   float64 `json:"deltaY,omitempty"`   // scroll, in pixels; positive scrolls down
	Origin   any     `json:"origin,omitempty"`   // scroll: "viewport" only
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

	if err := s.checkBiDiActions(params.Actions); err != nil {
		writeJSON(conn, ctx, bidiErrorResp{
			Type:    "error",
			ID:      cmd.ID,
			Error:   "invalid argument",
			Message: err.Error(),
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

	// Execute tick by tick. Through a station each action waits its turn
	// on the hold's pace, and nothing more reaches the target once the
	// hold has ended or the socket closed.
	pace := holdPace(ctx)
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
			if pace != nil && action.Type != "pause" {
				pace.WaitN(ctx, 1)
			}
			if ctx.Err() != nil {
				return
			}

			if err := s.executeBiDiAction(ctx, sess, src.Type, ptrType, src.ID, action); err != nil {
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

// What one input.performActions message may hold. Vibium, the client
// this endpoint is for, waits 60 s for a command to answer and types text
// as two key actions a character, so neither limit is near honest use: the
// longest pause or move is that wait, and 20,000 actions is a text of
// 10,000 characters.
const (
	bidiMaxActions    = 20000
	bidiMaxDurationMs = 60000 // of one pause, or one pointerMove
)

// checkBiDiActions refuses, before any of it reaches the target, what is
// too much for one message, and what the target can't do as a whole: on
// one that takes touch as its pointer, a gesture with more than one finger.
// Run tick by tick, it would press the first finger and only then fail on
// the second.
func (s *Server) checkBiDiActions(sources []bidiActionSource) error {
	actions := 0
	for _, src := range sources {
		actions += len(src.Actions)
		for _, a := range src.Actions {
			if a.Duration > bidiMaxDurationMs {
				return fmt.Errorf("an action lasts at most %d ms, not %d", bidiMaxDurationMs, a.Duration)
			}
		}
	}
	if actions > bidiMaxActions {
		return fmt.Errorf("one message holds at most %d actions, not %d", bidiMaxActions, actions)
	}
	if !s.Platform.TouchAsPointer() {
		return nil
	}
	fingers := 0
	for _, src := range sources {
		if src.Type != "pointer" || src.Parameters == nil || src.Parameters.PointerType != "touch" {
			continue
		}
		for _, a := range src.Actions {
			if a.Type == "pointerDown" {
				fingers++
				break
			}
		}
	}
	if fingers > 1 {
		return hid.ErrOneFinger
	}
	return nil
}

func (s *Server) executeBiDiAction(ctx context.Context, sess *bidiSession, srcType, ptrType, srcID string, action bidiAction) error {
	switch action.Type {
	case "pause":
		if action.Duration > 0 {
			sleepCtx(ctx, time.Duration(action.Duration)*time.Millisecond)
		}
		return nil

	case "scroll":
		return s.bidiScroll(srcType, action)

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
			return s.interpolateMove(ctx, sess, ptrType, srcID, startX, startY, endX, endY, action.Duration)
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
			sess.touchState[contactID] = &hid.TouchContact{
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
		// A character typed with Shift (":", "?", "A") gets one, unless
		// the client is already holding it.
		bidiSessionMu.Lock()
		addShift := bidiKeyNeedsShift(action.Value) && !shiftHeld(sess.heldKeys)
		if addShift {
			if sess.shiftFor == nil {
				sess.shiftFor = map[int]bool{}
			}
			sess.shiftFor[keycode] = true
			sess.heldKeys = append(sess.heldKeys, hid.ShiftKeycode)
		}
		sess.heldKeys = append(sess.heldKeys, keycode)
		bidiSessionMu.Unlock()
		if addShift {
			if err := s.HID.KeyPress(hid.ShiftKeycode); err != nil {
				return err
			}
		}
		return s.HID.KeyPress(keycode)

	case "keyUp":
		keycode, ok := bidiKeyToHID(action.Value)
		if !ok {
			return fmt.Errorf("unsupported key: %q", action.Value)
		}
		bidiSessionMu.Lock()
		sess.heldKeys = removeKey(sess.heldKeys, keycode)
		addedShift := sess.shiftFor[keycode]
		if addedShift {
			delete(sess.shiftFor, keycode)
			sess.heldKeys = removeKey(sess.heldKeys, hid.ShiftKeycode)
		}
		bidiSessionMu.Unlock()
		if err := s.HID.KeyRelease(keycode); err != nil {
			return err
		}
		if addedShift {
			return s.HID.KeyRelease(hid.ShiftKeycode)
		}
		return nil

	default:
		return fmt.Errorf("unsupported action type: %s", action.Type)
	}
}

// input.releaseActions

// interpolateMove moves a pointer from (startX, startY) to (endX, endY) over
// duration milliseconds, sending intermediate HID reports at ~60Hz. It
// stops where it is when ctx ends.
func (s *Server) interpolateMove(ctx context.Context, sess *bidiSession, ptrType, srcID string, startX, startY, endX, endY float64, durationMs int) error {
	const stepInterval = 16 * time.Millisecond // ~60Hz
	total := time.Duration(durationMs) * time.Millisecond
	steps := int(total / stepInterval)
	if steps < 1 {
		steps = 1
	}

	for i := 1; i <= steps; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
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
			sleepCtx(ctx, stepInterval)
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
	sess.shiftFor = nil
	buttons := sess.heldButtons
	sess.heldButtons = 0
	var touches [2]*hid.TouchContact
	copy(touches[:], sess.touchState[:])
	sess.touchState = [2]*hid.TouchContact{}
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
		s.HID.Touch([]hid.TouchContact{})
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

// How far notches of the wheel scroll, for turning a scroll's deltaY into
// notches. The target decides, so deltaY is a guide. An iPhone speeds a run
// up: in Safari on one, notches 8 ms apart (see notchesIOS) scrolled a 972
// px picture 0 px for 2, 35 for 3, 97 for 4, and about 88 px more for each
// one after (626 for 10), the same each time. Anywhere else a notch is
// taken to be a 32nd of the viewport.
const (
	iosNotch       = 88.0 / 972  // of the viewport, each notch past the start of a run
	iosRunStart    = 271.0 / 972 // what a run takes to get going, likewise
	iosFewest      = 3           // the fewest notches that move an iPhone
	bidiNotchShare = 32
)

// bidiNotches is how many notches scroll about dy pixels of a viewport h
// pixels tall; positive scrolls down.
func (s *Server) bidiNotches(dy, h float64) int {
	if dy == 0 || h <= 0 {
		return 0
	}
	var n int
	if s.Platform == platform.PlatformIOS {
		n = max(int(math.Round((math.Abs(dy)/h+iosRunStart)/iosNotch)), iosFewest)
	} else {
		n = max(int(math.Round(math.Abs(dy)*bidiNotchShare/h)), 1)
	}
	if dy < 0 {
		n = -n
	}
	return n
}

// bidiScroll turns the wheel for a wheel source's scroll action, with the
// pointer at (x, y) in the viewport, deltaY pixels. The pointer goes back
// where it was.
func (s *Server) bidiScroll(srcType string, action bidiAction) error {
	if srcType != "wheel" {
		return fmt.Errorf("scroll is an action of a wheel source, not %s", srcType)
	}
	if o, ok := action.Origin.(string); action.Origin != nil && (!ok || o != "viewport") {
		return errors.New(`scroll: only the "viewport" origin is supported`)
	}
	if action.DeltaX != 0 {
		return errors.New("scroll: only vertical scrolling is supported (deltaX must be 0)")
	}
	notches := s.bidiNotches(action.DeltaY, float64(s.viewportSize()["height"]))
	if notches == 0 {
		return nil
	}
	x, y := s.bidiToHID(action.X, action.Y)
	if at, ok := s.HID.(interface{ ScrollAt(x, y, amount int) error }); ok {
		return at.ScrollAt(x, y, notches)
	}
	if err := s.HID.MouseMove(x, y); err != nil {
		return err
	}
	return s.HID.MouseScroll(notches)
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

	// BiDi coords are relative to the cropped viewport: where that point
	// is in the full capture.
	fullX := float64(crop.Min.X) + pixelX
	fullY := float64(crop.Min.Y) + pixelY

	// The range spans what the target's absolute pointer spans. For a
	// target addressing its own display (iOS; see Platform.PointerSpace)
	// that is its picture, not the whole frame -- mapped across the frame,
	// every tap on an iPhone landed squeezed toward the middle of its screen.
	area := image.Rect(0, 0, int(captureW), int(captureH))
	if s.Platform.PointerSpace() == "screen" {
		area = s.Source.ContentRect()
		if area.Empty() {
			area = crop
		}
	}
	hidX := int(math.Round((fullX - float64(area.Min.X)) / float64(area.Dx()) * 32767))
	hidY := int(math.Round((fullY - float64(area.Min.Y)) / float64(area.Dy()) * 32767))

	return hid.ClampInt(hidX, 0, 32767), hid.ClampInt(hidY, 0, 32767)
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

func (s *Server) activeContacts(sess *bidiSession) []hid.TouchContact {
	// Caller must hold bidiSessionMu.
	var contacts []hid.TouchContact
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
	// A printable character: its key on a US keyboard, the table the HID
	// backends type with.
	if c, ok := printableKey(value); ok {
		return int(hid.USASCII[c] & 0x7f), true
	}
	return 0, false
}

// bidiKeyNeedsShift is whether a character is typed with Shift on a US
// keyboard: capitals, and the symbols above the digits and punctuation.
func bidiKeyNeedsShift(value string) bool {
	c, ok := printableKey(value)
	return ok && hid.USASCII[c]&0x80 != 0
}

func printableKey(value string) (byte, bool) {
	if utf8.RuneCountInString(value) != 1 {
		return 0, false
	}
	r := []rune(value)[0]
	if r < 0x20 || r >= 0x7f || hid.USASCII[r] == 0 {
		return 0, false
	}
	return byte(r), true
}

func shiftHeld(keys []int) bool {
	for _, k := range keys {
		if k == 0xE1 || k == 0xE5 { // left or right Shift
			return true
		}
	}
	return false
}

func removeKey(keys []int, keycode int) []int {
	for i, k := range keys {
		if k == keycode {
			return append(keys[:i], keys[i+1:]...)
		}
	}
	return keys
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
