package main

import (
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"
)

// mouseHID records mouse calls; the rest is keyHID's inert set.
type mouseHID struct {
	keyHID
	log []string
}

func (m *mouseHID) MouseMove(int, int) error { m.log = append(m.log, "move"); return nil }
func (m *mouseHID) MousePress(int) error     { m.log = append(m.log, "press"); return nil }
func (m *mouseHID) MouseClick(int) error     { m.log = append(m.log, "click"); return nil }
func (m *mouseHID) MouseRelease(int) error   { m.log = append(m.log, "release"); return nil }
func (m *mouseHID) Touch(c []TouchContact) error {
	m.log = append(m.log, fmt.Sprintf("touch %d", len(c)))
	return nil
}

// A press right after a move waits until the move is pressSettle old; one
// that comes later, or a release, goes straight through.
func TestPressWaitsForThePointerToSettle(t *testing.T) {
	inner := &mouseHID{}
	h := newSettledHID(inner)
	clock := time.Unix(1000, 0)
	var slept []time.Duration
	h.now = func() time.Time { return clock }
	h.sleep = func(d time.Duration) {
		slept = append(slept, d)
		inner.log = append(inner.log, "wait "+d.String())
		clock = clock.Add(d)
	}

	h.MouseMove(1, 2)
	h.MouseClick(1) // back to back: waits it all
	clock = clock.Add(time.Second)
	h.MouseMove(3, 4)
	clock = clock.Add(20 * time.Millisecond)
	h.MousePress(1) // 20 ms after the move: waits the rest
	h.MouseRelease(1)
	clock = clock.Add(time.Second)
	h.MouseClick(1) // long after the last move: no wait

	want := []string{"move", "wait 50ms", "click", "move", "wait 30ms", "press", "release", "click"}
	if !reflect.DeepEqual(inner.log, want) {
		t.Errorf("calls = %v, want %v", inner.log, want)
	}
}

// The first move after a release waits until the release is releaseSettle
// old; moves with the button held, as in a drag, don't wait.
func TestMoveWaitsForTheTapToFinish(t *testing.T) {
	inner := &mouseHID{}
	h := newSettledHID(inner)
	clock := time.Unix(1000, 0)
	h.now = func() time.Time { return clock }
	h.sleep = func(d time.Duration) {
		inner.log = append(inner.log, "wait "+d.String())
		clock = clock.Add(d)
	}

	h.MouseClick(1)
	h.MouseMove(1, 2) // right after a click: waits it all
	clock = clock.Add(time.Second)
	h.MousePress(1)
	h.MouseMove(3, 4) // dragging: no wait
	h.MouseMove(5, 6)
	clock = clock.Add(100 * time.Millisecond)
	h.MouseRelease(1)
	clock = clock.Add(100 * time.Millisecond)
	h.MouseMove(7, 8) // 100 ms after the release: waits the rest

	want := []string{"click", "wait 150ms", "move", "press", "move", "move", "release", "wait 50ms", "move"}
	if !reflect.DeepEqual(inner.log, want) {
		t.Errorf("calls = %v, want %v", inner.log, want)
	}
}

func TestSettledHIDPassesUSBStateThrough(t *testing.T) {
	if got := newSettledHID(&mouseHID{}).USBState(); got != "" {
		t.Errorf("relay backend: USBState = %q, want empty", got)
	}
	g := &usbStateHID{state: "configured"}
	var b HIDBackend = newSettledHID(g)
	s, ok := b.(interface{ USBState() string })
	if !ok || s.USBState() != "configured" {
		t.Errorf("gadget backend: USBState not passed through")
	}
}

type usbStateHID struct {
	keyHID
	state string
}

func (u *usbStateHID) USBState() string { return u.state }

// On a target that takes touch as its pointer, a finger down is a press
// there, moving it drags, lifting it releases, and a second finger is
// refused; elsewhere touch goes to the touchscreen as it is.
func TestTouchAsPointer(t *testing.T) {
	finger := func(tip bool, x, y int) []TouchContact {
		return []TouchContact{{ID: 0, Tip: tip, X: x, Y: y}}
	}
	inner := &mouseHID{}
	h := newSettledHID(inner)
	h.touchAsPointer = true
	clock := time.Unix(1000, 0)
	h.now = func() time.Time { return clock }
	h.sleep = func(d time.Duration) {
		inner.log = append(inner.log, "wait "+d.String())
		clock = clock.Add(d)
	}

	for _, c := range [][]TouchContact{
		finger(true, 10, 20),  // down: to the spot, then press
		finger(true, 30, 40),  // dragging
		finger(false, 30, 40), // lifted
		nil,                   // nothing down: nothing to do
	} {
		if err := h.Touch(c); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"move", "wait 50ms", "press", "move", "release"}
	if !reflect.DeepEqual(inner.log, want) {
		t.Errorf("one finger: %v, want %v", inner.log, want)
	}
	if h.LastInput().IsZero() {
		t.Error("touch didn't count as input for screenshots")
	}

	two := []TouchContact{{ID: 0, Tip: true}, {ID: 1, Tip: true, X: 5}}
	if err := h.Touch(two); err != errOneFinger {
		t.Errorf("two fingers: %v, want errOneFinger", err)
	}

	plain := &mouseHID{}
	if err := newSettledHID(plain).Touch(finger(true, 1, 2)); err != nil || !reflect.DeepEqual(plain.log, []string{"touch 1"}) {
		t.Errorf("a touchscreen target: %v %v", plain.log, err)
	}
}

// pathHID records where each move went.
type pathHID struct {
	keyHID
	moves [][2]int
}

func (p *pathHID) MouseMove(x, y int) error { p.moves = append(p.moves, [2]int{x, y}); return nil }
func (p *pathHID) MousePress(int) error     { return nil }
func (p *pathHID) MouseRelease(int) error   { return nil }

func clockedHID(inner HIDBackend) (h *settledHID, clock *time.Time, slept *time.Duration) {
	h = newSettledHID(inner)
	h.SetGlide(true)
	clock, slept = new(time.Time), new(time.Duration)
	*clock = time.Unix(1000, 0)
	h.now = func() time.Time { return *clock }
	h.sleep = func(d time.Duration) { *slept += d; *clock = clock.Add(d) }
	return h, clock, slept
}

// A jump from rest glides there along an arc, slow at both ends, one step
// per frame, and ends exactly where it was sent.
func TestAJumpGlides(t *testing.T) {
	inner := &pathHID{}
	h, clock, slept := clockedHID(inner)
	h.MouseMove(2000, 30000)
	*clock = clock.Add(time.Second)
	*slept = 0
	h.MouseMove(30000, 3000)

	path := inner.moves[1:]
	// Further than the screen is wide, so the longest glide: 300 ms.
	if want := int((glideBase + glideSpan) / glideFrame); len(path) != want {
		t.Fatalf("%d steps, want %d", len(path), want)
	}
	if *slept != time.Duration(len(path)-1)*glideFrame {
		t.Errorf("took %v for %d steps", *slept, len(path))
	}
	if last := path[len(path)-1]; last != [2]int{30000, 3000} {
		t.Errorf("ended at %v", last)
	}

	// Along the line from start to end, and how far off it.
	x0, y0, dx, dy := 2000.0, 30000.0, 28000.0, -27000.0
	dist := math.Hypot(dx, dy)
	along, off := make([]float64, len(path)), 0.0
	for i, p := range path {
		px, py := float64(p[0])-x0, float64(p[1])-y0
		along[i] = (px*dx + py*dy) / dist
		off = math.Max(off, math.Abs(px*dy-py*dx)/dist)
		if i > 0 && along[i] < along[i-1] {
			t.Errorf("step %d went backwards", i)
		}
	}
	if off < 0.05*dist {
		t.Errorf("at most %.0f off the straight line: no arc", off)
	}
	first, middle := along[0], along[len(path)/2]-along[len(path)/2-1]
	if first > middle/3 {
		t.Errorf("first step %.0f, middle step %.0f: not easing in", first, middle)
	}
}

// Short moves, moves with the button held, moves streamed less than
// glideRest apart, taps, the first move of all, and every move with glide
// off go straight there.
func TestWhatGoesStraight(t *testing.T) {
	inner := &pathHID{}
	h, clock, _ := clockedHID(inner)
	steps := func(x, y int) int {
		n := len(inner.moves)
		h.MouseMove(x, y)
		return len(inner.moves) - n
	}
	rest := func() { *clock = clock.Add(time.Second) }

	if n := steps(1000, 1000); n != 1 {
		t.Errorf("first move: %d steps", n)
	}
	rest()
	if n := steps(1000+glideMin/2, 1000); n != 1 {
		t.Errorf("short move: %d steps", n)
	}
	rest()
	if n := steps(20000, 20000); n < 2 {
		t.Errorf("a jump from rest went straight")
	}
	if n := steps(5000, 5000); n != 1 {
		t.Errorf("a move right after another: %d steps", n)
	}
	rest()
	h.MousePress(1)
	rest()
	if n := steps(30000, 30000); n != 1 {
		t.Errorf("a move with the button held: %d steps", n)
	}
	h.MouseRelease(1)
	rest()
	if n := steps(1000, 30000); n < 2 {
		t.Errorf("after the release, a jump went straight")
	}

	// A finger lands where it lands, even a long way from the last one.
	h.touchAsPointer = true
	rest()
	n := len(inner.moves)
	h.Touch([]TouchContact{{Tip: true, X: 30000, Y: 1000}})
	h.Touch(nil)
	if len(inner.moves)-n != 1 {
		t.Errorf("a tap took %d steps to land", len(inner.moves)-n)
	}

	h.SetGlide(false)
	rest()
	if n := steps(30000, 30000); n != 1 {
		t.Errorf("with glide off: %d steps", n)
	}
}

func TestGlideIsOffUnlessTurnedOn(t *testing.T) {
	if newSettledHID(&pathHID{}).Glide() {
		t.Error("glide is on by default")
	}
}
