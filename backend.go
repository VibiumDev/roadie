package main

import (
	"fmt"
	"strings"
)

// HIDBackend turns Roadie's input commands into USB input on the target.
//
// It is exactly the set of methods the server, the BiDi endpoint and main
// already called on *HIDController, so the RP2040 relay and the Linux USB
// gadget are interchangeable behind it without any handler, page or
// WebSocket message changing.
type HIDBackend interface {
	// Run maintains the connection until Shutdown; call it in a goroutine.
	Run()
	// Shutdown lets go of everything held and stops Run.
	Shutdown()
	// Backend names the implementation: "rp2040" or "linux-gadget".
	Backend() string

	// Status is the link from this server to the input devices.
	Status() HIDStatus
	// BoardResponding is whether the far end is taking input: the HID board
	// answering the relay, or the phone having enumerated the gadget.
	BoardResponding() bool
	// Port and Serial identify what Status refers to, for /api/hid/status.
	Port() string
	Serial() string

	Type(text string) error
	KeyPress(keycode int) error
	KeyRelease(keycode int) error
	MouseMove(x, y int) error
	MouseClick(buttons int) error
	MousePress(buttons int) error
	MouseRelease(buttons int) error
	MouseScroll(amount int) error
	Touch(contacts []TouchContact) error

	// SendAudio plays 16 kHz S16LE mono PCM into the target's USB microphone.
	SendAudio(pcm []byte) error

	// ResetHID makes the target see the input device unplugged and plugged
	// back in. ResetRelay restarts the link on this side without that.
	ResetHID() error
	ResetRelay() error
}

const (
	BackendRP2040      = "rp2040"
	BackendLinuxGadget = "linux-gadget"
)

// ParseHIDBackend normalizes --hid-backend. Empty means the RP2040 boards,
// which is what Roadie has always used.
func ParseHIDBackend(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", BackendRP2040:
		return BackendRP2040, nil
	case BackendLinuxGadget, "gadget":
		return BackendLinuxGadget, nil
	}
	return "", fmt.Errorf("unknown --hid-backend %q, expected %s or %s", s, BackendRP2040, BackendLinuxGadget)
}

// Backend reports that the relay boards are in use.
func (hc *HIDController) Backend() string { return BackendRP2040 }
