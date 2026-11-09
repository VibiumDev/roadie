package main

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

type timedSource struct {
	clock  *time.Time
	start  time.Time
	frames []timedFrame // in order of at
}

type timedFrame struct {
	at    time.Duration
	frame []byte
}

func (s *timedSource) Latest() []byte {
	var f []byte
	for _, tf := range s.frames {
		if s.clock.Sub(s.start) >= tf.at {
			f = tf.frame
		}
	}
	return f
}
func (s *timedSource) LatestRaw() []byte            { return s.Latest() }
func (s *timedSource) Status() CaptureStatus        { return StatusConnected }
func (s *timedSource) CropRect() image.Rectangle    { return image.Rectangle{} }
func (s *timedSource) ContentRect() image.Rectangle { return image.Rectangle{} }

func fakeSettleClock(clock *time.Time) settleClock {
	return settleClock{
		now: func() time.Time { return *clock },
		sleep: func(_ context.Context, d time.Duration) error {
			*clock = clock.Add(d)
			return nil
		},
	}
}

func testJPEG(w, h int) []byte {
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
