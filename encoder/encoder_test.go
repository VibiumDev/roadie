package encoder

import (
	"errors"
	"image"
	"os"
	"testing"
	"time"
)

// grey makes a 4:2:2 frame with a moving bar, so an encoder has something
// to do.
func grey(w, h, i int) *image.YCbCr {
	m := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio422)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := byte(64)
			if x/16 == i%(w/16) {
				v = 200
			}
			m.Y[m.YOffset(x, y)] = v
		}
	}
	for i := range m.Cb {
		m.Cb[i], m.Cr[i] = 128, 128
	}
	return m
}

// runEncoder feeds n frames and collects the samples that come out within a
// deadline.
func runEncoder(t *testing.T, e Encoder, n int) []Sample {
	t.Helper()
	m := grey(320, 240, 0)
	for i := 0; i < n; i++ {
		m = grey(320, 240, i)
		if err := e.Encode(Frame{Img: m, Rect: m.Bounds(), Timestamp: time.Duration(i) * 33 * time.Millisecond}); err != nil {
			t.Fatalf("Encode: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var out []Sample
	deadline := time.After(5 * time.Second)
	for len(out) < n-2 {
		select {
		case s, ok := <-e.Samples():
			if !ok {
				return out
			}
			out = append(out, s)
		case <-deadline:
			return out
		}
	}
	return out
}

func checkSamples(t *testing.T, out []Sample, name string) {
	t.Helper()
	if len(out) < 5 {
		t.Fatalf("%s: only %d samples", name, len(out))
	}
	if !out[0].Keyframe {
		t.Errorf("%s: first sample is not a keyframe", name)
	}
	sps, pps, idr := NALTypes(out[0].Data)
	if !sps || !pps || !idr {
		t.Errorf("%s: first sample has sps %v pps %v idr %v", name, sps, pps, idr)
	}
	for i := 1; i < len(out); i++ {
		if out[i].Timestamp < out[i-1].Timestamp {
			t.Errorf("%s: timestamps out of order at %d", name, i)
		}
	}
}

func TestFFmpegLibx264(t *testing.T) {
	if !ffmpegHas("libx264") {
		t.Skip("no ffmpeg with libx264")
	}
	e, err := NewFFmpeg(Config{Width: 320, Height: 240, FPS: 30, Bitrate: 500_000}, "libx264")
	if err != nil {
		t.Fatal(err)
	}
	out := runEncoder(t, e, 20)
	if err := e.Close(); err != nil {
		t.Error(err)
	}
	checkSamples(t, out, "libx264")
}

func TestV4L2M2M(t *testing.T) {
	if _, err := os.Stat(defaultEncoderDevice); err != nil {
		t.Skip("no hardware encoder")
	}
	e, name, err := Open("v4l2m2m", Config{Width: 320, Height: 240, FPS: 30, Bitrate: 500_000})
	if errors.Is(err, ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if name != "v4l2m2m" {
		t.Errorf("opened %q", name)
	}
	if err := e.SetBitrate(800_000); err != nil {
		t.Error(err)
	}
	if err := e.ForceKeyframe(); err != nil {
		t.Error(err)
	}
	out := runEncoder(t, e, 20)
	if err := e.Close(); err != nil {
		t.Error(err)
	}
	checkSamples(t, out, "v4l2m2m")
}

func TestOpenUnknown(t *testing.T) {
	if _, _, err := Open("gstreamer", Config{Width: 2, Height: 2}); err == nil {
		t.Error("unknown encoder opened")
	}
	if _, _, err := Open("none", Config{Width: 2, Height: 2}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("none: %v", err)
	}
}
