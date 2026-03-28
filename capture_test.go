package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"sync"
	"testing"
)

// makeImage creates a w×h RGBA image filled with fillColor, then draws a
// rectangle of contentColor within the given content bounds.
func makeImage(w, h int, fillColor, contentColor color.RGBA, content image.Rectangle) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, fillColor)
		}
	}
	for y := content.Min.Y; y < content.Max.Y; y++ {
		for x := content.Min.X; x < content.Max.X; x++ {
			img.Set(x, y, contentColor)
		}
	}
	return img
}

func TestDetectContentRectFullImage(t *testing.T) {
	// All bright pixels — no black bars at all.
	img := makeImage(1920, 1080, color.RGBA{200, 200, 200, 255}, color.RGBA{200, 200, 200, 255}, image.Rect(0, 0, 1920, 1080))
	got := detectContentRect(img, cropThreshold)
	want := image.Rect(0, 0, 1920, 1080)
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDetectContentRectPillarbox(t *testing.T) {
	// Black bars on left (0-520) and right (1400-1920), content in middle.
	black := color.RGBA{0, 0, 0, 255}
	bright := color.RGBA{200, 200, 200, 255}
	img := makeImage(1920, 1080, black, bright, image.Rect(520, 0, 1400, 1080))
	got := detectContentRect(img, cropThreshold)
	if got.Min.X != 520 || got.Max.X != 1400 || got.Min.Y != 0 || got.Max.Y != 1080 {
		t.Errorf("got %v, want (520,0)-(1400,1080)", got)
	}
}

func TestDetectContentRectLetterbox(t *testing.T) {
	// Black bars on top (0-140) and bottom (940-1080), content in middle.
	black := color.RGBA{0, 0, 0, 255}
	bright := color.RGBA{200, 200, 200, 255}
	img := makeImage(1920, 1080, black, bright, image.Rect(0, 140, 1920, 940))
	got := detectContentRect(img, cropThreshold)
	if got.Min.Y != 140 || got.Max.Y != 940 || got.Min.X != 0 || got.Max.X != 1920 {
		t.Errorf("got %v, want (0,140)-(1920,940)", got)
	}
}

func TestDetectContentRectAllBlack(t *testing.T) {
	// Entirely black image — should return full bounds (no crop).
	img := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	got := detectContentRect(img, cropThreshold)
	want := image.Rect(0, 0, 1920, 1080)
	if got != want {
		t.Errorf("got %v, want %v (full bounds for all-black)", got, want)
	}
}

func TestFrameBufferDualStorage(t *testing.T) {
	fb := &FrameBuffer{}
	cropped := testJPEG(880, 1080)
	raw := testJPEG(1920, 1080)
	rect := image.Rect(520, 0, 1400, 1080)

	fb.UpdateBoth(cropped, raw, rect)

	if got := fb.Latest(); !bytes.Equal(got, cropped) {
		t.Error("Latest() should return cropped frame")
	}
	if got := fb.LatestRaw(); !bytes.Equal(got, raw) {
		t.Error("LatestRaw() should return raw frame")
	}
	if got := fb.CropRect(); got != rect {
		t.Errorf("CropRect() = %v, want %v", got, rect)
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

func TestFrameBufferUpdateCropped(t *testing.T) {
	fb := &FrameBuffer{}
	raw := testJPEG(1920, 1080)
	cropped1 := testJPEG(880, 1080)
	rect := image.Rect(520, 0, 1400, 1080)

	// Set initial state with both.
	fb.UpdateBoth(cropped1, raw, rect)

	// Update only cropped.
	cropped2 := testJPEG(600, 1080)
	rect2 := image.Rect(660, 0, 1260, 1080)
	fb.UpdateCropped(cropped2, rect2)

	if got := fb.Latest(); !bytes.Equal(got, cropped2) {
		t.Error("Latest() should return updated cropped frame")
	}
	if got := fb.LatestRaw(); !bytes.Equal(got, raw) {
		t.Error("LatestRaw() should still return original raw frame")
	}
	if got := fb.CropRect(); got != rect2 {
		t.Errorf("CropRect() = %v, want %v", got, rect2)
	}
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

func TestIsMajorCropChangeFirstDetection(t *testing.T) {
	full := image.Rect(0, 0, 1920, 1080)
	newRect := image.Rect(520, 0, 1400, 1080)
	if !isMajorCropChange(image.Rectangle{}, newRect, full) {
		t.Error("expected true for first detection (zero activeCrop)")
	}
}

func TestIsMajorCropChangeSmallShift(t *testing.T) {
	full := image.Rect(0, 0, 1920, 1080)
	active := image.Rect(520, 0, 1400, 1080)
	// Shift by a few pixels — area stays the same.
	shifted := image.Rect(522, 0, 1402, 1080)
	if isMajorCropChange(active, shifted, full) {
		t.Error("expected false for small shift with same area")
	}
}

func TestIsMajorCropChangeLargeArea(t *testing.T) {
	full := image.Rect(0, 0, 1920, 1080)
	active := image.Rect(520, 0, 1400, 1080)
	// Jump to full frame — large area change.
	if !isMajorCropChange(active, full, full) {
		t.Error("expected true for large area change")
	}
}

func TestIsMajorCropChangeAspectFlip(t *testing.T) {
	full := image.Rect(0, 0, 1920, 1080)
	landscape := image.Rect(0, 140, 1920, 940) // 1920x800 landscape
	portrait := image.Rect(660, 0, 1260, 1080)  // 600x1080 portrait
	if !isMajorCropChange(landscape, portrait, full) {
		t.Error("expected true for landscape→portrait flip")
	}
	if !isMajorCropChange(portrait, landscape, full) {
		t.Error("expected true for portrait→landscape flip")
	}
}

func TestFrameBufferQuality(t *testing.T) {
	fb := &FrameBuffer{}
	// Default (never set) should return 80.
	if got := fb.Quality(); got != 80 {
		t.Errorf("default quality: got %d, want 80", got)
	}
	fb.SetQuality(50)
	if got := fb.Quality(); got != 50 {
		t.Errorf("after SetQuality(50): got %d, want 50", got)
	}
}

func TestFrameBufferQualityClamping(t *testing.T) {
	fb := &FrameBuffer{}
	fb.SetQuality(10)
	if got := fb.Quality(); got != 30 {
		t.Errorf("below min: got %d, want 30", got)
	}
	fb.SetQuality(100)
	if got := fb.Quality(); got != 95 {
		t.Errorf("above max: got %d, want 95", got)
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
