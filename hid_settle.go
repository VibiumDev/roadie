package main

import (
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
type settledHID struct {
	HIDBackend

	mu       sync.Mutex
	moved    time.Time
	released time.Time
	now      func() time.Time
	sleep    func(time.Duration)
}

func newSettledHID(b HIDBackend) *settledHID {
	return &settledHID{HIDBackend: b, now: time.Now, sleep: time.Sleep}
}

func (h *settledHID) MouseMove(x, y int) error {
	h.wait(&h.released, releaseSettle)
	err := h.HIDBackend.MouseMove(x, y)
	h.mark(&h.moved)
	return err
}

func (h *settledHID) MousePress(buttons int) error {
	h.wait(&h.moved, pressSettle)
	return h.HIDBackend.MousePress(buttons)
}

func (h *settledHID) MouseRelease(buttons int) error {
	err := h.HIDBackend.MouseRelease(buttons)
	h.mark(&h.released)
	return err
}

func (h *settledHID) MouseClick(buttons int) error {
	h.wait(&h.moved, pressSettle)
	err := h.HIDBackend.MouseClick(buttons)
	h.mark(&h.released)
	return err
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
