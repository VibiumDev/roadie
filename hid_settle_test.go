package main

import (
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
