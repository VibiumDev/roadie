package capture

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"time"
)

// Test doubles for the capture side: a frame source on a fake clock and a
// JPEG of a given size. The server's tests use them too, so they are not
// test files themselves.

type TimedSource struct {
	Clock  *time.Time
	Start  time.Time
	Frames []TimedFrame // in order of at
}

type TimedFrame struct {
	At    time.Duration
	Frame []byte
}

func (s *TimedSource) Latest() []byte {
	var f []byte
	for _, tf := range s.Frames {
		if s.Clock.Sub(s.Start) >= tf.At {
			f = tf.Frame
		}
	}
	return f
}
func (s *TimedSource) LatestRaw() []byte            { return s.Latest() }
func (s *TimedSource) Status() CaptureStatus        { return StatusConnected }
func (s *TimedSource) CropRect() image.Rectangle    { return image.Rectangle{} }
func (s *TimedSource) ContentRect() image.Rectangle { return image.Rectangle{} }

func FakeSettleClock(clock *time.Time) SettleClock {
	return SettleClock{
		Now: func() time.Time { return *clock },
		Sleep: func(_ context.Context, d time.Duration) error {
			*clock = clock.Add(d)
			return nil
		},
	}
}

func TestJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 149, B: 237, A: 255})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return buf.Bytes()
}
