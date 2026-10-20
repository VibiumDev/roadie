package main

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"time"
)

// The screen takes a moment to show input. On an iPhone through the HDMI
// dongle, a click's first change reached the capture 185-231 ms later, and
// Calculator's button highlight went on changing until about 750 ms. A
// screenshot taken straight after a click shows the screen from before it,
// and leaves a script guessing how long to sleep.
const (
	reactDelay  = 300 * time.Millisecond // input can't show sooner than this
	settleQuiet = 150 * time.Millisecond // no change for this long: settled
	settleLimit = 2 * time.Second        // from the input; a video never settles
	settlePoll  = 10 * time.Millisecond
)

// settleClock is time for settledFrame, swapped in tests.
type settleClock struct {
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

var realSettleClock = settleClock{now: time.Now, sleep: sleepCtx}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// settledFrame returns the latest frame once the screen has caught up with
// input at lastInput: no sooner than reactDelay after it, and once frames
// have stopped changing for settleQuiet, or at settleLimit. With no input
// that recent it returns the latest frame at once.
func settledFrame(ctx context.Context, src FrameSource, lastInput time.Time, clk settleClock) []byte {
	deadline := lastInput.Add(settleLimit)
	if !clk.now().Before(deadline) {
		return src.Latest()
	}
	if wait := lastInput.Add(reactDelay).Sub(clk.now()); wait > 0 {
		if clk.sleep(ctx, wait) != nil {
			return src.Latest()
		}
	}
	// Each frame is compared with the one the quiet stretch started on, not
	// the one before it, so a slow fade adds up to a change.
	latest := src.Latest()
	ref := decodeGray(latest)
	quietSince := clk.now()
	for {
		now := clk.now()
		if !now.Before(deadline) || now.Sub(quietSince) >= settleQuiet {
			return latest
		}
		if clk.sleep(ctx, settlePoll) != nil {
			return latest
		}
		cur := src.Latest()
		if bytes.Equal(cur, latest) {
			continue
		}
		latest = cur
		if img := decodeGray(cur); framesDiffer(ref, img) {
			ref, quietSince = img, clk.now()
		}
	}
}

func decodeGray(frame []byte) *image.Gray {
	img, err := jpeg.Decode(bytes.NewReader(frame))
	if err != nil {
		return nil
	}
	switch m := img.(type) {
	case *image.YCbCr: // a JPEG's luma is already the grayscale picture
		return &image.Gray{Pix: m.Y, Stride: m.YStride, Rect: m.Rect}
	case *image.Gray:
		return m
	}
	return nil
}

// framesDiffer is whether two frames show different screens, not just
// different compression noise: more than a few sampled pixels moved by
// more than 24 levels.
func framesDiffer(a, b *image.Gray) bool {
	if a == nil || b == nil || a.Bounds() != b.Bounds() {
		return true
	}
	const step, level, count = 2, 24, 8
	changed := 0
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y += step {
		for x := r.Min.X; x < r.Max.X; x += step {
			d := int(a.GrayAt(x, y).Y) - int(b.GrayAt(x, y).Y)
			if d > level || d < -level {
				if changed++; changed > count {
					return true
				}
			}
		}
	}
	return false
}
