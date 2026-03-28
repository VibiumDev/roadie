package main

import (
	"bytes"
	"encoding/json"
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
	hc := NewHIDController()
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
	hc := NewHIDController()
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
