package main

import (
	"errors"
	"fmt"
)

// HID report encoding for the linux-gadget backend. The layouts are fixed by
// the descriptors in gadget/hid/*.desc, which are the RP2040 HID board's own
// descriptors without report IDs; TestGadgetDescriptors parses those files
// and checks them against the lengths here.
const (
	keyboardReportLen = 8  // modifiers, reserved, six keys
	mouseReportLen    = 6  // buttons, x, y, wheel
	touchReportLen    = 13 // two contacts of (id, tip, x, y), contact count

	hidCoordMax   = 32767
	mouseButtons  = 0x1F // five buttons in the descriptor
	maxContacts   = 2
	shiftKeycode  = 0xE1 // Left Shift
	firstModifier = 0xE0
	lastModifier  = 0xE7
)

// keyboardState is what a boot keyboard report carries: a modifier bitmap
// and up to six other keys.
type keyboardState struct {
	mods byte
	keys [6]byte
}

var errSixKeys = errors.New("six keys are already held; release one first")

// press adds a key, as adafruit_hid's Keyboard.press does on the HID board:
// modifiers set a bit, anything else takes a free slot, and a key already
// held is left alone.
func (k *keyboardState) press(code int) error {
	if code < 0 || code > 0xFF {
		return fmt.Errorf("keycode %d is out of range (0-255)", code)
	}
	if code >= firstModifier && code <= lastModifier {
		k.mods |= 1 << (code - firstModifier)
		return nil
	}
	if code == 0 {
		return nil
	}
	for _, c := range k.keys {
		if c == byte(code) {
			return nil
		}
	}
	for i, c := range k.keys {
		if c == 0 {
			k.keys[i] = byte(code)
			return nil
		}
	}
	return errSixKeys
}

func (k *keyboardState) release(code int) {
	if code >= firstModifier && code <= lastModifier {
		k.mods &^= 1 << (code - firstModifier)
		return
	}
	for i, c := range k.keys {
		if c == byte(code) && code != 0 {
			k.keys[i] = 0
		}
	}
}

func (k keyboardState) held() bool {
	return k != keyboardState{}
}

func (k keyboardState) report() []byte {
	r := make([]byte, keyboardReportLen)
	r[0] = k.mods
	copy(r[2:], k.keys[:])
	return r
}

// usASCII maps 7-bit ASCII to US keyboard usage codes, with 0x80 meaning
// "hold shift". It is adafruit_hid's KeyboardLayoutUS.ASCII_TO_KEYCODE (MIT),
// the table the RP2040 HID board types with, so text comes out the same on
// either backend. Zero means no key: such characters cannot be typed.
var usASCII = [128]byte{
	'\b': 0x2a, '\t': 0x2b, '\n': 0x28, 0x1b: 0x29, ' ': 0x2c,
	'!': 0x9e, '"': 0xb4, '#': 0xa0, '$': 0xa1, '%': 0xa2, '&': 0xa4,
	'\'': 0x34, '(': 0xa6, ')': 0xa7, '*': 0xa5, '+': 0xae, ',': 0x36,
	'-': 0x2d, '.': 0x37, '/': 0x38,
	'0': 0x27, '1': 0x1e, '2': 0x1f, '3': 0x20, '4': 0x21, '5': 0x22,
	'6': 0x23, '7': 0x24, '8': 0x25, '9': 0x26,
	':': 0xb3, ';': 0x33, '<': 0xb6, '=': 0x2e, '>': 0xb7, '?': 0xb8,
	'@': 0x9f,
	'A': 0x84, 'B': 0x85, 'C': 0x86, 'D': 0x87, 'E': 0x88, 'F': 0x89,
	'G': 0x8a, 'H': 0x8b, 'I': 0x8c, 'J': 0x8d, 'K': 0x8e, 'L': 0x8f,
	'M': 0x90, 'N': 0x91, 'O': 0x92, 'P': 0x93, 'Q': 0x94, 'R': 0x95,
	'S': 0x96, 'T': 0x97, 'U': 0x98, 'V': 0x99, 'W': 0x9a, 'X': 0x9b,
	'Y': 0x9c, 'Z': 0x9d,
	'[': 0x2f, '\\': 0x31, ']': 0x30, '^': 0xa3, '_': 0xad, '`': 0x35,
	'a': 0x04, 'b': 0x05, 'c': 0x06, 'd': 0x07, 'e': 0x08, 'f': 0x09,
	'g': 0x0a, 'h': 0x0b, 'i': 0x0c, 'j': 0x0d, 'k': 0x0e, 'l': 0x0f,
	'm': 0x10, 'n': 0x11, 'o': 0x12, 'p': 0x13, 'q': 0x14, 'r': 0x15,
	's': 0x16, 't': 0x17, 'u': 0x18, 'v': 0x19, 'w': 0x1a, 'x': 0x1b,
	'y': 0x1c, 'z': 0x1d,
	'{': 0xaf, '|': 0xb1, '}': 0xb0, '~': 0xb5, 0x7f: 0x4c,
}

// typeReports returns the reports that type text, starting from the keys
// already held. Per character it does what KeyboardLayoutUS.write does on the
// HID board: shift down on its own, then the key, then release everything --
// including keys held by an earlier key_press. Text containing anything
// without a US key is rejected whole, before a single report is produced.
func typeReports(held keyboardState, text string) ([][]byte, error) {
	for i, r := range text {
		if r >= 128 || usASCII[r] == 0 {
			return nil, fmt.Errorf("cannot type %q (character %d): no key for it on a US keyboard", r, i)
		}
	}
	var out [][]byte
	for i := 0; i < len(text); i++ {
		code := usASCII[text[i]]
		k := held
		if code&0x80 != 0 {
			if err := k.press(shiftKeycode); err != nil {
				return nil, err
			}
			out = append(out, k.report())
		}
		if err := k.press(int(code & 0x7f)); err != nil {
			return nil, err
		}
		out = append(out, k.report())
		held = keyboardState{}
		out = append(out, held.report())
	}
	return out, nil
}

func clampCoord(v int) int {
	return min(max(v, 0), hidCoordMax)
}

func putU16(b []byte, v int) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}

// mouseState is the absolute mouse's buttons and position. placed is false
// until a position has been sent since the phone last enumerated us.
type mouseState struct {
	buttons byte
	x, y    int
	placed  bool
}

func mouseReport(buttons byte, x, y, wheel int) []byte {
	r := make([]byte, mouseReportLen)
	r[0] = buttons & mouseButtons
	putU16(r[1:], clampCoord(x))
	putU16(r[3:], clampCoord(y))
	r[5] = byte(int8(max(min(wheel, 127), -127)))
	return r
}

func (m mouseState) report() []byte {
	return mouseReport(m.buttons, m.x, m.y, 0)
}

// moveReports moves to (x, y). Linux's input core drops an absolute event
// equal to the last one, so a repeated position would go nowhere -- and the
// pointer is not necessarily still there, because the target moves it too.
// The HID board's fix, kept here: step one unit aside first. One unit is
// 1/32767 of the screen, under a pixel, so nothing visibly moves.
func (m mouseState) moveReports(x, y int) ([][]byte, mouseState) {
	next := m
	next.x, next.y, next.placed = clampCoord(x), clampCoord(y), true
	var out [][]byte
	if m.placed && m.x == next.x && m.y == next.y {
		aside := next.x - 1
		if next.x == 0 {
			aside = 1
		}
		out = append(out, mouseReport(next.buttons, aside, next.y, 0))
	}
	out = append(out, next.report())
	return out, next
}

// scrollReports turns the wheel. Positive amount scrolls down, which is a
// negative wheel value, in steps of at most 127 as the absolute_mouse
// library sends them.
func (m mouseState) scrollReports(amount int) [][]byte {
	var out [][]byte
	wheel := -amount
	for wheel != 0 {
		step := max(min(wheel, 127), -127)
		out = append(out, mouseReport(m.buttons, m.x, m.y, step))
		wheel -= step
	}
	return out
}

// touchReport encodes up to two contacts the way board/hid/digitizer.py
// does: both slots always carry their ids (0 and 1) so a lifted contact is
// identifiable, and the count is the number of contacts given.
func touchReport(contacts []TouchContact) ([]byte, error) {
	if len(contacts) > maxContacts {
		return nil, fmt.Errorf("%d contacts given; the digitizer has %d", len(contacts), maxContacts)
	}
	r := make([]byte, touchReportLen)
	r[0], r[6] = 0, 1
	for _, c := range contacts {
		if c.ID < 0 || c.ID >= maxContacts {
			return nil, fmt.Errorf("contact id %d; the digitizer's contacts are 0 and 1", c.ID)
		}
		off := c.ID * 6
		r[off] = byte(c.ID)
		if c.Tip {
			r[off+1] = 1
		}
		putU16(r[off+2:], clampCoord(c.X))
		putU16(r[off+4:], clampCoord(c.Y))
	}
	r[12] = byte(len(contacts))
	return r, nil
}
