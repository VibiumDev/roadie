package main

import (
	"errors"
	"math"
	"sync"
	"time"
)

// iOS takes pointer input in frames of about 16 ms, and a click needs time
// on both sides of it there:
//
//   - pressSettle before the press. A press in the same frame as a jump to
//     a new spot isn't applied until the next input arrives, and within
//     about 10 ms of the jump it can be lost: a click on a Calculator button
//     showed up only when the next click came. 20 ms was enough.
//   - releaseSettle after the release. iOS holds every tap about 100 ms, and
//     a move inside that window drags the tap, so one that jumps to another
//     button cancels it. Clicks 50 ms apart lost the first of them; 100 ms
//     apart, none.
const (
	pressSettle   = 50 * time.Millisecond
	releaseSettle = 150 * time.Millisecond
)

// settledHID gives every click that time: a press waits until the last move
// is pressSettle old, and the first move after a release waits until the
// release is releaseSettle old. Moves with the button held, as in a drag,
// go straight through. Every input path (REST, the viewer, BiDi) comes
// through here, so a client that clicks back to back gets each click.
//
// With glide on, a jump from rest glides there instead (see glidePath).
//
// With touchAsPointer it also delivers touch as the pointer, for a target
// that takes no touchscreen (iOS): a finger down is the button pressed
// there, moving it drags, and lifting it releases. Taps, long presses and
// swipes then work as on a touchscreen, and get the same settling; two
// fingers at once can't.
type settledHID struct {
	HIDBackend
	touchAsPointer bool

	mu       sync.Mutex
	moved    time.Time
	released time.Time
	input    time.Time // the last input of any kind, for screenshots
	x, y     int       // where the pointer is, once placed
	placed   bool
	held     int  // buttons down
	glide    bool // jumps glide (see glidePath)
	now      func() time.Time
	sleep    func(time.Duration)

	touchMu    sync.Mutex // one touch translated at a time
	fingerDown bool
}

// errOneFinger is what a two-finger gesture gets on a target whose only
// pointer stands in for touch.
var errOneFinger = errors.New("this device takes touch as its pointer, so one finger at a time: two-finger gestures aren't possible")

func newSettledHID(b HIDBackend) *settledHID {
	return &settledHID{HIDBackend: b, now: time.Now, sleep: time.Sleep}
}

func (h *settledHID) MouseMove(x, y int) error { return h.moveTo(x, y, true) }

// Glide reports whether jumps glide; SetGlide turns that on or off.
func (h *settledHID) Glide() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.glide
}

func (h *settledHID) SetGlide(on bool) {
	h.mu.Lock()
	h.glide = on
	h.mu.Unlock()
}

// moveTo moves the pointer, gliding if it may and glide is on.
func (h *settledHID) moveTo(x, y int, mayGlide bool) error {
	h.wait(&h.released, releaseSettle)
	path := [][2]int{{x, y}}
	if mayGlide {
		path = h.route(x, y)
	}
	var err error
	for i, p := range path {
		if i > 0 {
			h.sleep(glideFrame)
		}
		if err = h.HIDBackend.MouseMove(p[0], p[1]); err != nil {
			break
		}
	}
	h.mu.Lock()
	h.x, h.y, h.placed = x, y, err == nil
	h.mu.Unlock()
	h.mark(&h.moved, &h.input)
	return err
}

// route is the way to (x, y): a glide from rest, or else straight there.
func (h *settledHID) route(x, y int) [][2]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.glide || !h.placed || h.held != 0 || h.now().Sub(h.moved) < glideRest ||
		math.Hypot(float64(x-h.x), float64(y-h.y)) < glideMin {
		return [][2]int{{x, y}}
	}
	return glidePath(h.x, h.y, x, y)
}

func (h *settledHID) MousePress(buttons int) error {
	h.wait(&h.moved, pressSettle)
	err := h.HIDBackend.MousePress(buttons)
	h.mu.Lock()
	h.held |= buttons
	h.mu.Unlock()
	h.mark(&h.input)
	return err
}

func (h *settledHID) MouseRelease(buttons int) error {
	err := h.HIDBackend.MouseRelease(buttons)
	h.mu.Lock()
	h.held &^= buttons
	h.mu.Unlock()
	h.mark(&h.released, &h.input)
	return err
}

func (h *settledHID) MouseClick(buttons int) error {
	h.wait(&h.moved, pressSettle)
	err := h.HIDBackend.MouseClick(buttons)
	h.mark(&h.released, &h.input)
	return err
}

func (h *settledHID) MouseScroll(amount int) error {
	err := h.HIDBackend.MouseScroll(amount)
	h.mark(&h.input)
	return err
}

func (h *settledHID) Type(text string) error {
	err := h.HIDBackend.Type(text)
	h.mark(&h.input)
	return err
}

func (h *settledHID) KeyPress(keycode int) error {
	err := h.HIDBackend.KeyPress(keycode)
	h.mark(&h.input)
	return err
}

func (h *settledHID) KeyRelease(keycode int) error {
	err := h.HIDBackend.KeyRelease(keycode)
	h.mark(&h.input)
	return err
}

func (h *settledHID) Touch(contacts []TouchContact) error {
	if h.touchAsPointer {
		return h.touchWithPointer(contacts)
	}
	err := h.HIDBackend.Touch(contacts)
	h.mark(&h.input)
	return err
}

// touchWithPointer moves the pointer to the finger and holds the button
// while the finger is down. Contacts are every finger now on the screen.
func (h *settledHID) touchWithPointer(contacts []TouchContact) error {
	var tips []TouchContact
	for _, c := range contacts {
		if c.Tip {
			tips = append(tips, c)
		}
	}
	h.touchMu.Lock()
	defer h.touchMu.Unlock()
	switch len(tips) {
	case 0: // lifted
		if !h.fingerDown {
			return nil
		}
		h.fingerDown = false
		return h.MouseRelease(1)
	case 1:
		// A finger lands where it lands: no glide.
		if err := h.moveTo(tips[0].X, tips[0].Y, false); err != nil {
			return err
		}
		if h.fingerDown {
			return nil
		}
		if err := h.MousePress(1); err != nil {
			return err
		}
		h.fingerDown = true
		return nil
	}
	return errOneFinger
}

// LastInput is when input last went to the target, so a screenshot can
// wait for the screen to show it.
func (h *settledHID) LastInput() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.input
}

// wait sleeps until the event at *at is d old.
func (h *settledHID) wait(at *time.Time, d time.Duration) {
	h.mu.Lock()
	since := h.now().Sub(*at)
	h.mu.Unlock()
	if since < d {
		h.sleep(d - since)
	}
}

func (h *settledHID) mark(at ...*time.Time) {
	h.mu.Lock()
	now := h.now()
	for _, t := range at {
		*t = now
	}
	h.mu.Unlock()
}

// USBState passes the gadget's USB state through for /api/hid/status.
func (h *settledHID) USBState() string {
	if g, ok := h.HIDBackend.(interface{ USBState() string }); ok {
		return g.USBState()
	}
	return ""
}
