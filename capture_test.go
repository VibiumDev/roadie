package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"sync"
	"testing"
)

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

func TestFrameBufferLatestNil(t *testing.T) {
	fb := &FrameBuffer{}
	if got := fb.Latest(); got != nil {
		t.Errorf("expected nil, got %d bytes", len(got))
	}
}

func TestFrameBufferStoreAndRetrieve(t *testing.T) {
	fb := &FrameBuffer{}
	frame := testJPEG(320, 240)
	fb.Update(frame)

	if got := fb.Latest(); !bytes.Equal(got, frame) {
		t.Error("retrieved frame does not match stored frame")
	}
}

func TestFrameBufferOverwrite(t *testing.T) {
	fb := &FrameBuffer{}
	fb.Update(testJPEG(320, 240))

	frame2 := testJPEG(640, 480)
	fb.Update(frame2)

	if got := fb.Latest(); !bytes.Equal(got, frame2) {
		t.Error("expected latest frame to be frame2")
	}
}

func TestFrameBufferConcurrent(t *testing.T) {
	fb := &FrameBuffer{}
	frame := testJPEG(320, 240)

	var wg sync.WaitGroup
	wg.Add(5)
	go func() { defer wg.Done(); for i := 0; i < 1000; i++ { fb.Update(frame) } }()
	for i := 0; i < 4; i++ {
		go func() { defer wg.Done(); for j := 0; j < 1000; j++ { fb.Latest() } }()
	}
	wg.Wait()
}

func TestFrameBufferStatus(t *testing.T) {
	fb := &FrameBuffer{}
	if got := fb.Status(); got != "" {
		t.Errorf("default status: expected empty, got %q", got)
	}
	for _, s := range []CaptureStatus{StatusConnected, StatusDisconnected, StatusConnecting} {
		fb.SetStatus(s)
		if got := fb.Status(); got != s {
			t.Errorf("SetStatus(%q): got %q", s, got)
		}
	}
}

func TestFrameBufferClear(t *testing.T) {
	fb := &FrameBuffer{}
	fb.Update(testJPEG(320, 240))
	fb.SetStatus(StatusConnected)

	fb.Clear()

	if fb.Latest() != nil {
		t.Error("expected nil frame after Clear")
	}
	if fb.Status() != StatusDisconnected {
		t.Errorf("expected %q after Clear, got %q", StatusDisconnected, fb.Status())
	}
}

func TestIsBlackFrame(t *testing.T) {
	black := image.NewRGBA(image.Rect(0, 0, 100, 100))
	// All pixels default to zero (black).
	if !isBlackFrame(black) {
		t.Error("expected all-black image to be detected as black")
	}

	bright := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			bright.Set(x, y, color.RGBA{R: 200, G: 200, B: 200, A: 255})
		}
	}
	if isBlackFrame(bright) {
		t.Error("expected bright image not to be detected as black")
	}
}
