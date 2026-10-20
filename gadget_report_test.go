package main

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"
)

// hidField is one Input field of a parsed report descriptor.
type hidField struct {
	page, usage uint32 // first usage of the field
	offset      int    // bit offset in the report
	size, count int
	constant    bool
}

// parseHIDDescriptor walks a report descriptor's short items and lays out
// its Input report, which is all the gadget sends. Enough of the HID spec
// for these three descriptors; it fails the test on anything it doesn't
// handle rather than guessing.
func parseHIDDescriptor(t *testing.T, desc []byte) (fields []hidField, inputBits int, reportIDs int) {
	t.Helper()
	var page, size, count uint32
	var usages []uint32
	var usageMin uint32
	haveMin := false
	for i := 0; i < len(desc); {
		prefix := desc[i]
		n := int(prefix & 3)
		if n == 3 {
			n = 4
		}
		if i+1+n > len(desc) {
			t.Fatalf("item at %d runs past the end", i)
		}
		var data uint32
		for j := 0; j < n; j++ {
			data |= uint32(desc[i+1+j]) << (8 * j)
		}
		switch prefix & 0xFC {
		case 0x04: // Usage Page
			page = data
		case 0x74: // Report Size
			size = data
		case 0x94: // Report Count
			count = data
		case 0x84: // Report ID
			reportIDs++
		case 0x14, 0x24, 0x34, 0x44, 0x54, 0x64: // logical/physical, unit
		case 0x08: // Usage
			usages = append(usages, data)
		case 0x18: // Usage Minimum
			usageMin, haveMin = data, true
		case 0x28: // Usage Maximum
		case 0x80: // Input
			f := hidField{page: page, offset: inputBits, size: int(size), count: int(count), constant: data&1 != 0}
			switch {
			case len(usages) > 0:
				f.usage = usages[0]
			case haveMin:
				f.usage = usageMin
			}
			fields = append(fields, f)
			inputBits += int(size * count)
			usages, haveMin = nil, false
		case 0x90, 0xB0: // Output, Feature: not part of the input report
			usages, haveMin = nil, false
		case 0xA0, 0xC0: // Collection, End Collection
			usages, haveMin = nil, false
		default:
			t.Fatalf("unhandled HID item 0x%02x at %d", prefix, i)
		}
		i += 1 + n
	}
	return fields, inputBits, reportIDs
}

func loadDesc(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("gadget/hid/" + name + ".desc")
	if err != nil {
		t.Fatal(err)
	}
	var hex strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		hex.WriteString(strings.Join(strings.Fields(line), ""))
	}
	s := hex.String()
	if len(s)%2 != 0 {
		t.Fatalf("%s.desc: odd number of hex digits", name)
	}
	out := make([]byte, len(s)/2)
	for i := range out {
		v, err := strconv.ParseUint(s[2*i:2*i+2], 16, 8)
		if err != nil {
			t.Fatalf("%s.desc: %v", name, err)
		}
		out[i] = byte(v)
	}
	return out
}

func findField(t *testing.T, fields []hidField, page, usage uint32, nth int) hidField {
	t.Helper()
	for _, f := range fields {
		if f.page == page && f.usage == usage && !f.constant {
			if nth == 0 {
				return f
			}
			nth--
		}
	}
	t.Fatalf("no input field for usage page 0x%x usage 0x%x", page, usage)
	return hidField{}
}

// TestGadgetDescriptors parses the descriptors roadie-gadget writes and
// checks that each report the encoder builds puts every field exactly where
// the descriptor says it is. The descriptor files and the Go encoder are
// two statements of one format; this is what keeps them one.
func TestGadgetDescriptors(t *testing.T) {
	t.Run("keyboard", func(t *testing.T) {
		fields, bits, ids := parseHIDDescriptor(t, loadDesc(t, "keyboard"))
		if ids != 0 {
			t.Errorf("descriptor has %d report IDs; a separate-interface boot keyboard must have none", ids)
		}
		if bits != keyboardReportLen*8 {
			t.Fatalf("input report is %d bits, encoder sends %d bytes", bits, keyboardReportLen)
		}
		mods := findField(t, fields, 0x07, 0xE0, 0)
		keys := findField(t, fields, 0x07, 0x00, 0)
		if mods.offset != 0 || mods.size*mods.count != 8 || keys.offset != 16 || keys.count != 6 || keys.size != 8 {
			t.Errorf("layout: modifiers %+v, keys %+v; want modifiers at byte 0, six keys from byte 2", mods, keys)
		}
		var k keyboardState
		k.press(0xE1)
		k.press(0x04)
		if r := k.report(); r[0] != 0x02 || r[2] != 0x04 {
			t.Errorf("shift+a encodes as % x", r)
		}
	})

	t.Run("mouse", func(t *testing.T) {
		fields, bits, ids := parseHIDDescriptor(t, loadDesc(t, "mouse"))
		if ids != 0 {
			t.Errorf("descriptor has %d report IDs, want none", ids)
		}
		if bits != mouseReportLen*8 {
			t.Fatalf("input report is %d bits, encoder sends %d bytes", bits, mouseReportLen)
		}
		buttons := findField(t, fields, 0x09, 0x01, 0)
		xy := findField(t, fields, 0x01, 0x30, 0)
		wheel := findField(t, fields, 0x01, 0x38, 0)
		if buttons.offset != 0 || xy.offset != 8 || xy.size != 16 || xy.count != 2 || wheel.offset != 40 || wheel.size != 8 {
			t.Errorf("layout: buttons %+v, x/y %+v, wheel %+v", buttons, xy, wheel)
		}
		r := mouseReport(1, 0x1234, 0x0567, -3)
		if !bytes.Equal(r, []byte{1, 0x34, 0x12, 0x67, 0x05, 0xFD}) {
			t.Errorf("mouse report % x", r)
		}
	})

	t.Run("touch", func(t *testing.T) {
		fields, bits, ids := parseHIDDescriptor(t, loadDesc(t, "touch"))
		if ids != 0 {
			t.Errorf("descriptor has %d report IDs, want none", ids)
		}
		if bits != touchReportLen*8 {
			t.Fatalf("input report is %d bits, encoder sends %d bytes", bits, touchReportLen)
		}
		for c := 0; c < 2; c++ {
			id := findField(t, fields, 0x0D, 0x51, c)
			tip := findField(t, fields, 0x0D, 0x42, c)
			x := findField(t, fields, 0x01, 0x30, c)
			y := findField(t, fields, 0x01, 0x31, c)
			base := c * 48
			if id.offset != base || tip.offset != base+8 || x.offset != base+16 || y.offset != base+32 || x.size != 16 {
				t.Errorf("contact %d layout: id %+v tip %+v x %+v y %+v", c, id, tip, x, y)
			}
		}
		count := findField(t, fields, 0x0D, 0x54, 0)
		if count.offset != 96 {
			t.Errorf("contact count at bit %d, want 96", count.offset)
		}
		r, err := touchReport([]TouchContact{{ID: 1, Tip: true, X: 0x0102, Y: 0x0304}})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(r, []byte{0, 0, 0, 0, 0, 0, 1, 1, 0x02, 0x01, 0x04, 0x03, 1}) {
			t.Errorf("touch report % x", r)
		}
	})
}

// TestTouchDescriptorMatchesBoard keeps gadget/hid/touch.desc the HID
// board's digitizer: the descriptor already proven on phones.
func TestTouchDescriptorMatchesBoard(t *testing.T) {
	src, err := os.ReadFile("board/hid/digitizer.py")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "_REPORT_DESCRIPTOR = bytes((")
	end := strings.Index(body[start:], "))")
	if start < 0 || end < 0 {
		t.Fatal("cannot find _REPORT_DESCRIPTOR in digitizer.py")
	}
	var board []byte
	for _, line := range strings.Split(body[start:start+end], "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		for _, tok := range strings.Split(line, ",") {
			tok = strings.TrimSpace(tok)
			switch {
			case tok == "REPORT_ID":
				board = append(board, 12)
			case strings.HasPrefix(tok, "0x"):
				v, err := strconv.ParseUint(tok[2:], 16, 8)
				if err != nil {
					t.Fatal(err)
				}
				board = append(board, byte(v))
			}
		}
	}
	i := bytes.Index(board, []byte{0x85, 12})
	if i < 0 {
		t.Fatal("digitizer.py has no Report ID item")
	}
	board = append(board[:i:i], board[i+2:]...)
	if got := loadDesc(t, "touch"); !bytes.Equal(got, board) {
		t.Errorf("gadget/hid/touch.desc differs from board/hid/digitizer.py (less its report ID)")
	}
}

func TestKeyboardState(t *testing.T) {
	var k keyboardState
	for _, code := range []int{0xE0, 0xE3, 4, 5, 4} {
		if err := k.press(code); err != nil {
			t.Fatal(err)
		}
	}
	if r := k.report(); !bytes.Equal(r, []byte{0x09, 0, 4, 5, 0, 0, 0, 0}) {
		t.Errorf("ctrl+gui+a+b = % x", r)
	}
	k.release(4)
	k.release(0xE0)
	if r := k.report(); !bytes.Equal(r, []byte{0x08, 0, 0, 5, 0, 0, 0, 0}) {
		t.Errorf("after releasing ctrl and a: % x", r)
	}
	for code := 6; code < 11; code++ {
		if err := k.press(code); err != nil {
			t.Fatal(err)
		}
	}
	if err := k.press(11); err != errSixKeys {
		t.Errorf("seventh key: err = %v, want errSixKeys", err)
	}
	if err := k.press(300); err == nil {
		t.Error("keycode 300 accepted")
	}
}

func TestTypeReports(t *testing.T) {
	reports, err := typeReports(keyboardState{}, "aA\n")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{
		{0, 0, 0x04, 0, 0, 0, 0, 0}, {0, 0, 0, 0, 0, 0, 0, 0}, // a
		{0x02, 0, 0, 0, 0, 0, 0, 0}, {0x02, 0, 0x04, 0, 0, 0, 0, 0}, {0, 0, 0, 0, 0, 0, 0, 0}, // shift, A, up
		{0, 0, 0x28, 0, 0, 0, 0, 0}, {0, 0, 0, 0, 0, 0, 0, 0}, // enter
	}
	if len(reports) != len(want) {
		t.Fatalf("%d reports, want %d", len(reports), len(want))
	}
	for i := range want {
		if !bytes.Equal(reports[i], want[i]) {
			t.Errorf("report %d = % x, want % x", i, reports[i], want[i])
		}
	}

	// A held key rides along on the first character, then is let go, as
	// release_all does on the HID board.
	var held keyboardState
	held.press(0xE0)
	reports, _ = typeReports(held, "c")
	if reports[0][0] != 0x01 || reports[len(reports)-1][0] != 0 {
		t.Errorf("ctrl held through 'c': first % x, last % x", reports[0], reports[len(reports)-1])
	}

	for _, bad := range []string{"héllo", "a\x01", "日本"} {
		if _, err := typeReports(keyboardState{}, bad); err == nil {
			t.Errorf("typeReports(%q) accepted", bad)
		}
	}

	// Every printable ASCII character has a key.
	for c := 0x20; c < 0x7f; c++ {
		if _, err := typeReports(keyboardState{}, string(rune(c))); err != nil {
			t.Errorf("%q: %v", rune(c), err)
		}
	}
}

func TestMouseReports(t *testing.T) {
	var m mouseState
	reports, m := m.moveReports(100, 200)
	if len(reports) != 1 || !bytes.Equal(reports[0], []byte{0, 100, 0, 200, 0, 0}) {
		t.Fatalf("first move: % x", reports)
	}
	// The same position again is nudged a unit aside first.
	reports, m = m.moveReports(100, 200)
	if len(reports) != 2 || reports[0][1] != 99 || reports[1][1] != 100 {
		t.Errorf("repeat move: % x", reports)
	}
	var edge mouseState
	_, edge = edge.moveReports(0, 0)
	reports, _ = edge.moveReports(0, 0)
	if len(reports) != 2 || reports[0][1] != 1 {
		t.Errorf("repeat at the left edge should nudge right: % x", reports)
	}

	_, m = m.moveReports(-5, 99999)
	if m.x != 0 || m.y != hidCoordMax {
		t.Errorf("clamp: %d,%d", m.x, m.y)
	}

	scroll := m.scrollReports(300) // down 300 = wheel -300
	if len(scroll) != 3 || int8(scroll[0][5]) != -127 || int8(scroll[1][5]) != -127 || int8(scroll[2][5]) != -46 {
		t.Errorf("scroll 300: % x", scroll)
	}
	if up := m.scrollReports(-2); len(up) != 1 || int8(up[0][5]) != 2 {
		t.Errorf("scroll -2: % x", up)
	}
	if none := m.scrollReports(0); len(none) != 0 {
		t.Errorf("scroll 0 sent %d reports", len(none))
	}
}

func TestTouchReport(t *testing.T) {
	r, _ := touchReport(nil)
	if !bytes.Equal(r, []byte{0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0}) {
		t.Errorf("no contacts: % x (slot ids must stay 0 and 1)", r)
	}
	r, _ = touchReport([]TouchContact{{ID: 0, Tip: false, X: 40000, Y: -3}})
	if r[1] != 0 || r[2] != 0xFF || r[3] != 0x7F || r[4] != 0 || r[12] != 1 {
		t.Errorf("lifting contact, clamped: % x", r)
	}
	for _, bad := range [][]TouchContact{
		{{ID: 2}},
		{{ID: -1}},
		{{ID: 0}, {ID: 1}, {ID: 0}},
	} {
		if _, err := touchReport(bad); err == nil {
			t.Errorf("touchReport(%+v) accepted", bad)
		}
	}
}
