package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/VibiumDev/roadie/rtc"
	"github.com/VibiumDev/station/rig"
)

// The parameter sets of two pictures: an iPhone upright, 444x972, and a
// picture of 320x240.
var (
	tallSPS, _  = hex.DecodeString("2742c02895a0701efbb840000003004000000f382800162000005f5e6f7b80789135")
	otherSPS, _ = hex.DecodeString("2742c01eda0507e4")
	somePPS     = []byte{0x28, 0xce, 0x3c, 0x80}
)

func annexB(nals ...[]byte) []byte {
	var au []byte
	for _, n := range nals {
		au = append(append(au, 0, 0, 0, 1), n...)
	}
	return au
}

// fakeTap plays an encoder: a keyframe first, then a frame every 5 ms.
type fakeTap struct {
	mu      sync.Mutex
	sps     []byte
	taps    int
	stopped int
	viewer  bool // a viewer is recording the stream
}

func (f *fakeTap) Available() bool { return true }

func (f *fakeTap) ViewerRecording() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.viewer
}

func (f *fakeTap) Tap(sink rtc.SampleSink) func() {
	f.mu.Lock()
	f.taps++
	f.mu.Unlock()
	quit := make(chan struct{})
	go func() {
		start := time.Now()
		var was []byte
		for {
			f.mu.Lock()
			sps := f.sps
			f.mu.Unlock()
			if !bytes.Equal(sps, was) {
				was = sps
				sink(annexB(sps, somePPS, []byte{0x25, 0x88, 0x84}), true, time.Since(start))
			} else {
				sink(annexB([]byte{0x21, 0x9a, 0x02}), false, time.Since(start))
			}
			select {
			case <-quit:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(quit)
			f.mu.Lock()
			f.stopped++
			f.mu.Unlock()
		})
	}
}

// recordAHold runs one hold with a video tap and returns the uploaded zip.
func recordAHold(t *testing.T, tap *fakeTap, max int64, during func(r *recorder)) (*zip.Reader, string) {
	t.Helper()
	return recordAHoldWith(t, tap, nil, max, during)
}

func leftIn(dir string) []string {
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	return left
}

// settled is what is still in dir once the recorder has had its moment:
// an upload answers before the recorder removes the zip it sent.
func settled(dir string) []string {
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if left := leftIn(dir); len(left) == 0 || time.Now().After(deadline) {
			return left
		}
	}
}

// recordAHoldWith runs one hold with what the host has, a video tap or a
// sound source or both, and returns the uploaded zip.
func recordAHoldWith(t *testing.T, tap *fakeTap, sound SoundSource, max int64, during func(r *recorder)) (*zip.Reader, string) {
	t.Helper()
	entry := &rig.StationEntry{Name: "st", Npub: "npub1test", URL: "https://st.example", RigID: "rig_1", Record: true}
	key := nostr.Generate()
	dir := t.TempDir()
	r := NewRecorder(newFakeFrames(t), func() (*rig.StationEntry, string, string, nostr.SecretKey, bool) {
		return entry, "rig_1/phone", "hld_v", key, true
	}, dir)
	r.settle, r.burst = 20*time.Millisecond, 10*time.Millisecond
	r.logf = t.Logf
	if tap != nil {
		r.SetVideo(tap, max)
	} else {
		r.SetVideo(nil, max)
	}
	if sound != nil {
		r.SetSound(sound)
	}
	got := make(chan []byte, 1)
	r.upload = func(ctx context.Context, e *rig.StationEntry, k nostr.SecretKey, rec rig.Recording) (map[string]any, error) {
		data, _ := io.ReadAll(rec.Body)
		if int64(len(data)) != rec.Size {
			t.Errorf("upload: %d bytes read, size %d", len(data), rec.Size)
		}
		got <- data
		return map[string]any{"url": "https://st.example/x.zip"}, nil
	}
	r.Input("touch", map[string]any{})
	during(r)
	r.End("hld_v")
	select {
	case data := <-got:
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		return zr, dir
	case <-time.After(5 * time.Second):
		t.Fatal("no upload")
		return nil, ""
	}
}

func readVideoIndex(t *testing.T, zr *zip.Reader) []videoEntry {
	t.Helper()
	f, err := zr.Open("video/index.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var index struct {
		Version int          `json:"version"`
		Videos  []videoEntry `json:"videos"`
	}
	if err := json.NewDecoder(f).Decode(&index); err != nil || index.Version != 1 {
		t.Fatalf("video/index.json: version %d, %v", index.Version, err)
	}
	return index.Videos
}

func TestRecorderKeepsAVideoOfTheHold(t *testing.T) {
	tap := &fakeTap{sps: tallSPS}
	zr, dir := recordAHold(t, tap, 8<<20, func(*recorder) { time.Sleep(150 * time.Millisecond) })

	videos := readVideoIndex(t, zr)
	if len(videos) != 1 {
		t.Fatalf("videos: %+v", videos)
	}
	v := videos[0]
	if v.File != "video/screen.mp4" || v.Context != "screen" || v.MimeType != "video/mp4" || v.Width != 444 || v.Height != 972 || v.Error != "" {
		t.Errorf("entry %+v", v)
	}
	// The video starts with the recording and runs as long as the hold.
	if v.OffsetMs < 0 || v.OffsetMs > 100 || v.DurationMs < 100 || v.DurationMs > 1000 {
		t.Errorf("offset %d ms, duration %d ms", v.OffsetMs, v.DurationMs)
	}
	f, err := zr.Open(v.File)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if len(data) < 16 || string(data[4:8]) != "ftyp" || !bytes.Contains(data, []byte("moov")) || !bytes.Contains(data, []byte("avcC")) {
		t.Errorf("%s is not a finished MP4 (%d bytes)", v.File, len(data))
	}
	if size := binary.BigEndian.Uint64(data[40:]); string(data[36:40]) != "mdat" || size < 16 || int(size) > len(data) {
		t.Errorf("mdat says %d bytes in a file of %d", size, len(data))
	}
	// The steps and their frames are there as before.
	if _, err := zr.Open("trace.trace"); err != nil {
		t.Error(err)
	}
	if tap.taps != 1 || tap.stopped != 1 {
		t.Errorf("the encoder was tapped %d times and released %d", tap.taps, tap.stopped)
	}
	if left := settled(dir); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

func TestRecorderStartsAnotherVideoWhenThePictureChanges(t *testing.T) {
	tap := &fakeTap{sps: tallSPS}
	zr, _ := recordAHold(t, tap, 8<<20, func(*recorder) {
		time.Sleep(80 * time.Millisecond)
		tap.mu.Lock()
		tap.sps = otherSPS
		tap.mu.Unlock()
		time.Sleep(80 * time.Millisecond)
	})
	videos := readVideoIndex(t, zr)
	if len(videos) != 2 || videos[0].File != "video/screen.mp4" || videos[1].File != "video/screen-2.mp4" {
		t.Fatalf("videos: %+v", videos)
	}
	// The second begins where the first ends, give or take a frame.
	end := videos[0].OffsetMs + videos[0].DurationMs
	if d := videos[1].OffsetMs - end; d < -30 || d > 30 {
		t.Errorf("the first video ends at %d ms and the second starts at %d", end, videos[1].OffsetMs)
	}
	if videos[1].Width != 320 || videos[1].Height != 240 {
		t.Errorf("the second video is %dx%d", videos[1].Width, videos[1].Height)
	}
	for _, v := range videos {
		if _, err := zr.Open(v.File); err != nil {
			t.Error(err)
		}
	}
}

func TestRecorderStopsAVideoAtItsLimit(t *testing.T) {
	tap := &fakeTap{sps: tallSPS}
	zr, _ := recordAHold(t, tap, 150, func(*recorder) { time.Sleep(200 * time.Millisecond) })
	videos := readVideoIndex(t, zr)
	if len(videos) != 1 || !strings.Contains(videos[0].Error, "limit") {
		t.Fatalf("videos: %+v", videos)
	}
	if videos[0].DurationMs > 150 {
		t.Errorf("a video capped at 150 bytes ran %d ms", videos[0].DurationMs)
	}
	tap.mu.Lock()
	defer tap.mu.Unlock()
	if tap.stopped != 1 {
		t.Errorf("the encoder was released %d times", tap.stopped)
	}
}

func TestRecorderWithoutAnEncoderRecordsNoVideo(t *testing.T) {
	zr, _ := recordAHold(t, &fakeTap{sps: tallSPS}, 0, func(*recorder) { time.Sleep(30 * time.Millisecond) })
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "video/") {
			t.Errorf("%s in a recording with video off", f.Name)
		}
	}
}

func TestRecorderLeavesVideoToAViewerThatRecords(t *testing.T) {
	hasMedia := func(zr *zip.Reader) (names []string) {
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, "video/") || strings.HasPrefix(f.Name, "audio/") {
				names = append(names, f.Name)
			}
		}
		return names
	}
	// The viewer was recording before the hold's first input.
	tap := &fakeTap{sps: tallSPS, viewer: true}
	zr, dir := recordAHoldWith(t, tap, &fakeSound{tone: true}, 8<<20, func(*recorder) { time.Sleep(100 * time.Millisecond) })
	if got := hasMedia(zr); len(got) != 0 {
		t.Errorf("recorded %v next to a viewer that records", got)
	}
	if tap.taps != 0 {
		t.Errorf("the encoder was tapped %d times", tap.taps)
	}
	if left := settled(dir); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}

	// The viewer starts recording partway through the hold.
	tap = &fakeTap{sps: tallSPS}
	zr, dir = recordAHoldWith(t, tap, &fakeSound{tone: true}, 8<<20, func(r *recorder) {
		say(r, 100*time.Millisecond)
		r.ViewerRecords()
		say(r, 100*time.Millisecond)
	})
	if got := hasMedia(zr); len(got) != 0 {
		t.Errorf("kept %v after a viewer began recording", got)
	}
	if _, err := zr.Open("trace.trace"); err != nil {
		t.Error(err)
	}
	if left := settled(dir); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}
