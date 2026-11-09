package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"

	"github.com/VibiumDev/roadie/internal/capture"
	"github.com/VibiumDev/station/rig"
)

// A hold's recording: what the phone showed after each burst of input,
// in Vibium's recording format, so the Record Player plays it like any
// other run. The rig records a hold when the station's policy entry says
// so (`"record": true` in rig.json), keeps the frames in memory while
// the hold lasts, and when the hold ends writes the zip next to its
// policy and uploads it to that station under the rig key. A zip the
// upload could not deliver stays on disk and is tried again at the next
// start.

const roadieVersion = "dev"

// Recording limits: a hold of hours would otherwise fill memory, so past
// these the steps keep coming but the frames stop.
const (
	recordSettle    = 400 * time.Millisecond // quiet after an input before its frame
	recordBurst     = 250 * time.Millisecond // inputs this close are one step
	recordMaxFrames = 600
	recordMaxBytes  = 64 << 20
	recordMaxParams = 16 // inputs listed per step; the rest are counted
)

// holdSource says who holds the device now, from the rig side.
type holdSource func() (entry *rig.StationEntry, deviceID, holdID string, key nostr.SecretKey, ok bool)

// recorder keeps the hold recordings in the making.
type recorder struct {
	source  capture.FrameSource
	current holdSource
	dir     string // finished zips wait here until uploaded
	// upload sends a finished zip; the default is rig.Upload.
	upload func(ctx context.Context, e *rig.StationEntry, key nostr.SecretKey, rec rig.Recording) (map[string]any, error)
	logf   func(format string, args ...any)
	now    func() time.Time
	settle time.Duration
	burst  time.Duration

	mu     sync.Mutex
	active map[string]*holdRecording // by hold id
}

// holdRecording is one hold being recorded.
type holdRecording struct {
	entry    *rig.StationEntry
	key      nostr.SecretKey
	deviceID string
	holdID   string
	started  time.Time
	lines    []map[string]any
	frames   map[string][]byte
	bytes    int64
	calls    int
	step     *recordStep
	timer    *time.Timer
	ended    bool
	lastIn   time.Time
}

// recordStep is a burst of inputs that becomes one step with one frame.
type recordStep struct {
	callID int
	kind   string
	params []map[string]any
	count  int
	start  time.Time
}

func newRecorder(source capture.FrameSource, current holdSource, dir string) *recorder {
	return &recorder{source: source, current: current, dir: dir, upload: rig.Upload, logf: log.Printf, now: time.Now,
		settle: recordSettle, burst: recordBurst, active: map[string]*holdRecording{}}
}

func (r *recorder) ms(rec *holdRecording, t time.Time) int64 {
	return t.Sub(rec.started).Milliseconds()
}

// input is what the HID wrapper reports: every input that went through.
// Inputs under a hold the policy says to record are kept; others pass.
func (r *recorder) input(kind string, params map[string]any) {
	entry, deviceID, holdID, key, ok := r.current()
	if !ok || entry == nil || !entry.Record {
		return
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.active[holdID]
	if rec == nil {
		rec = &holdRecording{entry: entry, key: key, deviceID: deviceID, holdID: holdID, started: now, frames: map[string][]byte{}}
		rec.lines = append(rec.lines, map[string]any{
			// libraryName says whose format this is, which is what the Record
			// Player reads to tell a Vibium recording from a Playwright trace;
			// producer says who made it.
			"version": 8, "type": "context-options", "origin": "library", "libraryName": "vibium", "libraryVersion": roadieVersion, "producer": "roadie",
			"browserName": "phone", "platform": "linux", "wallTime": now.UnixMilli(), "monotonicTime": 0, "sdkLanguage": "javascript",
			"title": deviceID + " · " + holdID, "contextId": "context@" + holdID, "options": map[string]any{},
		})
		r.active[holdID] = rec
		r.logf("record: %s on %s: recording for %s", holdID, deviceID, entry.Name)
		// The picture as the hold found it.
		r.frame(rec, now)
	}
	if rec.ended {
		return
	}
	if rec.step != nil && now.Sub(rec.lastIn) > r.burst {
		r.closeStep(rec, now)
	}
	if rec.step == nil {
		rec.calls++
		rec.step = &recordStep{callID: rec.calls, kind: kind, start: now}
	}
	if len(rec.step.params) < recordMaxParams {
		p := map[string]any{"cmd": kind}
		for k, v := range params {
			p[k] = v
		}
		rec.step.params = append(rec.step.params, p)
	}
	rec.step.count++
	rec.lastIn = now
	if rec.timer != nil {
		rec.timer.Stop()
	}
	rec.timer = time.AfterFunc(r.settle, func() { r.settled(holdID) })
}

// settled runs when the inputs have been quiet: the step closes with a
// frame of what the phone shows now.
func (r *recorder) settled(holdID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.active[holdID]
	if rec == nil || rec.ended || rec.step == nil {
		return
	}
	r.closeStep(rec, r.now())
}

// closeStep writes the open step: before, the frame, after. Called with
// the lock held.
func (r *recorder) closeStep(rec *holdRecording, now time.Time) {
	st := rec.step
	rec.step = nil
	if st == nil {
		return
	}
	callID := fmt.Sprintf("call@%d", st.callID)
	params := map[string]any{"inputs": st.params, "count": st.count}
	rec.lines = append(rec.lines, map[string]any{"type": "before", "title": stepTitle(st.kind, st.count), "callId": callID,
		"startTime": r.ms(rec, st.start), "class": "HID", "method": "vibium:hid." + st.kind, "pageId": "page@screen", "params": params})
	r.frame(rec, now)
	rec.lines = append(rec.lines, map[string]any{"type": "after", "callId": callID, "endTime": r.ms(rec, now)})
}

// stepTitle names a step the way the Record Player shows it.
func stepTitle(kind string, count int) string {
	title := map[string]string{
		"type": "Keyboard.type", "key_press": "Keyboard.press", "key_release": "Keyboard.release",
		"mouse_move": "Pointer.move", "mouse_click": "Pointer.click", "mouse_press": "Pointer.press", "mouse_release": "Pointer.release",
		"mouse_scroll": "Pointer.scroll", "scroll_at": "Pointer.scroll", "touch": "Touch",
	}[kind]
	if title == "" {
		title = "HID." + kind
	}
	if count > 1 {
		title += fmt.Sprintf(" ×%d", count)
	}
	return title
}

// frame adds what the capture shows now, within the limits. Called with
// the lock held.
func (r *recorder) frame(rec *holdRecording, now time.Time) {
	if r.source == nil || len(rec.frames) >= recordMaxFrames || rec.bytes >= recordMaxBytes {
		return
	}
	data := r.source.Latest()
	if len(data) == 0 {
		return
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return
	}
	name := fmt.Sprintf("page@screen-%d.jpeg", now.UnixMilli())
	for i := 1; rec.frames[name] != nil; i++ {
		name = fmt.Sprintf("page@screen-%d-%d.jpeg", now.UnixMilli(), i)
	}
	copied := make([]byte, len(data))
	copy(copied, data)
	rec.frames[name] = copied
	rec.bytes += int64(len(copied))
	rec.lines = append(rec.lines, map[string]any{"type": "screencast-frame", "pageId": "page@screen", "sha1": name,
		"width": cfg.Width, "height": cfg.Height, "timestamp": r.ms(rec, now)})
}

// end finishes a hold's recording, if one is going: the zip is written
// and uploaded in the background.
func (r *recorder) end(holdID string) {
	r.mu.Lock()
	rec := r.active[holdID]
	if rec == nil {
		r.mu.Unlock()
		return
	}
	delete(r.active, holdID)
	rec.ended = true
	if rec.timer != nil {
		rec.timer.Stop()
	}
	now := r.now()
	r.closeStep(rec, now)
	// The picture as the hold left it.
	r.frame(rec, now)
	r.mu.Unlock()
	go r.finish(rec)
}

// endAll finishes every recording, at shutdown.
func (r *recorder) endAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.active))
	for id := range r.active {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.end(id)
	}
}

// recordingMeta rides next to a zip that waits for its upload.
type recordingMeta struct {
	Station  string `json:"station"` // the entry's npub
	DeviceID string `json:"device"`
	HoldID   string `json:"hold"`
	Started  string `json:"started"`
}

// finish writes the zip to disk and uploads it.
func (r *recorder) finish(rec *holdRecording) {
	var buf bytes.Buffer
	if err := writeTrace(&buf, rec.lines, rec.frames); err != nil {
		r.logf("record: %s: %v", rec.holdID, err)
		return
	}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		r.logf("record: %s: %v", rec.holdID, err)
		return
	}
	base := filepath.Join(r.dir, rec.holdID)
	meta, _ := json.Marshal(recordingMeta{Station: rec.entry.Npub, DeviceID: rec.deviceID, HoldID: rec.holdID, Started: rec.started.UTC().Format(time.RFC3339)})
	if err := os.WriteFile(base+".zip", buf.Bytes(), 0o600); err != nil {
		r.logf("record: %s: %v", rec.holdID, err)
		return
	}
	os.WriteFile(base+".json", meta, 0o600)
	r.logf("record: %s: %d steps, %d frames, %d bytes; uploading to %s", rec.holdID, rec.calls, len(rec.frames), buf.Len(), rec.entry.Name)
	r.send(base, rec.entry, rec.key, rec.deviceID, rec.holdID)
}

// send uploads base.zip and removes it with its sidecar on success.
func (r *recorder) send(base string, entry *rig.StationEntry, key nostr.SecretKey, deviceID, holdID string) {
	data, err := os.ReadFile(base + ".zip")
	if err != nil {
		r.logf("record: %s: %v", holdID, err)
		return
	}
	sum := sha256.Sum256(data)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	desc, err := r.upload(ctx, entry, key, rig.Recording{Body: bytes.NewReader(data), Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]),
		Name: holdID + ".zip", Type: "application/zip", DeviceID: deviceID, HoldID: holdID})
	if err != nil {
		r.logf("record: %s: %v (kept at %s.zip for the next start)", holdID, err, base)
		return
	}
	os.Remove(base + ".zip")
	os.Remove(base + ".json")
	r.logf("record: %s uploaded: %v", holdID, desc["url"])
}

// retry sends the zips an earlier run could not deliver.
func (r *recorder) retry(policy *rig.Policy, key nostr.SecretKey) {
	metas, _ := filepath.Glob(filepath.Join(r.dir, "*.json"))
	sort.Strings(metas)
	for _, m := range metas {
		var meta recordingMeta
		data, err := os.ReadFile(m)
		if err != nil || json.Unmarshal(data, &meta) != nil {
			continue
		}
		base := strings.TrimSuffix(m, ".json")
		if _, err := os.Stat(base + ".zip"); err != nil {
			os.Remove(m)
			continue
		}
		var entry *rig.StationEntry
		for _, e := range policy.Stations {
			if e.Npub == meta.Station {
				entry = e
			}
		}
		if entry == nil {
			r.logf("record: %s: its station is no longer in the policy; left at %s.zip", meta.HoldID, base)
			continue
		}
		r.send(base, entry, key, meta.DeviceID, meta.HoldID)
	}
}

// writeTrace writes a recording in Vibium's format: trace.trace with
// one JSON event per line, an empty trace.network, and the frames under
// resources/.
func writeTrace(w *bytes.Buffer, lines []map[string]any, frames map[string][]byte) error {
	zw := zip.NewWriter(w)
	f, err := zw.Create("trace.trace")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			return err
		}
	}
	if _, err := zw.Create("trace.network"); err != nil {
		return err
	}
	names := make([]string, 0, len(frames))
	for n := range frames {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: "resources/" + n, Method: zip.Store})
		if err != nil {
			return err
		}
		if _, err := f.Write(frames[n]); err != nil {
			return err
		}
	}
	return zw.Close()
}
