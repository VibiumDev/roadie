package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/driver"
	"github.com/pion/mediadevices/pkg/driver/camera"
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
}

// FrameBuffer is a thread-safe holder for the most recent JPEG frame.
type FrameBuffer struct {
	mu       sync.RWMutex
	current  []byte
	raw      []byte
	cropRect image.Rectangle
	status   CaptureStatus
}

// Update replaces the current frame (sets both cropped and raw to the same data).
func (fb *FrameBuffer) Update(frame []byte) {
	fb.mu.Lock()
	fb.current = frame
	fb.raw = frame
	fb.mu.Unlock()
}

// UpdateBoth replaces both cropped and raw frames atomically.
func (fb *FrameBuffer) UpdateBoth(cropped, raw []byte, rect image.Rectangle) {
	fb.mu.Lock()
	fb.current = cropped
	fb.raw = raw
	fb.cropRect = rect
	fb.mu.Unlock()
}

// Latest returns the most recent cropped JPEG frame, or nil if none yet.
func (fb *FrameBuffer) Latest() []byte {
	fb.mu.RLock()
	frame := fb.current
	fb.mu.RUnlock()
	return frame
}

// LatestRaw returns the most recent uncropped JPEG frame, or nil if none yet.
func (fb *FrameBuffer) LatestRaw() []byte {
	fb.mu.RLock()
	frame := fb.raw
	fb.mu.RUnlock()
	return frame
}

// CropRect returns the current crop rectangle.
func (fb *FrameBuffer) CropRect() image.Rectangle {
	fb.mu.RLock()
	r := fb.cropRect
	fb.mu.RUnlock()
	return r
}

// SetStatus updates the capture status.
func (fb *FrameBuffer) SetStatus(s CaptureStatus) {
	fb.mu.Lock()
	fb.status = s
	fb.mu.Unlock()
}

// Status returns the current capture status.
func (fb *FrameBuffer) Status() CaptureStatus {
	fb.mu.RLock()
	s := fb.status
	fb.mu.RUnlock()
	return s
}

// Clear nils the frame and sets status to disconnected atomically.
func (fb *FrameBuffer) Clear() {
	fb.mu.Lock()
	fb.current = nil
	fb.raw = nil
	fb.cropRect = image.Rectangle{}
	fb.status = StatusDisconnected
	fb.mu.Unlock()
}

// deviceInfo holds both the friendly name and the internal label (UID) of a device.
type deviceInfo struct {
	Name  string // friendly name (e.g. "USB Video")
	Label string // internal UID used for driver matching
}

// DetectDevice enumerates video devices and returns the best capture device
// candidate. It skips built-in cameras (FaceTime, iPhone) and prefers
// external/USB/HDMI devices. If filter is non-empty, it matches as a
// substring against device names.
func DetectDevice(filter string) (deviceInfo, error) {
	drivers := driver.GetManager().Query(func(d driver.Driver) bool {
		return d.Info().DeviceType == driver.Camera
	})

	if len(drivers) == 0 {
		return deviceInfo{}, fmt.Errorf("no video capture devices found")
	}

	skipKeywords := []string{"facetime", "iphone"}
	preferKeywords := []string{"usb", "hdmi", "capture", "video"}

	type candidate struct {
		info deviceInfo
	}
	var candidates []candidate
	for _, d := range drivers {
		info := d.Info()
		name := info.Name
		if name == "" {
			name = info.Label
		}
		candidates = append(candidates, candidate{
			info: deviceInfo{Name: name, Label: info.Label},
		})
	}

	var names []string
	for _, c := range candidates {
		names = append(names, c.info.Name)
	}

	// If a filter is provided, find the first match.
	if filter != "" {
		for _, c := range candidates {
			if strings.Contains(strings.ToLower(c.info.Name), strings.ToLower(filter)) {
				return c.info, nil
			}
		}
		return deviceInfo{}, fmt.Errorf("no device matching %q found\nAvailable devices: %s", filter, strings.Join(names, ", "))
	}

	// Auto-detect: skip built-in, only return external capture devices.
	for _, c := range candidates {
		lower := strings.ToLower(c.info.Name)
		skip := false
		for _, kw := range skipKeywords {
			if strings.Contains(lower, kw) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		for _, kw := range preferKeywords {
			if strings.Contains(lower, kw) {
				return c.info, nil
			}
		}
	}

	return deviceInfo{}, fmt.Errorf("no external capture device found\nAvailable devices: %s", strings.Join(names, ", "))
}

// ListDevices returns a list of all video device names for diagnostic output.
func ListDevices() []string {
	devices := mediadevices.EnumerateDevices()
	var names []string
	for _, d := range devices {
		if d.Kind == mediadevices.VideoInput {
			// Look up the friendly name from the driver.
			drivers := driver.GetManager().Query(func(drv driver.Driver) bool {
				return drv.Info().Label == d.Label
			})
			if len(drivers) > 0 {
				name := drivers[0].Info().Name
				if name != "" {
					names = append(names, name)
					continue
				}
			}
			names = append(names, d.Label)
		}
	}
	return names
}

// maxConsecutiveErrors is the number of consecutive read errors before
// the capture goroutine treats the device as disconnected.
const maxConsecutiveErrors = 3

// maxConsecutiveBlack is the number of consecutive near-black frames before
// the capture goroutine reports no signal.
const maxConsecutiveBlack = 10

// blackThreshold is the average pixel brightness (0-255) below which a frame
// is considered black/no-signal.
const blackThreshold = 5

// isBlackFrame returns true if the image's average brightness is below blackThreshold.
func isBlackFrame(img image.Image) bool {
	bounds := img.Bounds()
	// Sample a grid of pixels rather than checking every one.
	step := 1
	if w := bounds.Dx(); w > 100 {
		step = w / 50
	}
	var total, count uint64
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			// RGBA returns 16-bit values; scale to 8-bit.
			total += uint64((r>>8 + g>>8 + b>>8) / 3)
			count++
		}
	}
	if count == 0 {
		return true
	}
	return total/count < blackThreshold
}

// cropThreshold is the per-channel brightness (0-255) below which a pixel is
// considered black for crop-border detection. Set above 16 to handle
// limited-range YCbCr (where Y=16 is black) plus scaler transition pixels
// from HDMI capture devices.
const cropThreshold = 30

// cropStability is the minimum pixel change on any edge required to adopt a
// new crop rectangle. This prevents frame-to-frame jitter.
const cropStability = 4

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

// shouldUpdateCrop returns true if newRect differs from oldRect by more than
// stability pixels on any edge.
func shouldUpdateCrop(oldRect, newRect image.Rectangle, stability int) bool {
	if oldRect == (image.Rectangle{}) {
		return true
	}
	return abs(newRect.Min.X-oldRect.Min.X) > stability ||
		abs(newRect.Min.Y-oldRect.Min.Y) > stability ||
		abs(newRect.Max.X-oldRect.Max.X) > stability ||
		abs(newRect.Max.Y-oldRect.Max.Y) > stability
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// InitObserver starts the AVFoundation device observer which automatically
// registers and unregisters camera drivers as hardware is plugged/unplugged.
// Must be called once at startup before any device detection.
func InitObserver() error {
	return camera.StartObserver()
}

// StartCapture opens the device matching dev and runs a goroutine that
// reads frames, JPEG-encodes them, and stores them in buf.
// The returned channel closes when the goroutine exits (device disconnected).
func StartCapture(dev deviceInfo, width, height, fps, quality int, buf *FrameBuffer) (<-chan struct{}, error) {
	// Find the driver matching our detected device label (UID).
	drivers := driver.GetManager().Query(func(d driver.Driver) bool {
		return d.Info().DeviceType == driver.Camera && d.Info().Label == dev.Label
	})
	if len(drivers) == 0 {
		return nil, fmt.Errorf("device %q not found", dev.Name)
	}

	d := drivers[0]
	if err := d.Open(); err != nil {
		return nil, fmt.Errorf("failed to open device %q: %w", dev.Name, err)
	}

	// Query device capabilities and pick the best matching format.
	props := d.Properties()
	if len(props) == 0 {
		return nil, fmt.Errorf("device %q reported no supported formats", dev.Name)
	}

	best := selectBestProp(props, width, height, fps)

	recorder, ok := d.(driver.VideoRecorder)
	if !ok {
		return nil, fmt.Errorf("device %q does not support video recording", dev.Name)
	}

	reader, err := recorder.VideoRecord(best)
	if err != nil {
		return nil, fmt.Errorf("failed to start recording on %q: %w", dev.Name, err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer d.Close()

		var consecutiveErrors int
		var consecutiveBlack int
		var activeCrop image.Rectangle
		jpegOpts := &jpeg.Options{Quality: quality}
		for {
			img, release, err := reader.Read()
			if err != nil {
				consecutiveErrors++
				if consecutiveErrors >= maxConsecutiveErrors {
					log.Printf("device %q: %d consecutive read errors, treating as disconnected", dev.Name, consecutiveErrors)
					buf.Clear()
					return
				}
				continue
			}
			consecutiveErrors = 0

			if isBlackFrame(img) {
				consecutiveBlack++
				if consecutiveBlack == maxConsecutiveBlack {
					log.Printf("device %q: no signal (black frames)", dev.Name)
					buf.SetStatus(StatusNoSignal)
				}
			} else {
				if consecutiveBlack >= maxConsecutiveBlack {
					log.Printf("device %q: signal restored", dev.Name)
					buf.SetStatus(StatusConnected)
				}
				consecutiveBlack = 0
			}

			// Detect content rectangle and apply crop stability.
			rect := detectContentRect(img, cropThreshold)
			if shouldUpdateCrop(activeCrop, rect, cropStability) {
				activeCrop = rect
			}

			fullBounds := img.Bounds()
			if activeCrop == fullBounds {
				// No crop needed — encode once and use for both.
				var imgBuf bytes.Buffer
				if err := jpeg.Encode(&imgBuf, img, jpegOpts); err != nil {
					release()
					continue
				}
				release()
				encoded := imgBuf.Bytes()
				buf.UpdateBoth(encoded, encoded, activeCrop)
			} else {
				// Crop active — encode both cropped and raw.
				type subImager interface {
					SubImage(r image.Rectangle) image.Image
				}
				var cropped image.Image
				if si, ok := img.(subImager); ok {
					cropped = si.SubImage(activeCrop)
				} else {
					// Fallback: draw into new RGBA (shouldn't happen with standard types).
					dst := image.NewRGBA(activeCrop)
					for y := activeCrop.Min.Y; y < activeCrop.Max.Y; y++ {
						for x := activeCrop.Min.X; x < activeCrop.Max.X; x++ {
							dst.Set(x, y, img.At(x, y))
						}
					}
					cropped = dst
				}

				var croppedBuf bytes.Buffer
				if err := jpeg.Encode(&croppedBuf, cropped, jpegOpts); err != nil {
					release()
					continue
				}

				var rawBuf bytes.Buffer
				if err := jpeg.Encode(&rawBuf, img, jpegOpts); err != nil {
					release()
					continue
				}
				release()

				buf.UpdateBoth(croppedBuf.Bytes(), rawBuf.Bytes(), activeCrop)
			}
		}
	}()

	return done, nil
}

// CaptureManager manages the lifecycle of capture devices, handling
// automatic detection, connection, and reconnection.
type CaptureManager struct {
	Filter         string
	Width          int
	Height         int
	FPS            int
	Quality        int
	Buf            *FrameBuffer
	AudioBroadcast *AudioBroadcaster

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.RWMutex
	device deviceInfo
}

// NewCaptureManager creates a new CaptureManager.
func NewCaptureManager(filter string, width, height, fps, quality int, buf *FrameBuffer, ab *AudioBroadcaster) *CaptureManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &CaptureManager{
		Filter:         filter,
		Width:          width,
		Height:         height,
		FPS:            fps,
		Quality:        quality,
		Buf:            buf,
		AudioBroadcast: ab,
		ctx:            ctx,
		cancel:         cancel,
	}
}

// Run loops: detect device → start capture → wait for disconnect → repeat.
// The AVFoundation observer (started via InitObserver) keeps the driver
// manager in sync with hardware — devices are automatically registered on
// plug and unregistered on unplug.
func (cm *CaptureManager) Run() {
	retryDelay := 2 * time.Second
	const maxRetryDelay = 30 * time.Second

	for {
		cm.Buf.SetStatus(StatusConnecting)

		// Try to detect a device from the driver manager (kept current by the observer).
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

		log.Printf("device %q detected, starting capture", dev.Name)

		done, err := StartCapture(dev, cm.Width, cm.Height, cm.FPS, cm.Quality, cm.Buf)
		if err != nil {
			log.Printf("failed to start capture on %q: %v", dev.Name, err)
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

		// Capture is running — reset backoff and mark connected.
		retryDelay = 2 * time.Second
		cm.Buf.SetStatus(StatusConnected)

		// Start audio capture (optional — failure is non-fatal).
		var audioDone <-chan struct{}
		var audioCancel context.CancelFunc
		if cm.AudioBroadcast != nil {
			audioDev, found := DetectAudioDevice(cm.Filter)
			if found {
				audioCtx, ac := context.WithCancel(cm.ctx)
				audioCancel = ac
				ad, err := StartAudioCapture(audioCtx, audioDev, cm.AudioBroadcast)
				if err != nil {
					log.Printf("audio capture failed on %q: %v (continuing without audio)", audioDev.Name, err)
					audioCancel()
				} else {
					audioDone = ad
					log.Printf("audio capture started on %q", audioDev.Name)
				}
			}
		}

		// Wait for video disconnect or shutdown.
		select {
		case <-done:
			log.Printf("device %q disconnected", dev.Name)
		case <-cm.ctx.Done():
			if audioCancel != nil {
				audioCancel()
			}
			return
		}

		// Stop audio when video disconnects.
		if audioCancel != nil {
			audioCancel()
		}
		if audioDone != nil {
			<-audioDone
		}

		select {
		case <-cm.ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
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
