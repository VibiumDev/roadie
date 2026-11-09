package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/VibiumDev/roadie/internal/capture"
	"github.com/VibiumDev/roadie/internal/hid"
	"github.com/VibiumDev/station/rig"
)

// fakeFrames is a FrameSource that always has one small JPEG.
type fakeFrames struct{ jpg []byte }

func newFakeFrames(t *testing.T) *fakeFrames {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 30), uint8(y * 40), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 50}); err != nil {
		t.Fatal(err)
	}
	return &fakeFrames{jpg: buf.Bytes()}
}

func (f *fakeFrames) Latest() []byte                { return f.jpg }
func (f *fakeFrames) LatestRaw() []byte             { return f.jpg }
func (f *fakeFrames) Status() capture.CaptureStatus { return capture.StatusConnected }
func (f *fakeFrames) CropRect() image.Rectangle     { return image.Rect(0, 0, 8, 6) }
func (f *fakeFrames) ContentRect() image.Rectangle  { return image.Rect(0, 0, 8, 6) }

type uploaded struct {
	mu   sync.Mutex
	recs []rig.Recording
	data [][]byte
	done chan struct{}
}

func readTrace(t *testing.T, data []byte) (lines []map[string]any, files []string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		files = append(files, f.Name)
		if f.Name == "trace.trace" {
			rc, _ := f.Open()
			raw, _ := io.ReadAll(rc)
			rc.Close()
			for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var m map[string]any
				if err := json.Unmarshal([]byte(l), &m); err != nil {
					t.Fatalf("trace line %q: %v", l, err)
				}
				lines = append(lines, m)
			}
		}
	}
	return lines, files
}

func TestRecorderRecordsAHold(t *testing.T) {
	entry := &rig.StationEntry{Name: "st", Npub: "npub1test", URL: "https://st.example", RigID: "rig_1", Record: true}
	key := nostr.Generate()
	up := &uploaded{done: make(chan struct{}, 4)}
	dir := t.TempDir()
	r := newRecorder(newFakeFrames(t), func() (*rig.StationEntry, string, string, nostr.SecretKey, bool) {
		return entry, "rig_1/phone", "hld_1", key, true
	}, dir)
	r.settle, r.burst = 20*time.Millisecond, 10*time.Millisecond
	r.logf = func(string, ...any) {}
	r.upload = func(ctx context.Context, e *rig.StationEntry, k nostr.SecretKey, rec rig.Recording) (map[string]any, error) {
		data, _ := io.ReadAll(rec.Body)
		up.mu.Lock()
		up.recs = append(up.recs, rec)
		up.data = append(up.data, data)
		up.mu.Unlock()
		up.done <- struct{}{}
		return map[string]any{"url": "https://st.example/x.zip"}, nil
	}
	// A burst of typing, then a tap a while later.
	r.input("type", map[string]any{"text": "hel"})
	r.input("type", map[string]any{"text": "lo"})
	time.Sleep(60 * time.Millisecond)
	r.input("touch", map[string]any{"contacts": []hid.TouchContact{{ID: 0, Tip: true, X: 100, Y: 200}}})
	r.input("touch", map[string]any{"contacts": []hid.TouchContact{}})
	time.Sleep(60 * time.Millisecond)
	r.end("hld_1")
	select {
	case <-up.done:
	case <-time.After(5 * time.Second):
		t.Fatal("no upload")
	}
	rec := up.recs[0]
	if rec.HoldID != "hld_1" || rec.DeviceID != "rig_1/phone" || rec.Name != "hld_1.zip" || rec.Type != "application/zip" || rec.Size != int64(len(up.data[0])) {
		t.Fatalf("upload: %+v", rec)
	}
	lines, files := readTrace(t, up.data[0])
	if lines[0]["type"] != "context-options" || lines[0]["libraryName"] != "vibium" || lines[0]["producer"] != "roadie" || lines[0]["title"] != "rig_1/phone · hld_1" {
		t.Fatalf("first line: %v", lines[0])
	}
	var befores, frames []map[string]any
	for _, l := range lines {
		switch l["type"] {
		case "before":
			befores = append(befores, l)
		case "screencast-frame":
			frames = append(frames, l)
		}
	}
	if len(befores) != 2 {
		t.Fatalf("want two steps (a typing burst and a tap), got %d: %v", len(befores), befores)
	}
	if befores[0]["title"] != "Keyboard.type ×2" || befores[1]["title"] != "Touch ×2" || befores[0]["method"] != "vibium:hid.type" {
		t.Fatalf("titles: %v %v", befores[0]["title"], befores[1]["title"])
	}
	if p := befores[0]["params"].(map[string]any); p["count"].(float64) != 2 || len(p["inputs"].([]any)) != 2 {
		t.Fatalf("step params: %v", p)
	}
	// A frame at the start, one per step, one at the end.
	if len(frames) != 4 {
		t.Fatalf("want four frames, got %d", len(frames))
	}
	for _, f := range frames {
		name := "resources/" + f["sha1"].(string)
		found := false
		for _, n := range files {
			if n == name {
				found = true
			}
		}
		if !found || f["width"].(float64) != 8 || f["height"].(float64) != 6 {
			t.Fatalf("frame %v missing from %v", f, files)
		}
	}
	// Delivered: nothing left on disk.
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	// An input after the end starts a new recording, not a late step.
	if r.active["hld_1"] != nil {
		t.Fatal("ended recording still active")
	}
}

func TestRecorderSkipsWithoutPolicy(t *testing.T) {
	entry := &rig.StationEntry{Name: "st", RigID: "rig_1"}
	r := newRecorder(newFakeFrames(t), func() (*rig.StationEntry, string, string, nostr.SecretKey, bool) {
		return entry, "rig_1/phone", "hld_1", nostr.Generate(), true
	}, t.TempDir())
	r.input("type", map[string]any{"text": "x"})
	if len(r.active) != 0 {
		t.Fatal("recorded without the policy's say")
	}
	r2 := newRecorder(newFakeFrames(t), func() (*rig.StationEntry, string, string, nostr.SecretKey, bool) {
		return nil, "", "", nostr.SecretKey{}, false
	}, t.TempDir())
	r2.input("type", map[string]any{"text": "x"})
	if len(r2.active) != 0 {
		t.Fatal("recorded with no hold")
	}
}

func TestRecorderKeepsAndRetriesAFailedUpload(t *testing.T) {
	entry := &rig.StationEntry{Name: "st", Npub: "npub1test", URL: "https://st.example", RigID: "rig_1", Record: true}
	key := nostr.Generate()
	dir := t.TempDir()
	fail := true
	tries := make(chan string, 4)
	r := newRecorder(newFakeFrames(t), func() (*rig.StationEntry, string, string, nostr.SecretKey, bool) {
		return entry, "rig_1/phone", "hld_2", key, true
	}, dir)
	r.settle, r.burst = 10*time.Millisecond, 5*time.Millisecond
	r.logf = func(string, ...any) {}
	r.upload = func(ctx context.Context, e *rig.StationEntry, k nostr.SecretKey, rec rig.Recording) (map[string]any, error) {
		tries <- rec.HoldID
		if fail {
			return nil, os.ErrDeadlineExceeded
		}
		return map[string]any{}, nil
	}
	r.input("key_press", map[string]any{"keycode": 40})
	r.end("hld_2")
	<-tries
	time.Sleep(20 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "hld_2.zip")); err != nil {
		t.Fatal("a failed upload should leave the zip")
	}
	fail = false
	r.retry(&rig.Policy{Stations: []*rig.StationEntry{entry}}, key)
	if got := <-tries; got != "hld_2" {
		t.Fatalf("retry sent %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Fatalf("left behind after the retry: %v", left)
	}
}

func TestStepTitle(t *testing.T) {
	if stepTitle("type", 1) != "Keyboard.type" || stepTitle("touch", 3) != "Touch ×3" || stepTitle("odd", 1) != "HID.odd" {
		t.Fatal("titles")
	}
}
