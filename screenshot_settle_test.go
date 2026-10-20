package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"time"
)

func grayJPEG(t *testing.T, level uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 40, 80))
	for i := range img.Pix {
		img.Pix[i] = level
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// timedSource serves whichever frame its timeline says is on screen at the
// fake clock's time.
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

func TestSettledFrame(t *testing.T) {
	before, during, after := grayJPEG(t, 20), grayJPEG(t, 120), grayJPEG(t, 220)
	noisy := func(level uint8) []byte { return grayJPEG(t, level) }
	start := time.Unix(1000, 0)

	for _, tc := range []struct {
		name     string
		input    time.Duration // when the last input was, relative to start
		frames   []timedFrame
		want     []byte
		returnAt time.Duration // when settledFrame should return
	}{{
		name:     "no recent input: the latest frame at once",
		input:    -5 * time.Second,
		frames:   []timedFrame{{0, before}},
		want:     before,
		returnAt: 0,
	}, {
		// A click that shows 200 ms later and changes again at 400 ms.
		name:  "waits for the screen to show the input and stop changing",
		input: 0,
		frames: []timedFrame{{0, before}, {200 * time.Millisecond, during},
			{400 * time.Millisecond, after}},
		want:     after,
		returnAt: 550 * time.Millisecond,
	}, {
		// A highlight fading 8 levels a frame from 200 to 700 ms, about the
		// rate measured on an iPhone: no two frames in a row differ enough
		// to count, but it hasn't settled until 700 ms.
		name:  "a slow fade isn't settled until it ends",
		input: 0,
		frames: func() []timedFrame {
			f := []timedFrame{{0, before}}
			for i := 0; i <= 15; i++ {
				f = append(f, timedFrame{200*time.Millisecond + time.Duration(i)*33*time.Millisecond, grayJPEG(t, uint8(60+8*i))})
			}
			return f
		}(),
		want:     grayJPEG(t, 180),
		returnAt: 695*time.Millisecond + settleQuiet,
	}, {
		name:     "input that changes nothing costs only the reaction time",
		input:    0,
		frames:   []timedFrame{{0, before}},
		want:     before,
		returnAt: reactDelay + settleQuiet,
	}, {
		name:  "compression noise isn't a change",
		input: 0,
		frames: []timedFrame{{0, before}, {320 * time.Millisecond, noisy(22)},
			{360 * time.Millisecond, noisy(19)}},
		want:     noisy(19),
		returnAt: reactDelay + settleQuiet,
	}, {
		name:  "a screen that never settles returns at the limit",
		input: 0,
		frames: func() []timedFrame {
			var f []timedFrame
			for i := 0; i < 100; i++ {
				f = append(f, timedFrame{time.Duration(i) * 33 * time.Millisecond, grayJPEG(t, uint8(i%2*200))})
			}
			return f
		}(),
		returnAt: settleLimit,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			clock := start
			src := &timedSource{clock: &clock, start: start, frames: tc.frames}
			got := settledFrame(context.Background(), src, start.Add(tc.input), fakeSettleClock(&clock))
			if tc.want != nil && !bytes.Equal(got, tc.want) {
				t.Errorf("returned the wrong frame")
			}
			if took := clock.Sub(start); took < tc.returnAt || took > tc.returnAt+settlePoll {
				t.Errorf("returned at %v, want %v", took, tc.returnAt)
			}
		})
	}
}

func TestFramesDiffer(t *testing.T) {
	a := decodeGray(grayJPEG(t, 100))
	if framesDiffer(a, decodeGray(grayJPEG(t, 104))) {
		t.Error("a small level change counted as a different screen")
	}
	if !framesDiffer(a, decodeGray(grayJPEG(t, 200))) {
		t.Error("a different screen didn't count")
	}
	small := image.NewGray(image.Rect(0, 0, 10, 10))
	small.Set(0, 0, color.Gray{100})
	if !framesDiffer(a, small) {
		t.Error("a different size didn't count")
	}
}
