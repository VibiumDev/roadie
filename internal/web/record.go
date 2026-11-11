package web

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
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
//
// On a host that encodes H.264 the recording also holds a video of the
// screen for as long as it ran (see record_video.go), and with it the
// sound: what the device played and what was sent to its microphone (see
// record_sound.go).

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
	// video is where a hold's video comes from, and videoMax the most of
	// it one recording keeps, in bytes. No tap or no room means no video.
	// sound is where what the device played comes from. A videoMax of
	// zero records neither.
	video    VideoTap
	videoMax int64
	sound    SoundSource

	mu      sync.Mutex
	active  map[string]*holdRecording // by hold id
	writing sync.WaitGroup            // zips on their way to disk
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
	endedAt  time.Time
	media    bool // video and sound are kept, as far as the host has them
	video    *holdVideo
	sound    *holdSound // what the device played
	said     *holdSound // what went to its microphone
}

// recordStep is a burst of inputs that becomes one step with one frame.
type recordStep struct {
	callID int
	kind   string
	params []map[string]any
	count  int
	start  time.Time
}

func NewRecorder(source capture.FrameSource, current holdSource, dir string) *recorder {
	return &recorder{source: source, current: current, dir: dir, upload: rig.Upload, logf: log.Printf, now: time.Now,
		settle: recordSettle, burst: recordBurst, active: map[string]*holdRecording{}}
}

// SetVideo gives recordings a video, at most maxBytes of it each.
func (r *recorder) SetVideo(tap VideoTap, maxBytes int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.video, r.videoMax = tap, maxBytes
}

// SetSound gives recordings what the device played.
func (r *recorder) SetSound(src SoundSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sound = src
}

// ViewerRecords is told when a viewer of the device starts keeping the
// stream itself, as Vibium does when it records a session. That recording
// has the video and the sound, so the hold's own gives up what it has of
// them and keeps its steps and frames.
func (r *recorder) ViewerRecords() {
	r.mu.Lock()
	var videos []*holdVideo
	var sounds []*holdSound
	for _, rec := range r.active {
		if rec.video != nil {
			videos = append(videos, rec.video)
		}
		for _, s := range []*holdSound{rec.sound, rec.said} {
			if s != nil {
				sounds = append(sounds, s)
			}
		}
		rec.media, rec.video, rec.sound, rec.said = false, nil, nil, nil
	}
	r.mu.Unlock()
	for _, v := range videos {
		go v.drop()
	}
	for _, s := range sounds {
		go s.drop()
	}
}

// startMedia begins a new recording's video and sound, as far as the host
// can make them. Called with the lock held.
func (r *recorder) startMedia(rec *holdRecording) {
	if r.videoMax <= 0 || (r.video != nil && r.video.ViewerRecording()) {
		return
	}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		r.logf("record: %s: no video: %v", rec.holdID, err)
		return
	}
	if free, ok := freeBytes(r.dir); ok && free < uint64(videoRoom*r.videoMax) {
		r.logf("record: %s: no video: %d MB free at %s, and a video needs %d", rec.holdID, free>>20, r.dir, videoRoom*r.videoMax>>20)
		return
	}
	rec.media = true
	base := filepath.Join(r.dir, rec.holdID)
	if r.sound != nil {
		rec.sound = startHoldSound(r.sound, base+".heard.part", rec.started, r.now, r.logf)
	}
	if r.video != nil && r.video.Available() {
		rec.video = startHoldVideo(r.video, base, rec.started, r.videoMax, r.now, r.logf)
	}
}

// Said takes the sound that goes to the device's microphone: 16 kHz mono,
// 16-bit little-endian, as it is sent. It is kept from the first piece on,
// so a hold in which nothing is said costs nothing.
func (r *recorder) Said(pcm []byte) {
	_, _, holdID, _, ok := r.current()
	if !ok {
		return
	}
	r.mu.Lock()
	rec := r.active[holdID]
	if rec == nil || rec.ended || !rec.media {
		r.mu.Unlock()
		return
	}
	if rec.said == nil {
		rec.said = startSaidSound(filepath.Join(r.dir, rec.holdID)+".said.part", rec.started, r.now, r.logf)
	}
	said := rec.said
	r.mu.Unlock()
	said.push(pcm)
}

func (r *recorder) ms(rec *holdRecording, t time.Time) int64 {
	return t.Sub(rec.started).Milliseconds()
}

// input is what the HID wrapper reports: every input that went through.
// Inputs under a hold the policy says to record are kept; others pass.
func (r *recorder) Input(kind string, params map[string]any) {
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
		r.startMedia(rec)
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
func (r *recorder) End(holdID string) {
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
	rec.endedAt = now
	r.closeStep(rec, now)
	// The picture as the hold left it.
	r.frame(rec, now)
	r.writing.Add(1)
	r.mu.Unlock()
	go r.finish(rec)
}

// endAll finishes every recording, at shutdown, and waits for the zips to
// reach the disk. Their uploads go on while the process lasts.
func (r *recorder) EndAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.active))
	for id := range r.active {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.End(id)
	}
	r.writing.Wait()
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
	base, ok := r.write(rec)
	r.writing.Done()
	if ok {
		r.send(base, rec.entry, rec.key, rec.deviceID, rec.holdID)
	}
}

// write puts the recording's zip and its sidecar on disk.
func (r *recorder) write(rec *holdRecording) (base string, ok bool) {
	// The sound ends first: the video's files take theirs from it.
	var heard, said *holdSound
	if rec.sound != nil {
		defer rec.sound.remove()
		if rec.sound.finish() {
			heard = rec.sound
		}
	}
	if rec.said != nil {
		defer rec.said.remove()
		if rec.said.finish() {
			said = rec.said
		}
	}
	var videos []videoPart
	if rec.video != nil {
		videos = rec.video.finish(rec.endedAt, mixSound(heard, said))
		defer removeParts(videos)
	}
	if err := os.MkdirAll(r.dir, 0o700); err != nil {
		r.logf("record: %s: %v", rec.holdID, err)
		return "", false
	}
	base = filepath.Join(r.dir, rec.holdID)
	// The zip appears under its name whole, so a start after a crash never
	// uploads half of one.
	size, err := writeZip(base+".zip.part", rec.lines, rec.frames, videos, heard, said, rec.started)
	if err == nil {
		err = os.Rename(base+".zip.part", base+".zip")
	}
	if err != nil {
		os.Remove(base + ".zip.part")
		r.logf("record: %s: %v", rec.holdID, err)
		return "", false
	}
	meta, _ := json.Marshal(recordingMeta{Station: rec.entry.Npub, DeviceID: rec.deviceID, HoldID: rec.holdID, Started: rec.started.UTC().Format(time.RFC3339)})
	os.WriteFile(base+".json", meta, 0o600)
	var filmed time.Duration
	for _, v := range videos {
		filmed += v.info.Duration
	}
	sound := "no sound"
	switch {
	case heard != nil && said != nil:
		sound = "sound heard and said"
	case heard != nil:
		sound = "sound heard"
	case said != nil:
		sound = "sound said"
	}
	r.logf("record: %s: %d steps, %d frames, %.0f s of video, %s, %d bytes; uploading to %s", rec.holdID, rec.calls, len(rec.frames), filmed.Seconds(), sound, size, rec.entry.Name)
	return base, true
}

// writeZip writes a recording to path and returns its size.
func writeZip(path string, lines []map[string]any, frames map[string][]byte, videos []videoPart, heard, said *holdSound, started time.Time) (int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriterSize(f, 256<<10)
	err = writeTrace(w, lines, frames, videos, heard, said, started)
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// send uploads base.zip and removes it with its sidecar on success.
func (r *recorder) send(base string, entry *rig.StationEntry, key nostr.SecretKey, deviceID, holdID string) {
	f, err := os.Open(base + ".zip")
	if err != nil {
		r.logf("record: %s: %v", holdID, err)
		return
	}
	defer f.Close()
	sum := sha256.New()
	size, err := io.Copy(sum, f)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		r.logf("record: %s: %v", holdID, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	desc, err := r.upload(ctx, entry, key, rig.Recording{Body: f, Size: size, SHA256: hex.EncodeToString(sum.Sum(nil)),
		Name: holdID + ".zip", Type: "application/zip", DeviceID: deviceID, HoldID: holdID})
	if err != nil {
		r.logf("record: %s: %v (kept at %s.zip for the next start)", holdID, err, base)
		return
	}
	os.Remove(base + ".zip")
	os.Remove(base + ".json")
	r.logf("record: %s uploaded: %v", holdID, desc["url"])
}

// retry sends the zips an earlier run could not deliver. What that run
// left half written is dropped. Call it before any hold is recorded.
func (r *recorder) Retry(policy *rig.Policy, key nostr.SecretKey) {
	parts, _ := filepath.Glob(filepath.Join(r.dir, "*.part"))
	for _, p := range parts {
		os.Remove(p)
	}
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
// one JSON event per line, an empty trace.network, the frames under
// resources/, the videos under video/ and the sound under audio/, each
// with its index.
func writeTrace(w io.Writer, lines []map[string]any, frames map[string][]byte, videos []videoPart, heard, said *holdSound, started time.Time) error {
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
	if len(videos) > 0 {
		entries := videoEntries(videos, started)
		f, err := zw.Create("video/index.json")
		if err != nil {
			return err
		}
		if err := json.NewEncoder(f).Encode(map[string]any{"version": 1, "videos": entries}); err != nil {
			return err
		}
		for i, v := range videos {
			if err := copyInto(zw, entries[i].File, v.path); err != nil {
				return err
			}
		}
	}
	if heard != nil || said != nil {
		// What the device played is a track. What went to its microphone is
		// a clip played once, from the recording's start.
		index := map[string]any{"version": 1}
		wav := func(id string, s *holdSound) map[string]any {
			return map[string]any{"id": id, "target": "session", "file": "audio/" + id + ".wav",
				"sampleRate": soundRate, "channels": 1, "encoding": "pcm_s16le", "durationMs": s.duration().Milliseconds()}
		}
		if heard != nil {
			track := wav("in-session", heard)
			track["startMs"] = 0
			index["tracks"] = []any{track}
		}
		if said != nil {
			clip := wav("out-session", said)
			clip["plays"] = []any{map[string]any{"startMs": 0, "endMs": said.duration().Milliseconds()}}
			index["clips"] = []any{clip}
		}
		f, err := zw.Create("audio/index.json")
		if err != nil {
			return err
		}
		if err := json.NewEncoder(f).Encode(index); err != nil {
			return err
		}
		// Sound is mostly quiet, which packs to almost nothing.
		for id, s := range map[string]*holdSound{"in-session": heard, "out-session": said} {
			if s == nil {
				continue
			}
			if f, err = zw.Create("audio/" + id + ".wav"); err != nil {
				return err
			}
			if err := s.writeWAV(f); err != nil {
				return err
			}
		}
	}
	return zw.Close()
}

// copyInto stores the file at path in the zip as name.
func copyInto(zw *zip.Writer, name, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return err
}
