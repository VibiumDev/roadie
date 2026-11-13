package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/VibiumDev/roadie/internal/alsa"
	"github.com/VibiumDev/roadie/internal/audio"
	"github.com/VibiumDev/roadie/internal/devices"
	"github.com/VibiumDev/roadie/rtc"
	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/driver"
	"github.com/pion/mediadevices/pkg/driver/availability"
	"github.com/pion/mediadevices/pkg/driver/camera"
	"github.com/pion/mediadevices/pkg/driver/microphone"
	"github.com/pion/mediadevices/pkg/io/video"
	"github.com/pion/mediadevices/pkg/prop"
)

// CaptureStatus represents the current state of the capture device.
type CaptureStatus string

const (
	StatusConnected    CaptureStatus = "connected"
	StatusDisconnected CaptureStatus = "disconnected"
	StatusConnecting   CaptureStatus = "connecting"
	StatusNoSignal     CaptureStatus = "no_signal"
)

// FrameSource provides the latest captured JPEG frame and device status.
type FrameSource interface {
	Latest() []byte
	LatestRaw() []byte
	Status() CaptureStatus
	CropRect() image.Rectangle
	ContentRect() image.Rectangle
}

// FrameBuffer holds the most recent frame: the captured picture itself,
// and the cropped and uncropped JPEGs made from it. The JPEGs are made in
// the capture loop while someone is streaming, and otherwise only when
// asked for (a snapshot, a screenshot), so an idle Roadie encodes nothing.
type FrameBuffer struct {
	Mu       sync.RWMutex
	current  []byte // the cropped JPEG
	rawJPEG  []byte // the uncropped JPEG
	cropRect image.Rectangle
	// The picture the JPEGs come from, and which picture each JPEG is of.
	raw        *rawFrame
	rawSeq     uint64
	currentSeq uint64
	rawJPEGSeq uint64
	encMu      sync.Mutex // one JPEG encode at a time, when made on demand
	// Sinks take every captured picture as it arrives: the WebRTC encoder.
	sinkMu sync.RWMutex
	sinks  []rtc.FrameSink
	// Where the target's picture sits inside the captured frame. Tracked
	// even when autocrop is off: the frame is then served whole, but input
	// still has to land on the picture rather than on the letterbox bars.
	contentRect image.Rectangle
	// The size of the frames contentRect was detected in.
	contentFrame image.Point
	// Crops someone saved from a bright screen, one per way the target can
	// be turned, used instead of what is detected; which way it is turned
	// now; and the size of the frames being captured.
	savedCrops    map[string]SavedCrop
	orientation   string
	orientPending string
	orientSeen    int
	frameSize     image.Point
	status        CaptureStatus
	quality       atomic.Int32
	viewers       atomic.Int32
	fps           atomic.Int32
	width         atomic.Int32
	height        atomic.Int32
	// cropUse is the crop chosen: CropAuto (the default, ""), CropOff or
	// CropSaved.
	cropUse string
}

// The crops to choose from: trim what is black at the edges, nothing, or
// the crop saved for the way the target is turned (auto where there is
// none).
const (
	CropAuto  = "auto"
	CropOff   = "off"
	CropSaved = "saved"
)

// AddViewer increments the active viewer count.
func (fb *FrameBuffer) AddViewer() { fb.viewers.Add(1) }

// RemoveViewer decrements the active viewer count.
func (fb *FrameBuffer) RemoveViewer() { fb.viewers.Add(-1) }

// Viewers returns the number of active stream viewers.
func (fb *FrameBuffer) Viewers() int { return int(fb.viewers.Load()) }

// SetFPS sets the target framerate.
func (fb *FrameBuffer) SetFPS(n int) { fb.fps.Store(int32(n)) }

// FPS returns the target framerate. Defaults to 30 if never set.
func (fb *FrameBuffer) FPS() int {
	v := int(fb.fps.Load())
	if v == 0 {
		return 30
	}
	return v
}

// SetWidth sets the capture width.
func (fb *FrameBuffer) SetWidth(n int) { fb.width.Store(int32(n)) }

// Width returns the capture width. Defaults to 1920 if never set.
func (fb *FrameBuffer) Width() int {
	v := int(fb.width.Load())
	if v == 0 {
		return 1920
	}
	return v
}

// SetHeight sets the capture height.
func (fb *FrameBuffer) SetHeight(n int) { fb.height.Store(int32(n)) }

// Height returns the capture height. Defaults to 1080 if never set.
func (fb *FrameBuffer) Height() int {
	v := int(fb.height.Load())
	if v == 0 {
		return 1080
	}
	return v
}

// SetAutocrop chooses auto-crop (true) or no crop (false): the older,
// two-way form of SetCropUse.
func (fb *FrameBuffer) SetAutocrop(on bool) {
	if on {
		fb.SetCropUse(CropAuto)
	} else {
		fb.SetCropUse(CropOff)
	}
}

// Autocrop returns whether auto-crop is the crop chosen.
func (fb *FrameBuffer) Autocrop() bool { return fb.CropUse() == CropAuto }

// SetCropUse chooses the crop: CropAuto, CropOff or CropSaved.
func (fb *FrameBuffer) SetCropUse(use string) {
	fb.Mu.Lock()
	fb.cropUse = use
	fb.Mu.Unlock()
}

// CropUse returns the crop chosen.
func (fb *FrameBuffer) CropUse() string {
	fb.Mu.RLock()
	defer fb.Mu.RUnlock()
	if fb.cropUse == "" {
		return CropAuto
	}
	return fb.cropUse
}

// SetQuality sets the JPEG quality, clamped to [30, 95].
func (fb *FrameBuffer) SetQuality(q int) {
	if q < 30 {
		q = 30
	}
	if q > 95 {
		q = 95
	}
	fb.quality.Store(int32(q))
}

// Quality returns the current JPEG quality. Defaults to 80 if never set.
func (fb *FrameBuffer) Quality() int {
	v := int(fb.quality.Load())
	if v == 0 {
		return 80
	}
	return v
}

// rawFrame is one captured picture, held until the next one arrives and
// nothing is still encoding it; then its reader gets the memory back.
type rawFrame struct {
	img     image.Image
	release func()
	crop    image.Rectangle // what the cropped JPEG shows: the whole frame if no crop
	seq     uint64
	pins    int
	stale   bool
}

// captureEpoch is the clock frame timestamps count from.
var captureEpoch = time.Now()

// UpdateRaw keeps the latest picture and its crop and lets go of the one
// before. The JPEGs are now out of date until they are remade.
func (fb *FrameBuffer) UpdateRaw(img image.Image, release func(), crop image.Rectangle) {
	fb.Mu.Lock()
	prev := fb.raw
	fb.rawSeq++
	fb.raw = &rawFrame{img: img, release: release, crop: crop, seq: fb.rawSeq}
	fb.cropRect = crop
	var done bool
	if prev != nil {
		prev.stale = true
		done = prev.pins == 0
	}
	fb.Mu.Unlock()
	if done && prev.release != nil {
		prev.release()
	}
}

// pin holds the latest picture for an encode; unpin lets it go.
func (fb *FrameBuffer) pin() *rawFrame {
	fb.Mu.Lock()
	f := fb.raw
	if f != nil {
		f.pins++
	}
	fb.Mu.Unlock()
	return f
}

func (fb *FrameBuffer) unpin(f *rawFrame) {
	fb.Mu.Lock()
	f.pins--
	done := f.stale && f.pins == 0
	fb.Mu.Unlock()
	if done && f.release != nil {
		f.release()
	}
}

// Detach copies the latest picture onto the heap if it still lives in a
// capture driver's buffer, and waits for anyone encoding it, so the driver
// can take its memory back. Called when capture stops.
func (fb *FrameBuffer) Detach() {
	fb.Mu.Lock()
	f := fb.raw
	fb.Mu.Unlock()
	if f == nil {
		return
	}
	cp, ok := f.img.(interface{ Copy() image.Image })
	if !ok {
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		fb.Mu.Lock()
		if fb.raw != f {
			fb.Mu.Unlock()
			return
		}
		if f.pins == 0 {
			img, release := cp.Copy(), f.release
			fb.raw = &rawFrame{img: img, crop: f.crop, seq: f.seq}
			f.stale = true
			fb.Mu.Unlock()
			if release != nil {
				release()
			}
			return
		}
		fb.Mu.Unlock()
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// AddSink starts handing every captured picture to s, in the capture loop.
func (fb *FrameBuffer) AddSink(s rtc.FrameSink) {
	fb.sinkMu.Lock()
	fb.sinks = append(fb.sinks, s)
	fb.sinkMu.Unlock()
}

// RemoveSink stops that.
func (fb *FrameBuffer) RemoveSink(s rtc.FrameSink) {
	fb.sinkMu.Lock()
	kept := fb.sinks[:0]
	for _, k := range fb.sinks {
		if k != s {
			kept = append(kept, k)
		}
	}
	fb.sinks = kept
	fb.sinkMu.Unlock()
}

// deliver hands a picture to the sinks.
func (fb *FrameBuffer) deliver(img image.Image, crop image.Rectangle, ts time.Duration) {
	fb.sinkMu.RLock()
	sinks := fb.sinks
	fb.sinkMu.RUnlock()
	for _, s := range sinks {
		s.Frame(img, crop, ts)
	}
}

// Update replaces the current frame with a JPEG from elsewhere (an HTTP
// source), cropped and uncropped alike.
func (fb *FrameBuffer) Update(frame []byte) {
	fb.Mu.Lock()
	fb.current = frame
	fb.rawJPEG = frame
	fb.currentSeq, fb.rawJPEGSeq = fb.rawSeq, fb.rawSeq
	fb.Mu.Unlock()
}

// UpdateBoth replaces both cropped and raw JPEGs, made from the latest
// picture.
func (fb *FrameBuffer) UpdateBoth(cropped, raw []byte, rect image.Rectangle) {
	fb.Mu.Lock()
	fb.current = cropped
	fb.rawJPEG = raw
	fb.cropRect = rect
	fb.currentSeq, fb.rawJPEGSeq = fb.rawSeq, fb.rawSeq
	fb.Mu.Unlock()
}

// UpdateCropped replaces only the cropped JPEG, leaving raw untouched.
func (fb *FrameBuffer) UpdateCropped(cropped []byte, rect image.Rectangle) {
	fb.Mu.Lock()
	fb.current = cropped
	fb.cropRect = rect
	fb.currentSeq = fb.rawSeq
	fb.Mu.Unlock()
}

// Latest returns the cropped JPEG of the most recent frame, or nil if none
// yet, making it now if the capture loop did not.
func (fb *FrameBuffer) Latest() []byte {
	fb.Mu.RLock()
	frame, seq, raw := fb.current, fb.currentSeq, fb.raw
	fb.Mu.RUnlock()
	if raw == nil || (frame != nil && seq == raw.seq) {
		return frame
	}
	return fb.encodeLatest(false)
}

// LatestRaw returns the uncropped JPEG of the most recent frame, or nil if
// none yet, making it now if the capture loop did not.
func (fb *FrameBuffer) LatestRaw() []byte {
	fb.Mu.RLock()
	frame, seq, raw := fb.rawJPEG, fb.rawJPEGSeq, fb.raw
	fb.Mu.RUnlock()
	if raw == nil || (frame != nil && seq == raw.seq) {
		return frame
	}
	return fb.encodeLatest(true)
}

// LatestAt is the JPEG of the most recent picture at a quality of the
// caller's own, cropped or whole, made now and not kept: for screenshots,
// which want a different quality than the stream. Nil when there is no
// picture to encode (an HTTP source, or nothing captured yet).
func (fb *FrameBuffer) LatestAt(quality int, whole bool) []byte {
	f := fb.pin()
	if f == nil {
		return nil
	}
	defer fb.unpin(f)
	rect := f.img.Bounds()
	if !whole && !f.crop.Empty() {
		rect = f.crop
	}
	return encodeJPEG(f.img, rect, quality)
}

// encodeLatest makes the JPEG, whole or cropped, of the latest picture.
func (fb *FrameBuffer) encodeLatest(whole bool) []byte {
	fb.encMu.Lock()
	defer fb.encMu.Unlock()
	f := fb.pin()
	if f == nil {
		return nil
	}
	defer fb.unpin(f)
	// Another caller, or the loop, may have made it meanwhile.
	fb.Mu.RLock()
	if whole && fb.rawJPEGSeq == f.seq && fb.rawJPEG != nil {
		frame := fb.rawJPEG
		fb.Mu.RUnlock()
		return frame
	}
	if !whole && fb.currentSeq == f.seq && fb.current != nil {
		frame := fb.current
		fb.Mu.RUnlock()
		return frame
	}
	fb.Mu.RUnlock()
	rect := f.img.Bounds()
	if !whole && !f.crop.Empty() {
		rect = f.crop
	}
	data := encodeJPEG(f.img, rect, fb.Quality())
	fb.Mu.Lock()
	if whole {
		fb.rawJPEG, fb.rawJPEGSeq = data, f.seq
	} else {
		fb.current, fb.currentSeq = data, f.seq
	}
	fb.Mu.Unlock()
	return data
}

// encodeJPEG makes the JPEG of a rectangle of a picture.
func encodeJPEG(img image.Image, rect image.Rectangle, quality int) []byte {
	if p, ok := img.(interface {
		YCbCr(image.Rectangle) *image.YCbCr
	}); ok {
		// Packed pixels take image/jpeg's slow path; planar ones its fast one.
		img = p.YCbCr(rect)
	} else if rect != img.Bounds() {
		img = subImage(img, rect)
	}
	b := bufPool.Get().(*bytes.Buffer)
	b.Reset()
	defer bufPool.Put(b)
	if err := jpeg.Encode(b, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil
	}
	out := make([]byte, b.Len())
	copy(out, b.Bytes())
	return out
}

// subImage is the rectangle of a picture, shared when the picture allows it.
func subImage(img image.Image, r image.Rectangle) image.Image {
	if si, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return si.SubImage(r)
	}
	dst := image.NewRGBA(r)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			dst.Set(x, y, img.At(x, y))
		}
	}
	return dst
}

// SetContentRect records where the picture sits in the captured frame, and
// from its shape which way the target is turned.
func (fb *FrameBuffer) SetContentRect(rect image.Rectangle) {
	fb.Mu.Lock()
	fb.contentRect, fb.contentFrame = rect, fb.frameSize
	fb.trackOrientation(ShapeOf(rect, fb.frameSize))
	fb.Mu.Unlock()
}

// shapeOf names which way a picture is turned, from where it sits in a frame
// of that size. A target's screen spans at least half the frame one way or
// the other: anything smaller (a dark app's one bright corner, a dongle's
// own message) says nothing.
func ShapeOf(r image.Rectangle, frame image.Point) string {
	if frame.X > 0 && r.Dx()*2 < frame.X && r.Dy()*2 < frame.Y {
		return ""
	}
	return OrientationOf(r)
}

// shapeWatch says when to detect the picture's edges at once, rather than
// in the next second: for a second's frames after the picture changes
// shape, so a target that turned is seen turned in two frames (the two
// detections in a row trackOrientation wants), and a crop that has to follow
// settles. An iPhone's picture changes shape half a second after it is
// told to turn, then turns inside it for half a second more. The shape it
// watches is brightBox's, cheap enough to take of every frame.
type ShapeWatch struct {
	last string // the shape last seen
	left int    // frames still to detect
}

// see takes a frame's shape, and reports whether to detect its edges.
func (w *ShapeWatch) See(shape string, frames int) bool {
	if shape != "" && shape != w.last {
		if w.last != "" {
			w.left = frames
		}
		w.last = shape
	}
	if w.left == 0 {
		return false
	}
	w.left--
	return true
}

// trackOrientation follows the picture's shape, but only once two
// detections in a row agree, so one odd frame can't swap the saved crops.
// That goes for the first one too: the first frames from a dongle that has
// just started can show anything.
func (fb *FrameBuffer) trackOrientation(o string) {
	switch {
	case o == "" || o == fb.orientation:
		fb.orientPending, fb.orientSeen = "", 0
	default:
		if o == fb.orientPending {
			fb.orientSeen++
		} else {
			fb.orientPending, fb.orientSeen = o, 1
		}
		if fb.orientSeen >= cropAgreeCount {
			fb.orientation, fb.orientPending, fb.orientSeen = o, "", 0
		}
	}
}

// orientationOf names a picture's shape. A dark app can shrink what is
// detected, but not turn a tall picture wide; a square-ish one says
// nothing.
func OrientationOf(r image.Rectangle) string {
	switch w, h := r.Dx(), r.Dy(); {
	case w*5 > h*6:
		return OrientationLandscape
	case h*5 > w*6:
		return OrientationPortrait
	}
	return ""
}

const (
	OrientationPortrait  = "portrait"
	OrientationLandscape = "landscape"
)

// presumeOrientation starts out turned that way before anything is
// detected, so a saved crop applies from the first frame after a restart.
// Detection still turns it, as usual.
func (fb *FrameBuffer) PresumeOrientation(o string) {
	fb.Mu.Lock()
	if fb.orientation == "" {
		fb.orientation = o
	}
	fb.Mu.Unlock()
}

// Orientation is which way the target's picture is turned, as last
// detected; empty before anything was.
func (fb *FrameBuffer) Orientation() string {
	fb.Mu.RLock()
	defer fb.Mu.RUnlock()
	return fb.orientation
}

// SetOrientation says which way the picture is turned, as a detection
// would have; for tests that stand in for one.
func (fb *FrameBuffer) SetOrientation(o string) {
	fb.Mu.Lock()
	defer fb.Mu.Unlock()
	fb.orientation = o
}

// ContentRect returns where the picture sits in the captured frame: the
// saved crop when it is in use, otherwise what was last detected.
func (fb *FrameBuffer) ContentRect() image.Rectangle {
	fb.Mu.RLock()
	defer fb.Mu.RUnlock()
	if r, ok := fb.savedInUse(fb.frameSize); ok {
		return r
	}
	return fb.detected()
}

// savedInUse returns the saved crop when it is the crop chosen and there is
// one for the way the target is turned, at this frame size. Call with mu
// held.
func (fb *FrameBuffer) savedInUse(frame image.Point) (image.Rectangle, bool) {
	if fb.cropUse != CropSaved {
		return image.Rectangle{}, false
	}
	return fb.savedCrops[fb.orientation].appliesTo(frame)
}

// DetectedRect returns the picture's edges as last detected, ignoring any
// saved crop: what saving a crop now would save.
func (fb *FrameBuffer) DetectedRect() image.Rectangle {
	fb.Mu.RLock()
	defer fb.Mu.RUnlock()
	return fb.detected()
}

// detected is the last detection, in the frames now being captured: one
// made before the capture resolution changed is scaled to the new one
// until the next detection replaces it. Call with mu held.
func (fb *FrameBuffer) detected() image.Rectangle {
	return scaleRect(fb.contentRect, fb.contentFrame, fb.frameSize)
}

// SavedCrop is a crop saved from a bright screen, and the size of the
// frames it was taken from.
type SavedCrop struct {
	Rect  image.Rectangle
	Frame image.Point
}

// appliesTo returns the crop for frames of the given size: the saved one,
// scaled to it.
func (c SavedCrop) appliesTo(frame image.Point) (image.Rectangle, bool) {
	if c.Rect.Empty() || frame.X <= 0 || frame.Y <= 0 {
		return image.Rectangle{}, false
	}
	r := scaleRect(c.Rect, c.Frame, frame)
	return r, !r.Empty() && r.In(image.Rectangle{Max: frame})
}

// scaleRect maps a rectangle in frames of one size to frames of another.
// A capture dongle scales the whole picture to whatever size it is asked
// for (at 1280x720, and stretched at 720x480), so where the target's
// screen sits scales with it. Either size unknown: r as it is.
func scaleRect(r image.Rectangle, from, to image.Point) image.Rectangle {
	if from == to || from.X <= 0 || from.Y <= 0 || to.X <= 0 || to.Y <= 0 {
		return r
	}
	x := func(v int) int { return int(math.Round(float64(v) * float64(to.X) / float64(from.X))) }
	y := func(v int) int { return int(math.Round(float64(v) * float64(to.Y) / float64(from.Y))) }
	return image.Rect(x(r.Min.X), y(r.Min.Y), x(r.Max.X), y(r.Max.Y)).Intersect(image.Rectangle{Max: to})
}

// SetSavedCrop sets the crop to use, instead of detection, while the
// target is turned that way; the zero SavedCrop clears it.
func (fb *FrameBuffer) SetSavedCrop(orientation string, c SavedCrop) {
	fb.Mu.Lock()
	defer fb.Mu.Unlock()
	if c.Rect.Empty() {
		delete(fb.savedCrops, orientation)
		return
	}
	if fb.savedCrops == nil {
		fb.savedCrops = map[string]SavedCrop{}
	}
	fb.savedCrops[orientation] = c
}

// SavedCrops returns the saved crops by orientation.
func (fb *FrameBuffer) SavedCrops() map[string]SavedCrop {
	fb.Mu.RLock()
	defer fb.Mu.RUnlock()
	out := make(map[string]SavedCrop, len(fb.savedCrops))
	for o, c := range fb.savedCrops {
		out[o] = c
	}
	return out
}

// savedCropFor returns the saved crop if it is in use for frames of bounds.
func (fb *FrameBuffer) SavedCropFor(bounds image.Rectangle) (image.Rectangle, bool) {
	fb.Mu.RLock()
	defer fb.Mu.RUnlock()
	return fb.savedInUse(bounds.Size())
}

// autocropping is whether the crop is detected now: auto-crop chosen, or
// the saved crop chosen with none for the way the target is turned.
func (fb *FrameBuffer) autocropping() bool {
	return fb.CropMode() == CropAuto
}

// setFrameSize records the size of the frames being captured.
func (fb *FrameBuffer) SetFrameSize(p image.Point) {
	fb.Mu.Lock()
	fb.frameSize = p
	fb.Mu.Unlock()
}

// FrameSize returns the size of the frames being captured, zero before the
// first one.
func (fb *FrameBuffer) FrameSize() image.Point {
	fb.Mu.RLock()
	defer fb.Mu.RUnlock()
	return fb.frameSize
}

// CropMode says what decides the crop now: CropSaved, CropAuto or CropOff.
// It is the crop chosen, except that the saved crop chosen with none for
// the way the target is turned (or for this frame size) means auto.
func (fb *FrameBuffer) CropMode() string {
	fb.Mu.RLock()
	defer fb.Mu.RUnlock()
	if _, ok := fb.savedInUse(fb.frameSize); ok {
		return CropSaved
	}
	if fb.cropUse == CropOff {
		return CropOff
	}
	return CropAuto
}

// CropRect returns the current crop rectangle.
func (fb *FrameBuffer) CropRect() image.Rectangle {
	fb.Mu.RLock()
	r := fb.cropRect
	fb.Mu.RUnlock()
	return r
}

// SetStatus updates the capture status.
func (fb *FrameBuffer) SetStatus(s CaptureStatus) {
	fb.Mu.Lock()
	fb.status = s
	fb.Mu.Unlock()
}

// Status returns the current capture status.
func (fb *FrameBuffer) Status() CaptureStatus {
	fb.Mu.RLock()
	s := fb.status
	fb.Mu.RUnlock()
	return s
}

// Clear nils the frame and sets status to disconnected atomically.
func (fb *FrameBuffer) Clear() {
	fb.Mu.Lock()
	if f := fb.raw; f != nil {
		f.stale = true
		if f.pins == 0 && f.release != nil {
			defer f.release()
		}
		fb.raw = nil
	}
	fb.current = nil
	fb.rawJPEG = nil
	fb.cropRect = image.Rectangle{}
	fb.status = StatusDisconnected
	fb.Mu.Unlock()
}

// DetectDevice enumerates video devices and returns the best capture device
// candidate. It skips built-in cameras (FaceTime, iPhone, MacBook, iMac,
// integrated, built-in) and prefers external/USB/HDMI devices. If no device
// matches the prefer keywords, the first non-skipped device is returned as a
// fallback. If filter is non-empty, it matches as a substring against device
// names (bypassing the skip/prefer heuristic).
// cameraName returns the name to show for a camera driver, and false when
// the driver does not identify itself.
//
// A Raspberry Pi registers its ISP and codec nodes as cameras, and some of
// them report no name at all. The label is "name;path", so an unnamed one
// reduces to punctuation -- which then gets offered as a --device choice
// and handed to the capture monitor, which spends the rest of the session
// failing to open a device called ";".
func cameraName(info driver.Info) (string, bool) {
	name := info.Name
	if name == "" {
		name = info.Label
	}
	if strings.Trim(name, " ;:") == "" {
		return "", false
	}
	return name, true
}

func DetectDevice(filter string) (devices.DeviceInfo, error) {
	drivers := driver.GetManager().Query(func(d driver.Driver) bool {
		return d.Info().DeviceType == driver.Camera
	})
	var devs []devices.DeviceInfo
	for _, d := range drivers {
		info := d.Info()
		if name, ok := cameraName(info); ok {
			devs = append(devs, devices.DeviceInfo{Name: name, Label: info.Label})
		}
	}
	return chooseDevice(devs, filter)
}

// chooseDevice picks the device to capture from: the first whose name
// contains filter, or without one, an external capture device. On-chip
// video blocks are never chosen: nothing can be captured from them, and
// while a dongle is briefly off the bus (a USB reset) they would be all
// that is left to fall back on.
func chooseDevice(devs []devices.DeviceInfo, filter string) (devices.DeviceInfo, error) {
	skipKeywords := []string{"facetime", "iphone", "macbook", "imac", "integrated", "built-in"}
	preferKeywords := []string{"usb", "hdmi", "capture", "video"}

	var candidates []devices.DeviceInfo
	for _, d := range devs {
		if !onChipVideo(d.Name) {
			candidates = append(candidates, d)
		}
	}
	if len(candidates) == 0 {
		return devices.DeviceInfo{}, fmt.Errorf("no video capture devices found")
	}

	var names []string
	for _, c := range candidates {
		names = append(names, c.Name)
	}

	// If a filter is provided, find the first match.
	if filter != "" {
		for _, c := range candidates {
			if strings.Contains(strings.ToLower(c.Name), strings.ToLower(filter)) {
				return c, nil
			}
		}
		return devices.DeviceInfo{}, fmt.Errorf("no device matching %q found\nAvailable devices: %s", filter, strings.Join(names, ", "))
	}

	skipped := func(c devices.DeviceInfo) bool {
		lower := strings.ToLower(c.Name)
		for _, kw := range skipKeywords {
			if strings.Contains(lower, kw) {
				return true
			}
		}
		return false
	}

	// Auto-detect: skip built-in, prefer external capture devices.
	for _, c := range candidates {
		if skipped(c) {
			continue
		}
		lower := strings.ToLower(c.Name)
		for _, kw := range preferKeywords {
			if strings.Contains(lower, kw) {
				return c, nil
			}
		}
	}

	// Fallback: return first non-skipped device.
	for _, c := range candidates {
		if !skipped(c) {
			return c, nil
		}
	}

	return devices.DeviceInfo{}, fmt.Errorf("no external capture device found\nAvailable devices: %s", strings.Join(names, ", "))
}

// CaptureCandidates returns the distinct names of connected video devices
// that DetectDevice's auto-detect would consider, i.e. everything that isn't
// a built-in camera. Each dongle registers several V4L2 nodes, so names are
// deduplicated — the result counts physical devices, not nodes.
func CaptureCandidates() []string {
	drivers := driver.GetManager().Query(func(d driver.Driver) bool {
		return d.Info().DeviceType == driver.Camera
	})

	skipKeywords := []string{"facetime", "iphone", "macbook", "imac", "integrated", "built-in"}

	seen := make(map[string]bool)
	var names []string
	for _, d := range drivers {
		info := d.Info()
		name, ok := cameraName(info)
		if !ok {
			continue
		}
		if onChipVideo(name) {
			continue
		}
		lower := strings.ToLower(name)
		skip := false
		for _, kw := range skipKeywords {
			if strings.Contains(lower, kw) {
				skip = true
				break
			}
		}
		if skip || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// onChipVideo reports whether a video device is a block on the host's own
// silicon rather than something to capture from. A Pi enumerates its ISP
// and its H.264 codec as video devices -- "bcm2835-isp;platform:bcm2835-isp"
// -- whether or not any camera exists. They take frames in and hand frames
// back, so no one can ever stream a target from them, and listing them
// only invites the question of what they are.
func onChipVideo(name string) bool {
	return strings.Contains(name, ";platform:")
}

// ListDevices returns a list of all video device names for diagnostic output.
func ListDevices() []string {
	devs := mediadevices.EnumerateDevices()
	var names []string
	seen := make(map[string]bool)
	for _, d := range devs {
		if d.Kind != mediadevices.VideoInput {
			continue
		}
		// Look up the friendly name from the driver.
		info := driver.Info{Label: d.Label}
		drivers := driver.GetManager().Query(func(drv driver.Driver) bool {
			return drv.Info().Label == d.Label
		})
		if len(drivers) > 0 {
			info = drivers[0].Info()
		}
		name, ok := cameraName(info)
		if !ok || seen[name] || onChipVideo(name) {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// captureNodes returns the nodes to try for dev, in order: the one chosen,
// then the same device's others by label. A capture dongle presents more
// than one node under one name -- one that streams video, one that carries
// only metadata -- with different labels, and the driver list comes in no
// fixed order, so the node chosen is as likely to be the one with nothing
// to stream.
func captureNodes(drivers []driver.Driver, dev devices.DeviceInfo) []driver.Driver {
	var nodes []driver.Driver
	for _, d := range drivers {
		info := d.Info()
		if info.DeviceType != driver.Camera {
			continue
		}
		if name, ok := cameraName(info); info.Label == dev.Label || ok && name == dev.Name {
			nodes = append(nodes, d)
		}
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i].Info().Label, nodes[j].Info().Label
		if (a == dev.Label) != (b == dev.Label) {
			return a == dev.Label
		}
		return a < b
	})
	return nodes
}

// nativeNodes returns the V4L2 nodes to try for dev, in captureNodes'
// order. A device the driver list does not hold has its own node alone.
func nativeNodes(drivers []driver.Driver, dev devices.DeviceInfo) []string {
	var nodes []string
	add := func(label string) {
		node := devices.DeviceInfo{Label: label}.VideoNode()
		for _, have := range nodes {
			if have == node {
				return
			}
		}
		if node != "" {
			nodes = append(nodes, node)
		}
	}
	add(dev.Label)
	for _, d := range captureNodes(drivers, dev) {
		add(d.Info().Label)
	}
	return nodes
}

// captureWarmup is how long after a capture starts its frames are shown
// but not used to find the picture: a dongle that has just started can send
// it torn for a few seconds (3 s seen).
const captureWarmup = 5 * time.Second

// maxConsecutiveBlack is the number of consecutive near-black frames before
// the capture goroutine reports no signal.
const maxConsecutiveBlack = 10

// blackThreshold is the average pixel brightness (0-255) below which a frame
// is considered black/no-signal.
const blackThreshold = 5

// isBlackFrame returns true if every sampled pixel is below blackThreshold.
func isBlackFrame(img image.Image) bool {
	bounds := img.Bounds()
	step := 1
	if w := bounds.Dx(); w > 100 {
		step = w / 50
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			brightness := (r>>8 + g>>8 + b>>8) / 3
			if brightness >= blackThreshold {
				return false
			}
		}
	}
	return true
}

// sampleFingerprint computes a quick hash of sparsely sampled pixel values.
// Two frames with the same fingerprint are effectively identical.
func sampleFingerprint(img image.Image) uint64 {
	bounds := img.Bounds()
	step := 1
	if w := bounds.Dx(); w > 100 {
		step = w / 50
	}
	var h uint64
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			// FNV-1a-style mixing
			h ^= uint64(r>>8)<<16 | uint64(g>>8)<<8 | uint64(b>>8)
			h *= 0x100000001b3
		}
	}
	return h
}

// cropThreshold is the per-channel brightness (0-255) below which a pixel is
// considered black for crop-border detection. Set above 16 to handle
// limited-range YCbCr (where Y=16 is black) plus scaler transition pixels
// from HDMI capture devices.
const cropThreshold = 30

// majorCropAreaFraction is the minimum area change (as a fraction of total
// frame area) required to adopt a new crop rectangle.  This prevents
// frame-to-frame jitter while still reacting to major transitions like
// blank→signal, portrait↔landscape, and signal loss.
const majorCropAreaFraction = 0.20

// bufPool reuses bytes.Buffers across frames to reduce GC pressure on ARM.
var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// detectContentRect scans inward from each edge of img and returns the
// bounding rectangle of the non-black content area. A pixel is "black" if
// all of its R, G, B channels (scaled to 8-bit) are below threshold.
// If the entire image is non-black (or all-black), the full bounds are returned.
func detectContentRect(img image.Image, threshold uint8) image.Rectangle {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return bounds
	}

	// Number of samples per row/column scan.
	step := w / 100
	if step < 1 {
		step = 1
	}
	stepY := h / 100
	if stepY < 1 {
		stepY = 1
	}

	thresh := uint32(threshold)

	isBlackPixel := func(x, y int) bool {
		r, g, b, _ := img.At(x, y).RGBA()
		// RGBA returns 16-bit values; scale to 8-bit for comparison.
		return r>>8 < thresh && g>>8 < thresh && b>>8 < thresh
	}

	// rowBlack returns true if all sampled pixels in row y are black.
	rowBlack := func(y int) bool {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			if !isBlackPixel(x, y) {
				return false
			}
		}
		return true
	}

	// colBlack returns true if all sampled pixels in column x are black.
	colBlack := func(x int) bool {
		for y := bounds.Min.Y; y < bounds.Max.Y; y += stepY {
			if !isBlackPixel(x, y) {
				return false
			}
		}
		return true
	}

	top := bounds.Min.Y
	for top < bounds.Max.Y && rowBlack(top) {
		top++
	}
	bottom := bounds.Max.Y
	for bottom > top && rowBlack(bottom-1) {
		bottom--
	}
	left := bounds.Min.X
	for left < bounds.Max.X && colBlack(left) {
		left++
	}
	right := bounds.Max.X
	for right > left && colBlack(right-1) {
		right--
	}

	// If everything is black, return full bounds (no crop).
	if top >= bottom || left >= right {
		return bounds
	}

	return image.Rect(left, top, right, bottom)
}

// cropAgreeCount is how many detections in a row must agree on a new crop
// before it replaces the one in use.
const cropAgreeCount = 2

// cropsAgree reports whether two detected rectangles are close enough to be
// the same one, allowing for a few pixels of noise at the content edge.
func cropsAgree(a, b, fullBounds image.Rectangle) bool {
	if b == (image.Rectangle{}) {
		return false
	}
	tolerance := fullBounds.Dx() / 50 // 2% of the frame width
	if tolerance < 4 {
		tolerance = 4
	}
	off := func(x, y int) bool {
		d := x - y
		if d < 0 {
			d = -d
		}
		return d > tolerance
	}
	return !(off(a.Min.X, b.Min.X) || off(a.Min.Y, b.Min.Y) ||
		off(a.Max.X, b.Max.X) || off(a.Max.Y, b.Max.Y))
}

// isMajorCropChange returns true when the new crop rectangle represents a
// significant change from the active crop.  It triggers on:
//   - first detection (activeCrop is zero)
//   - area change > majorCropAreaFraction of full frame area
//   - aspect-ratio flip (landscape ↔ portrait)
func isMajorCropChange(activeCrop, newRect, fullBounds image.Rectangle) bool {
	if activeCrop == (image.Rectangle{}) {
		return true
	}
	totalArea := fullBounds.Dx() * fullBounds.Dy()
	if totalArea == 0 {
		return false
	}
	oldArea := activeCrop.Dx() * activeCrop.Dy()
	newArea := newRect.Dx() * newRect.Dy()
	diff := oldArea - newArea
	if diff < 0 {
		diff = -diff
	}
	if float64(diff)/float64(totalArea) > majorCropAreaFraction {
		return true
	}
	// Aspect-ratio flip: landscape ↔ portrait.
	oldLandscape := activeCrop.Dx() > activeCrop.Dy()
	newLandscape := newRect.Dx() > newRect.Dy()
	return oldLandscape != newLandscape
}

// InitObserver starts the platform device observer (macOS only) which
// automatically registers/unregisters camera drivers on hotplug.
// On Linux this is a no-op; the CaptureManager re-scans devices each loop iteration instead.
func InitObserver() error {
	err := camera.StartObserver()
	// Only macOS implements the observer. Everywhere else the library
	// says so with a sentinel, which is an answer rather than a failure:
	// warning about it on every Linux start reads like something broke.
	if errors.Is(err, availability.ErrUnimplemented) {
		return nil
	}
	return err
}

// StartCapture opens the device matching dev and runs a goroutine that
// reads frames, JPEG-encodes them, and stores them in buf.
// The returned channel closes when the goroutine exits (device disconnected or context cancelled).
// frameReader is a source of frames: the dongle through V4L2, or through
// mediadevices. Each frame comes with a release to call when done with it.
type frameReader interface {
	Read() (image.Image, func(), error)
	Close() error
}

// errNoNativeCapture means the V4L2 path does not apply to this device or
// host, and mediadevices is used instead.
var errNoNativeCapture = errors.New("native capture not available")

// mediadevicesReader is the mediadevices camera driver as a frameReader.
type mediadevicesReader struct {
	reader video.Reader
	driver driver.Driver
}

func (m *mediadevicesReader) Read() (image.Image, func(), error) { return m.reader.Read() }
func (m *mediadevicesReader) Close() error                       { return m.driver.Close() }

func StartCapture(ctx context.Context, dev devices.DeviceInfo, width, height, fps int, buf *FrameBuffer) (<-chan struct{}, error) {
	var reader frameReader
	if r, err := openNativeCapture(dev, width, height, fps); err == nil {
		reader = r
	} else {
		if !errors.Is(err, errNoNativeCapture) {
			log.Printf("device %q: V4L2 capture not used: %v", dev.Name, err)
		}
		if reader, err = openMediadevicesCapture(dev, width, height, fps); err != nil {
			return nil, err
		}
	}
	return runCapture(ctx, dev, reader, fps, buf), nil
}

// openMediadevicesCapture opens the device through pion/mediadevices.
func openMediadevicesCapture(dev devices.DeviceInfo, width, height, fps int) (frameReader, error) {
	drivers := captureNodes(driver.GetManager().Query(func(driver.Driver) bool { return true }), dev)
	if len(drivers) == 0 {
		return nil, fmt.Errorf("device %q not found", dev.Name)
	}

	// Take the first node that opens and actually offers a format: picking
	// the metadata node means retrying a device that can never work while
	// the usable one sits next to it.
	var d driver.Driver
	var props []prop.Media
	var lastErr error
	for _, candidate := range drivers {
		if err := candidate.Open(); err != nil {
			lastErr = fmt.Errorf("failed to open device %q: %w", dev.Name, err)
			continue
		}
		if p := candidate.Properties(); len(p) > 0 {
			d, props = candidate, p
			break
		}
		candidate.Close()
		lastErr = fmt.Errorf("device %q reported no supported formats", dev.Name)
	}
	if d == nil {
		if lastErr == nil {
			lastErr = fmt.Errorf("device %q has no usable node", dev.Name)
		}
		return nil, lastErr
	}

	best := selectBestProp(props, width, height, fps)

	recorder, ok := d.(driver.VideoRecorder)
	if !ok {
		d.Close()
		return nil, fmt.Errorf("device %q does not support video recording", dev.Name)
	}

	reader, err := recorder.VideoRecord(best)
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("failed to start recording on %q: %w", dev.Name, err)
	}
	return &mediadevicesReader{reader: reader, driver: d}, nil
}

// runCapture reads frames until the context ends or the device fails.
func runCapture(ctx context.Context, dev devices.DeviceInfo, reader frameReader, fps int, buf *FrameBuffer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Last out: the driver's memory, once the last frame is off it.
		defer reader.Close()
		defer buf.Detach()

		var consecutiveErrors int
		var consecutiveStatic int
		var activeCrop image.Rectangle
		var pendingCrop image.Rectangle
		var pendingCropSeen int
		var usingSaved bool
		var frameCount int
		var prevBlack bool
		var prevFingerprint uint64
		var noSignalFP uint64 // the dongle's own No Signal screen, once recognised
		var blank blankFrame
		var shapes ShapeWatch
		started := time.Now()
		var detectedOnce bool
		jpegOpts := &jpeg.Options{}
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			jpegOpts.Quality = buf.Quality()
			img, release, err := reader.Read()
			if err != nil {
				consecutiveErrors++
				if consecutiveErrors >= audio.MaxConsecutiveErrors {
					log.Printf("device %q: %d consecutive read errors, treating as disconnected (%s)", dev.Name, consecutiveErrors, devices.ReadHeat())
					buf.Clear()
					return
				}
				continue
			}
			consecutiveErrors = 0
			frameCount++

			// The dongle's own No Signal screen (see nosignal.go) is never
			// shown: a dark frame goes out in its place, and counts as no
			// signal. It is recognised once, then by its fingerprint while
			// it stays.
			fp := sampleFingerprint(img)
			if fp != noSignalFP && fp != prevFingerprint && dongleNoSignal(img) {
				noSignalFP = fp
			}
			if noSignalFP != 0 && fp == noSignalFP {
				img = blank.of(img.Bounds())
			}

			// No-signal detection: a real no-signal produces identical
			// black frames.  Dark movie content varies frame-to-frame
			// due to compression noise, so we require both conditions.
			black := isBlackFrame(img)
			static := fp == prevFingerprint
			prevFingerprint = fp
			if black && static {
				consecutiveStatic++
				if consecutiveStatic == maxConsecutiveBlack {
					log.Printf("device %q: no signal (static black frames)", dev.Name)
					buf.SetStatus(StatusNoSignal)
				}
			} else {
				if consecutiveStatic >= maxConsecutiveBlack {
					log.Printf("device %q: signal restored", dev.Name)
					buf.SetStatus(StatusConnected)
				}
				consecutiveStatic = 0
			}

			// Crop detection: the first good frame, then every ~1 s, on signal
			// transitions, and when the picture changes shape.
			// Nor does a frame with nothing bright on it: the dongle's black
			// is about 16, not 0, which isBlackFrame does not count.
			box := BrightBox(img)
			dark := black || box.Empty()
			transitioned := dark != prevBlack
			prevBlack = dark
			fullBounds := img.Bounds()
			buf.SetFrameSize(fullBounds.Size())
			// A black frame says nothing about where the picture is or which
			// way it is turned (the dongle sends them for seconds after it
			// starts): keep what the last real one said. Nor do the first
			// seconds of frames: the dongle can send the picture torn
			// sideways for a few seconds after it starts, which reads as an
			// upright phone turned on its side.
			turned := shapes.See(ShapeOf(box, fullBounds.Size()), fps)
			detectNow := !dark && time.Since(started) >= captureWarmup &&
				(!detectedOnce || transitioned || turned || frameCount%fps == 0)
			if detectNow {
				detected := detectContentRect(img, cropThreshold)
				if detected != (image.Rectangle{}) {
					buf.SetContentRect(detected)
				}
				detectedOnce = true
			}
			if saved, ok := buf.SavedCropFor(fullBounds); ok {
				activeCrop, usingSaved = saved, true
				pendingCrop, pendingCropSeen = image.Rectangle{}, 0
			} else if usingSaved {
				// The saved crop was just cleared: detect afresh.
				activeCrop, usingSaved = image.Rectangle{}, false
			}
			if !usingSaved && buf.autocropping() {
				if frameCount == 1 || detectNow {
					rect := buf.DetectedRect()
					if isMajorCropChange(activeCrop, rect, fullBounds) {
						// One frame is a poor witness. When the target's own
						// screen goes dark near its edges, the detected
						// content shrinks to whatever is still bright, and
						// adopting that collapses the view -- and with it
						// where pointer coordinates land, since they are
						// measured against the crop. Wait for the same
						// rectangle twice running before believing it.
						if activeCrop == (image.Rectangle{}) {
							activeCrop = rect
							pendingCrop, pendingCropSeen = image.Rectangle{}, 0
						} else if cropsAgree(rect, pendingCrop, fullBounds) {
							pendingCropSeen++
							if pendingCropSeen >= cropAgreeCount {
								activeCrop = rect
								pendingCrop, pendingCropSeen = image.Rectangle{}, 0
							}
						} else {
							pendingCrop, pendingCropSeen = rect, 1
						}
					} else {
						pendingCrop, pendingCropSeen = image.Rectangle{}, 0
					}
				}
			} else if !usingSaved {
				activeCrop = image.Rectangle{}
			}

			// The picture is kept for snapshots made on demand, and handed
			// to the WebRTC encoder if a viewer is connected. JPEG is made
			// here only while someone is streaming it.
			rect := activeCrop
			if rect == (image.Rectangle{}) {
				rect = fullBounds
			}
			ts := time.Since(captureEpoch)
			if s, ok := img.(interface{ Stamp() time.Duration }); ok && s.Stamp() != 0 {
				ts = s.Stamp()
			}
			buf.UpdateRaw(img, release, rect)
			buf.deliver(img, rect, ts)
			if buf.Viewers() == 0 {
				continue
			}
			cropped := encodeJPEG(img, rect, jpegOpts.Quality)
			if cropped == nil {
				continue
			}
			switch {
			case rect == fullBounds:
				// No crop needed: one JPEG serves as both.
				buf.UpdateBoth(cropped, cropped, fullBounds)
			case frameCount%fps == 0:
				// The uncropped one, for the diagnostic endpoints, every ~1 s.
				if raw := encodeJPEG(img, fullBounds, jpegOpts.Quality); raw != nil {
					buf.UpdateBoth(cropped, raw, activeCrop)
				} else {
					buf.UpdateCropped(cropped, activeCrop)
				}
			default:
				buf.UpdateCropped(cropped, activeCrop)
			}
		}
	}()

	return done
}

// audioFilter is the filter to select the audio device with.
func (cm *CaptureManager) audioFilter() string {
	if cm.AudioFilter != "" {
		return cm.AudioFilter
	}
	return cm.Filter
}

// CaptureManager manages the lifecycle of capture devices, handling
// automatic detection, connection, and reconnection.
type CaptureManager struct {
	Filter string
	// AudioFilter selects the audio device. Empty falls back to Filter,
	// which is right for the common case: a capture dongle shows up under
	// one name for video and another that starts the same way for audio.
	AudioFilter    string
	Buf            *FrameBuffer
	AudioBroadcast *audio.AudioBroadcaster

	ctx             context.Context
	cancel          context.CancelFunc
	settingsChanged chan struct{}
	usbReset        chan chan error

	mu     sync.RWMutex
	device devices.DeviceInfo
}

// NewCaptureManager creates a new CaptureManager.
func NewCaptureManager(filter, audioFilter string, buf *FrameBuffer, ab *audio.AudioBroadcaster) *CaptureManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &CaptureManager{
		Filter:          filter,
		AudioFilter:     audioFilter,
		Buf:             buf,
		AudioBroadcast:  ab,
		ctx:             ctx,
		cancel:          cancel,
		settingsChanged: make(chan struct{}, 1),
		usbReset:        make(chan chan error),
	}
}

// NotifySettingsChanged signals the capture loop to restart with new settings.
func (cm *CaptureManager) NotifySettingsChanged() {
	select {
	case cm.settingsChanged <- struct{}{}:
	default:
	}
}

// Run loops: detect device → start capture → monitor → repeat.
// Capture runs continuously while a device is connected.
func (cm *CaptureManager) Run() {
	retryDelay := 2 * time.Second
	const maxRetryDelay = 30 * time.Second
	var stuck stuckWatch

	for {
		cm.Buf.SetStatus(StatusConnecting)

		// Re-scan /dev/video* and audio devices so the driver manager
		// picks up newly connected (or reconnected) devices on Linux.
		camera.Initialize()
		// Clear stale microphone drivers and re-register fresh ones.
		mgr := driver.GetManager()
		for _, d := range mgr.Query(driver.FilterAudioRecorder()) {
			mgr.Delete(d.ID())
		}
		microphone.Initialize()

		dev, err := DetectDevice(cm.Filter)
		if err != nil {
			select {
			case <-cm.ctx.Done():
				return
			case <-time.After(retryDelay):
				if retryDelay < maxRetryDelay {
					retryDelay = min(retryDelay*2, maxRetryDelay)
				}
				continue
			}
		}

		cm.mu.Lock()
		cm.device = dev
		cm.mu.Unlock()
		cm.Buf.SetStatus(StatusConnected)

		log.Printf("device %q detected, starting capture", dev.Name)

		captureCtx, captureCancel := context.WithCancel(cm.ctx)
		started := time.Now()
		done, err := StartCapture(captureCtx, dev, cm.Buf.Width(), cm.Buf.Height(), cm.Buf.FPS(), cm.Buf)
		if err != nil {
			captureCancel()
			log.Printf("failed to start capture on %q: %v", dev.Name, err)
			if stuck.ended(0, time.Now()) {
				cm.resetStuck(dev)
			}
			select {
			case <-cm.ctx.Done():
				return
			case <-time.After(retryDelay):
				if retryDelay < maxRetryDelay {
					retryDelay = min(retryDelay*2, maxRetryDelay)
				}
				continue
			}
		}

		retryDelay = 2 * time.Second
		cm.Buf.SetStatus(StatusConnected)

		// Start audio capture (optional — failure is non-fatal).
		var audioDone <-chan struct{}
		var audioCancel context.CancelFunc
		useDriver := false
		if cm.AudioBroadcast != nil {
			// The dongle's own sound card, straight from ALSA where there is
			// one; the microphone driver only where there is none (macOS,
			// capture that is not a USB dongle).
			audioCtx, ac := context.WithCancel(captureCtx)
			ad, desc, err := alsa.StartDongleAudio(audioCtx, dev, cm.AudioFilter, cm.AudioBroadcast)
			switch {
			case err == nil:
				audioCancel, audioDone = ac, ad
				log.Printf("audio capture started on %s", desc)
			case errors.Is(err, alsa.ErrNoDongleCard):
				ac()
				useDriver = true
			default:
				ac()
				log.Printf("audio capture failed: %v (continuing without audio)", err)
			}
		}
		if useDriver {
			audioDev, found := audio.DetectAudioDevice(cm.audioFilter())
			if found {
				audioCtx, ac := context.WithCancel(captureCtx)
				audioCancel = ac
				ad, err := audio.StartAudioCapture(audioCtx, audioDev, cm.AudioBroadcast)
				if err != nil {
					log.Printf("audio capture failed on %q: %v (continuing without audio)", audioDev.Name, err)
					audioCancel()
					audioCancel = nil
				} else {
					audioDone = ad
					log.Printf("audio capture started on %q", audioDev.Name)
				}
			}
		}

		// Monitor: wait for device disconnect, shutdown, or settings change.
		reason := cm.monitorCapture(done)
		log.Printf("capture monitor exited: reason=%s", reason)

		// Tear down capture + audio.
		captureCancel()
		if audioCancel != nil {
			audioCancel()
		}
		// Wait for capture + audio goroutines — with a timeout for USB reset,
		// where readers may be stuck on dead file descriptors.
		if audioDone != nil {
			select {
			case <-audioDone:
			case <-time.After(5 * time.Second):
				log.Printf("audio capture goroutine did not exit in time, proceeding")
			}
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			log.Printf("capture goroutine did not exit in time, proceeding")
		}

		switch reason {
		case "shutdown":
			return
		case "disconnect":
			log.Printf("device %q disconnected", dev.Name)
			if stuck.ended(time.Since(started), time.Now()) {
				cm.resetStuck(dev)
			}
			select {
			case <-cm.ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		case "settings":
			log.Printf("settings changed, restarting capture on %q", dev.Name)
		case "reset":
			cm.Buf.Clear()
			stuck = stuckWatch{}
			log.Printf("USB reset on %q, restarting capture", dev.Name)
		}
	}
}

// A dongle can get into a state where every capture fails at once, and
// reopening it never helps; unbinding and rebinding its USB device does.
// stuckWatch decides when the capture loop does that itself, as POST
// /api/capture/reset does: after resetAfter captures in a row that each
// ended within quickFail of starting. Repeated resets stay at least a gap
// apart, a gap that doubles up to maxResetGap while the dongle stays
// broken. A capture that lasts quickFail or longer clears it all.
type stuckWatch struct {
	fails     int
	gap       time.Duration
	lastReset time.Time
}

const (
	quickFail     = 15 * time.Second
	resetAfter    = 3
	firstResetGap = time.Minute
	maxResetGap   = 15 * time.Minute
)

// ended records a capture that ended by itself after running for lasted,
// and says whether to reset the dongle's USB now.
func (w *stuckWatch) ended(lasted time.Duration, now time.Time) bool {
	if lasted >= quickFail {
		*w = stuckWatch{}
		return false
	}
	w.fails++
	if w.fails < resetAfter {
		return false
	}
	if !w.lastReset.IsZero() && now.Sub(w.lastReset) < w.gap {
		return false
	}
	if w.lastReset.IsZero() {
		w.gap = firstResetGap
	} else {
		w.gap = min(2*w.gap, maxResetGap)
	}
	w.fails, w.lastReset = 0, now
	return true
}

// resetStuck unbinds and rebinds the USB device of a dongle whose captures
// keep failing.
func (cm *CaptureManager) resetStuck(dev devices.DeviceInfo) {
	node := dev.VideoNode()
	log.Printf("capture on %q failed %d times in a row; resetting its USB device (%s)", dev.Name, resetAfter, node)
	if err := devices.ResetCaptureUSB(node); err != nil {
		log.Printf("USB reset failed: %v", err)
		return
	}
	cm.Buf.Clear()
	log.Println("USB capture device reset complete")
}

// monitorCapture watches for device disconnect, shutdown, settings change, or USB reset.
// Returns the reason: "disconnect", "shutdown", "settings", or "reset".
func (cm *CaptureManager) monitorCapture(done <-chan struct{}) string {
	select {
	case <-cm.ctx.Done():
		return "shutdown"
	case <-done:
		return "disconnect"
	case <-cm.settingsChanged:
		return "settings"
	case result := <-cm.usbReset:
		cm.mu.RLock()
		node := cm.device.VideoNode()
		cm.mu.RUnlock()
		log.Printf("resetting USB capture device (%s)", node)
		err := devices.ResetCaptureUSB(node)
		if err != nil {
			log.Printf("USB reset failed: %v", err)
		} else {
			log.Println("USB capture device reset complete")
		}
		result <- err
		return "reset"
	}
}

// ResetUSB performs a USB unbind/rebind on the capture device, forcing
// HDMI re-negotiation. The request is handled by the capture loop to
// ensure proper sequencing: unbind → rebind → re-detect.
func (cm *CaptureManager) ResetUSB() error {
	result := make(chan error, 1)
	select {
	case cm.usbReset <- result:
		return <-result
	case <-cm.ctx.Done():
		return cm.ctx.Err()
	}
}

// Shutdown stops the capture manager loop.
func (cm *CaptureManager) Shutdown() {
	cm.cancel()
}

// Device returns the current device name (empty if none connected).
func (cm *CaptureManager) Device() string {
	cm.mu.RLock()
	d := cm.device.Name
	cm.mu.RUnlock()
	return d
}

// selectBestProp picks the prop.Media from the device's capabilities that
// best matches the requested width, height, and fps.
func selectBestProp(props []prop.Media, width, height, fps int) prop.Media {
	best := props[0]
	bestDist := mediaDist(best, width, height, fps)

	for _, p := range props[1:] {
		d := mediaDist(p, width, height, fps)
		if d < bestDist {
			best = p
			bestDist = d
		}
	}

	return best
}

// mediaDist computes a simple distance metric between a media property and
// desired width/height/fps (lower is better).
func mediaDist(p prop.Media, width, height, fps int) float64 {
	dw := float64(p.Width - width)
	dh := float64(p.Height - height)
	df := float64(p.FrameRate) - float64(fps)
	return math.Abs(dw) + math.Abs(dh) + math.Abs(df)*10
}
