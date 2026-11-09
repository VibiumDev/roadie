package main

import (
	"bytes"
	"fmt"

	"github.com/VibiumDev/roadie/internal/hid"
)

// Fakes standing in for the input backend in the server's tests. The hid
// package keeps its own; a test file cannot be imported, so these are
// copies.

// keyHID records key presses and releases; the rest of HIDBackend is inert.
type keyHID struct{ log []string }

func (k *keyHID) Run()                           {}
func (k *keyHID) Shutdown()                      {}
func (k *keyHID) Backend() string                { return "test" }
func (k *keyHID) Status() hid.HIDStatus          { return hid.HIDConnected }
func (k *keyHID) BoardResponding() bool          { return true }
func (k *keyHID) Port() string                   { return "" }
func (k *keyHID) Serial() string                 { return "" }
func (k *keyHID) Type(string) error              { return nil }
func (k *keyHID) MouseMove(int, int) error       { return nil }
func (k *keyHID) MouseClick(int) error           { return nil }
func (k *keyHID) MousePress(int) error           { return nil }
func (k *keyHID) MouseRelease(int) error         { return nil }
func (k *keyHID) MouseScroll(int) error          { return nil }
func (k *keyHID) Touch([]hid.TouchContact) error { return nil }
func (k *keyHID) SendAudio([]byte) error         { return nil }
func (k *keyHID) ResetHID() error                { return nil }
func (k *keyHID) ResetRelay() error              { return nil }
func (k *keyHID) KeyPress(c int) error           { k.log = append(k.log, fmt.Sprintf("+%d", c)); return nil }
func (k *keyHID) KeyRelease(c int) error         { k.log = append(k.log, fmt.Sprintf("-%d", c)); return nil }

// wheelHID records pointer moves and wheel turns.
type wheelHID struct {
	keyHID
	log []string
}

func (w *wheelHID) MouseMove(x, y int) error {
	w.log = append(w.log, fmt.Sprintf("move %d,%d", x, y))
	return nil
}

func (w *wheelHID) MouseScroll(n int) error {
	w.log = append(w.log, fmt.Sprintf("wheel %d", n))
	return nil
}

// pathHID records where the pointer was sent.
type pathHID struct {
	keyHID
	moves [][2]int
}

func (p *pathHID) MouseMove(x, y int) error { p.moves = append(p.moves, [2]int{x, y}); return nil }
func (p *pathHID) MousePress(int) error     { return nil }
func (p *pathHID) MouseRelease(int) error   { return nil }

// mockPort is a board that only listens.
type mockPort struct {
	buf bytes.Buffer
}

func (m *mockPort) Write(p []byte) (int, error) { return m.buf.Write(p) }
func (m *mockPort) Close() error                { return nil }

func newTestHID(mp *mockPort) *hid.HIDController { return hid.NewControllerWithPort(mp) }
