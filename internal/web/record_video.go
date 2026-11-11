package web

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/VibiumDev/roadie/mp4"
	"github.com/VibiumDev/roadie/rtc"
)

// A hold's video: the H.264 the host already encodes for its viewers,
// stored as it comes. The recorder is one more taker of the encoder's
// output, so recording costs a copy and a write, not a second encode. With
// no viewer connected the tap keeps the encoder running for the hold.

// VideoTap is where the encoded picture comes from. rtc.Broadcaster is one.
type VideoTap interface {
	Available() bool
	Tap(sink rtc.SampleSink) (stop func())
}

// videoQueue is how many encoded frames may wait for the disk: 5 s at 30
// frames a second. Past it frames are dropped, never the encoder held up.
const videoQueue = 150

// videoRoom is how much free disk a hold's video needs, in multiples of
// its limit: the spooled file, its copy inside the zip, and room to spare.
const videoRoom = 3

// holdVideo is the video of one hold being recorded.
type holdVideo struct {
	base    string    // spool files are base-1.mp4.part, base-2.mp4.part
	started time.Time // the recording's start
	max     int64
	now     func() time.Time
	logf    func(format string, args ...any)

	queue   chan videoUnit
	quit    chan struct{}
	done    chan struct{}
	waitKey atomic.Bool
	gaps    atomic.Int64

	// The writer's own.
	cur     *mp4.Writer
	curPath string
	curAt   time.Time
	parts   []videoPart
	bytes   int64
	full    bool
}

type videoUnit struct {
	data []byte
	ts   time.Duration
	at   time.Time
	key  bool
}

// videoPart is one finished file. A hold has a second one when its
// picture changed size, as a phone turned on its side does.
type videoPart struct {
	path      string
	startedAt time.Time
	info      mp4.Info
	gaps      int64
	note      string // why it stops before the hold does
}

// startHoldVideo begins taking the encoder's output for a recording that
// started at started. It returns at once; the tap is made off the caller's
// path, which is an input's.
func startHoldVideo(tap VideoTap, base string, started time.Time, max int64, now func() time.Time, logf func(string, ...any)) *holdVideo {
	v := &holdVideo{base: base, started: started, max: max, now: now, logf: logf,
		queue: make(chan videoUnit, videoQueue), quit: make(chan struct{}), done: make(chan struct{})}
	go v.run(tap)
	return v
}

// sink runs in the encoder's pump.
func (v *holdVideo) sink(data []byte, key bool, ts time.Duration) {
	if v.waitKey.Load() && !key {
		return
	}
	select {
	case v.queue <- videoUnit{data: bytes.Clone(data), key: key, ts: ts, at: v.now()}:
		v.waitKey.Store(false)
	default:
		// The disk is behind. What follows a dropped frame cannot be
		// decoded until the next keyframe.
		if !v.waitKey.Swap(true) {
			v.gaps.Add(1)
		}
	}
}

func (v *holdVideo) run(tap VideoTap) {
	defer close(v.done)
	stop := tap.Tap(v.sink)
	defer stop()
	for {
		select {
		case u := <-v.queue:
			if v.add(u); v.full {
				stop()
			}
		case <-v.quit:
			stop()
			for {
				select {
				case u := <-v.queue:
					v.add(u)
				default:
					v.closePart("")
					return
				}
			}
		}
	}
}

func (v *holdVideo) add(u videoUnit) {
	if v.full {
		return
	}
	if v.cur == nil {
		if !u.key {
			return
		}
		path := fmt.Sprintf("%s-%d.mp4.part", v.base, len(v.parts)+1)
		w, err := mp4.Create(path)
		if err != nil {
			v.logf("record: video: %v", err)
			v.full = true
			return
		}
		v.cur, v.curPath, v.curAt = w, path, u.at
	}
	err := v.cur.Add(u.data, u.ts)
	switch {
	case errors.Is(err, mp4.ErrChanged):
		v.closePart("")
		v.add(u)
	case err != nil:
		v.closePart(err.Error())
		v.full = true
	case v.bytes+v.cur.Bytes() >= v.max:
		v.closePart(fmt.Sprintf("the video reached its limit of %d MB", v.max>>20))
		v.full = true
	}
}

// closePart finishes the file being written.
func (v *holdVideo) closePart(note string) {
	if v.cur == nil {
		return
	}
	w, path := v.cur, v.curPath
	v.cur = nil
	info, err := w.Finish()
	if err != nil {
		w.Discard()
		v.logf("record: video: %v", err)
		return
	}
	v.bytes += info.Bytes
	v.parts = append(v.parts, videoPart{path: path, startedAt: v.curAt, info: info, gaps: v.gaps.Swap(0), note: note})
}

// finish ends the video and returns its files, which the caller removes.
func (v *holdVideo) finish() []videoPart {
	close(v.quit)
	<-v.done
	return v.parts
}

// videoEntry is one video in video/index.json, as Vibium's recording
// format has it for a device.
type videoEntry struct {
	File       string `json:"file"`
	Context    string `json:"context"`
	StartedAt  int64  `json:"startedAt"`
	OffsetMs   int64  `json:"offsetMs"`
	DurationMs int64  `json:"durationMs"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	MimeType   string `json:"mimeType"`
	Gaps       int64  `json:"gaps,omitempty"`
	Error      string `json:"error,omitempty"`
}

// videoEntries names the parts as the zip holds them.
func videoEntries(parts []videoPart, started time.Time) []videoEntry {
	entries := make([]videoEntry, len(parts))
	for i, p := range parts {
		name := "video/screen.mp4"
		if i > 0 {
			name = fmt.Sprintf("video/screen-%d.mp4", i+1)
		}
		entries[i] = videoEntry{File: name, Context: "screen", StartedAt: p.startedAt.UnixMilli(),
			OffsetMs: p.startedAt.Sub(started).Milliseconds(), DurationMs: p.info.Duration.Milliseconds(),
			Width: p.info.Width, Height: p.info.Height, MimeType: "video/mp4", Gaps: p.gaps, Error: p.note}
	}
	return entries
}

// freeBytes is the room left on the disk that holds dir.
func freeBytes(dir string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}

// removeParts drops the spooled files once the zip holds them.
func removeParts(parts []videoPart) {
	for _, p := range parts {
		os.Remove(p.path)
	}
}
