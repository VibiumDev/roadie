package capture

import (
	"context"
	"image"
	"image/color"
	"sync"
	"testing"
	"time"

	"github.com/VibiumDev/roadie/internal/devices"
	"github.com/VibiumDev/roadie/v4l2"
)

// yuyv makes the picture a capture card would send, stamped at.
func yuyv(img image.Image, at time.Duration) *v4l2.YUYV {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := make([]byte, w*h*2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x += 2 {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			y0, cb, cr := color.RGBToYCbCr(uint8(r>>8), uint8(g>>8), uint8(bl>>8))
			r, g, bl, _ = img.At(b.Min.X+x+1, b.Min.Y+y).RGBA()
			y1, _, _ := color.RGBToYCbCr(uint8(r>>8), uint8(g>>8), uint8(bl>>8))
			i := (y*w + x) * 2
			pix[i], pix[i+1], pix[i+2], pix[i+3] = y0, cb, y1, cr
		}
	}
	return v4l2.NewYUYV(pix, w*2, w, h, at)
}

// scriptedReader plays its frames once and then waits to be closed.
type scriptedReader struct {
	frames []image.Image
	next   int
	closed chan struct{}
	once   sync.Once
}

func (r *scriptedReader) Read() (image.Image, func(), error) {
	if r.next < len(r.frames) {
		r.next++
		return r.frames[r.next-1], func() {}, nil
	}
	<-r.closed
	return nil, nil, context.Canceled
}

func (r *scriptedReader) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

type stampSink struct {
	mu     sync.Mutex
	stamps []time.Duration
	blanks int
}

func (s *stampSink) Frame(img image.Image, _ image.Rectangle, ts time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stamps = append(s.stamps, ts)
	if _, ok := img.(*image.Gray); ok {
		s.blanks++
	}
}

// The dark frame that stands in for the dongle's No Signal screen keeps the
// time of the frame it replaces. Stamped by another clock, it sat days from
// its neighbours, and a recording gave the frame after it an hour.
func TestBlankFrameKeepsTheCaptureClock(t *testing.T) {
	phone := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for y := 0; y < 1080; y++ {
		for x := 0; x < 1920; x++ {
			c := color.RGBA{16, 16, 16, 255}
			if x >= 734 && x < 1186 && y >= 54 && y < 1026 {
				c = color.RGBA{200, 60, 120, 255}
			}
			phone.Set(x, y, c)
		}
	}
	noSignal := loadJPEG(t, "../../testdata/dongle-no-signal.jpg")

	// The driver's clock counts from boot: weeks, on a host that stays up.
	const boot = 500 * time.Hour
	const frame = time.Second / 30
	reader := &scriptedReader{closed: make(chan struct{})}
	for i := 0; i < 20; i++ {
		img := phone
		if i >= 8 && i < 13 {
			img = nil
		}
		at := boot + time.Duration(i)*frame
		if img == nil {
			reader.frames = append(reader.frames, yuyv(noSignal, at))
		} else {
			reader.frames = append(reader.frames, yuyv(img, at))
		}
	}

	buf := &FrameBuffer{}
	buf.SetQuality(80)
	buf.SetFPS(30)
	buf.SetWidth(1920)
	buf.SetHeight(1080)
	sink := &stampSink{}
	buf.AddSink(sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := runCapture(ctx, devices.DeviceInfo{Name: "test"}, reader, 30, buf)
	deadline := time.Now().Add(20 * time.Second)
	for {
		sink.mu.Lock()
		n := len(sink.stamps)
		sink.mu.Unlock()
		if n >= len(reader.frames) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	reader.Close()
	<-done

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.stamps) != len(reader.frames) {
		t.Fatalf("%d of %d frames were delivered", len(sink.stamps), len(reader.frames))
	}
	if sink.blanks == 0 {
		t.Fatal("no dark frame stood in for the No Signal screen: the test did not reach the swap")
	}
	for i := 1; i < len(sink.stamps); i++ {
		if step := sink.stamps[i] - sink.stamps[i-1]; step != frame {
			t.Errorf("frame %d is %v after frame %d, want %v", i, step, i-1, frame)
		}
	}
}
