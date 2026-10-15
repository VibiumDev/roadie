package main

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type mockPort struct {
	buf bytes.Buffer
}

func (m *mockPort) Write(p []byte) (int, error) { return m.buf.Write(p) }
func (m *mockPort) Close() error                { return nil }

func (m *mockPort) lines() []string {
	raw := m.buf.String()
	if raw == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
}

func newTestHID(mp *mockPort) *HIDController {
	hc := NewHIDController("")
	hc.port = mp
	hc.status.Store(HIDConnected)
	return hc
}

func TestMouseMove(t *testing.T) {
	mp := &mockPort{}
	hc := newTestHID(mp)
	defer hc.Shutdown()

	if err := hc.MouseMove(16383, 16383); err != nil {
		t.Fatal(err)
	}

	// MouseMove is async (coalesced) — wait for drain.
	time.Sleep(100 * time.Millisecond)

	lines := mp.lines()
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var cmd map[string]any
	json.Unmarshal([]byte(lines[0]), &cmd)
	if cmd["cmd"] != "mouse_move" {
		t.Errorf("expected mouse_move, got %v", cmd["cmd"])
	}
	if int(cmd["x"].(float64)) != 16383 {
		t.Errorf("expected x=16383, got %v", cmd["x"])
	}
	if int(cmd["y"].(float64)) != 16383 {
		t.Errorf("expected y=16383, got %v", cmd["y"])
	}
}

func TestTypeShortText(t *testing.T) {
	mp := &mockPort{}
	hc := newTestHID(mp)
	defer hc.Shutdown()

	if err := hc.Type("hello"); err != nil {
		t.Fatal(err)
	}

	lines := mp.lines()
	if len(lines) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(lines))
	}

	var cmd map[string]any
	json.Unmarshal([]byte(lines[0]), &cmd)
	if cmd["text"] != "hello" {
		t.Errorf("expected 'hello', got %v", cmd["text"])
	}
}

func TestTypeChunking(t *testing.T) {
	mp := &mockPort{}
	hc := newTestHID(mp)
	defer hc.Shutdown()

	// 60 chars = 3 chunks (29 + 29 + 2)
	text := strings.Repeat("a", 60)
	if err := hc.Type(text); err != nil {
		t.Fatal(err)
	}

	lines := mp.lines()
	if len(lines) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(lines))
	}

	for i, line := range lines {
		var cmd map[string]any
		json.Unmarshal([]byte(line), &cmd)
		chunk := cmd["text"].(string)
		switch i {
		case 0, 1:
			if len(chunk) != 29 {
				t.Errorf("chunk %d: expected 29 chars, got %d", i, len(chunk))
			}
		case 2:
			if len(chunk) != 2 {
				t.Errorf("chunk 2: expected 2 chars, got %d", len(chunk))
			}
		}
	}
}

func TestKeyPressRelease(t *testing.T) {
	mp := &mockPort{}
	hc := newTestHID(mp)
	defer hc.Shutdown()

	hc.KeyPress(4)
	hc.KeyRelease(4)

	lines := mp.lines()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var press, release map[string]any
	json.Unmarshal([]byte(lines[0]), &press)
	json.Unmarshal([]byte(lines[1]), &release)

	if press["cmd"] != "key_press" {
		t.Errorf("expected key_press, got %v", press["cmd"])
	}
	if release["cmd"] != "key_release" {
		t.Errorf("expected key_release, got %v", release["cmd"])
	}
}

func TestStatusDisconnected(t *testing.T) {
	hc := NewHIDController("")
	defer hc.Shutdown()

	if hc.Status() != HIDDisconnected {
		t.Errorf("expected disconnected, got %v", hc.Status())
	}

	err := hc.Ping()
	if err == nil {
		t.Error("expected error when disconnected")
	}
}

func TestMouseClick(t *testing.T) {
	mp := &mockPort{}
	hc := newTestHID(mp)
	defer hc.Shutdown()

	hc.MouseClick(1)

	lines := mp.lines()
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var cmd map[string]any
	json.Unmarshal([]byte(lines[0]), &cmd)
	if cmd["cmd"] != "mouse_click" {
		t.Errorf("expected mouse_click, got %v", cmd["cmd"])
	}
	if int(cmd["buttons"].(float64)) != 1 {
		t.Errorf("expected buttons=1, got %v", cmd["buttons"])
	}
}

func TestRelaySerialFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/dev/serial/by-id/usb-Adafruit_Roadie-Relay_A1B2C3D4E5F60001-if02", "A1B2C3D4E5F60001"},
		{"/dev/serial/by-id/usb-Adafruit_Roadie-Relay_A1B2C3D4E5F60002-if02", "A1B2C3D4E5F60002"},
		{"/dev/cu.usbmodem2103", ""}, // macOS ports carry no serial in the path
		{"", ""},
	}
	for _, tt := range tests {
		if got := relaySerialFromPath(tt.path); got != tt.want {
			t.Errorf("relaySerialFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestSelectRelay(t *testing.T) {
	a := RelayInfo{Serial: "A1B2C3D4E5F60001", Path: "/dev/serial/by-id/usb-Adafruit_Roadie-Relay_A1B2C3D4E5F60001-if02"}
	b := RelayInfo{Serial: "A1B2C3D4E5F60002", Path: "/dev/serial/by-id/usb-Adafruit_Roadie-Relay_A1B2C3D4E5F60002-if02"}
	both := []RelayInfo{a, b}

	tests := []struct {
		name    string
		relays  []RelayInfo
		filter  string
		want    RelayInfo
		wantErr bool
	}{
		{"no boards", nil, "", RelayInfo{}, true},
		{"single board, no filter", []RelayInfo{a}, "", a, false},
		{"two boards, no filter is ambiguous", both, "", RelayInfo{}, true},
		{"full serial", both, "A1B2C3D4E5F60001", a, false},
		{"unique suffix", both, "0002", b, false},
		{"lowercase filter", both, "a1b2c3d4e5f60001", a, false},
		{"port path substring", both, "F60002-if02", b, false},
		{"shared prefix is ambiguous", both, "A1B2C3D4E5F600", RelayInfo{}, true},
		{"no match", both, "NOSUCHBOARD", RelayInfo{}, true},
		{"filter matches single board", []RelayInfo{a}, "0001", a, false},
		{"filter misses single board", []RelayInfo{a}, "0002", RelayInfo{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectRelay(tt.relays, tt.filter)
			if (err != nil) != tt.wantErr {
				t.Fatalf("selectRelay(%q) error = %v, wantErr %v", tt.filter, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("selectRelay(%q) = %v, want %v", tt.filter, got, tt.want)
			}
		})
	}
}

// TestAudioConstants checks the audio frame constants in this package
// against board/shared/protocol.py, which is what the boards actually run.
//
// The two are separate declarations of one wire format, and a comment
// asking for them to match is not enough: renumbering CMD_AUDIO in the
// Python moved the boards but not the server, and the only symptom was
// audio arriving through the relay's serial port (pure Python) while every
// path through the server went silent.
func TestAudioConstants(t *testing.T) {
	const pyPath = "board/shared/protocol.py"
	body, err := os.ReadFile(pyPath)
	if err != nil {
		t.Fatalf("cannot read %s: %v", pyPath, err)
	}

	// Pull out "NAME = 0x40" / "NAME = 512" definitions.
	defs := map[string]int64{}
	re := regexp.MustCompile(`(?m)^([A-Z_][A-Z0-9_]*)\s*=\s*(0[xX][0-9A-Fa-f]+|\d+)\s*(?:#.*)?$`)
	for _, m := range re.FindAllStringSubmatch(string(body), -1) {
		v, err := strconv.ParseInt(m[2], 0, 64)
		if err != nil {
			continue
		}
		defs[m[1]] = v
	}
	if len(defs) == 0 {
		t.Fatalf("parsed no constants out of %s", pyPath)
	}

	for _, tt := range []struct {
		pyName string
		goName string
		goVal  int64
	}{
		{"CMD_AUDIO", "audioCmd", audioCmd},
		{"AUDIO_MAGIC", "audioMagic", audioMagic},
		{"AUDIO_LEAD", "audioLead", audioLead},
		{"AUDIO_MAX", "audioMax", audioMax},
	} {
		want, ok := defs[tt.pyName]
		if !ok {
			t.Errorf("%s is no longer defined in %s; %s has nothing to "+
				"match", tt.pyName, pyPath, tt.goName)
			continue
		}
		if tt.goVal != want {
			t.Errorf("%s = %#x but %s = %#x in %s — the server and the "+
				"boards disagree about the wire format", tt.goName,
				tt.goVal, tt.pyName, want, pyPath)
		}
	}
}
