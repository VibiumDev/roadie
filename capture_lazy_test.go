package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
	"time"

	"github.com/VibiumDev/roadie/rtc"
)

func testPicture() *image.YCbCr {
	m := image.NewYCbCr(image.Rect(0, 0, 64, 48), image.YCbCrSubsampleRatio422)
	for i := range m.Y {
		m.Y[i] = byte(i)
	}
	return m
}

func decodeSize(t *testing.T, data []byte) image.Point {
	t.Helper()
	if data == nil {
		t.Fatal("no JPEG")
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	return image.Pt(cfg.Width, cfg.Height)
}

func TestFrameBufferJPEGOnDemand(t *testing.T) {
	fb := &FrameBuffer{}
	fb.SetQuality(80)
	released := 0
	crop := image.Rect(16, 8, 48, 40)
	fb.UpdateRaw(testPicture(), func() { released++ }, crop)

	if got := decodeSize(t, fb.Latest()); got != image.Pt(32, 32) {
		t.Errorf("cropped JPEG is %v", got)
	}
	if got := decodeSize(t, fb.LatestRaw()); got != image.Pt(64, 48) {
		t.Errorf("raw JPEG is %v", got)
	}
	if fb.CropRect() != crop {
		t.Errorf("CropRect = %v", fb.CropRect())
	}
	// Asking again returns the JPEG already made, not another encode.
	a, b := fb.Latest(), fb.Latest()
	if &a[0] != &b[0] {
		t.Error("Latest encoded the same picture twice")
	}
	if released != 0 {
		t.Error("the picture was released while current")
	}

	// The next picture retires the first, and the JPEGs are remade from it.
	fb.UpdateRaw(testPicture(), nil, image.Rectangle{})
	if released != 1 {
		t.Errorf("released %d times, want 1", released)
	}
	c := fb.Latest()
	if &a[0] == &c[0] {
		t.Error("Latest returned the previous picture's JPEG")
	}
	if got := decodeSize(t, c); got != image.Pt(64, 48) {
		t.Errorf("uncropped JPEG is %v", got)
	}
}

func TestFrameBufferLatestAt(t *testing.T) {
	fb := &FrameBuffer{}
	fb.SetQuality(80)
	if fb.LatestAt(90, false) != nil {
		t.Error("a JPEG from nothing")
	}
	fb.UpdateRaw(testPicture(), nil, image.Rect(16, 8, 48, 40))
	low, high := fb.LatestAt(30, false), fb.LatestAt(95, false)
	if got := decodeSize(t, low); got != image.Pt(32, 32) {
		t.Errorf("cropped screenshot is %v", got)
	}
	if len(high) <= len(low) {
		t.Errorf("quality 95 (%d bytes) is not larger than 30 (%d)", len(high), len(low))
	}
	if got := decodeSize(t, fb.LatestAt(80, true)); got != image.Pt(64, 48) {
		t.Errorf("whole screenshot is %v", got)
	}
	if fb.Latest() == nil {
		t.Error("no stream JPEG")
	}
}

func TestFrameBufferLoopJPEGStaysFresh(t *testing.T) {
	fb := &FrameBuffer{}
	fb.SetQuality(80)
	fb.UpdateRaw(testPicture(), nil, image.Rect(0, 0, 64, 48))
	loopMade := []byte("jpeg made by the loop")
	fb.UpdateBoth(loopMade, loopMade, image.Rect(0, 0, 64, 48))
	if got := fb.Latest(); !bytes.Equal(got, loopMade) {
		t.Error("Latest re-encoded a frame the loop already encoded")
	}
	if got := fb.LatestRaw(); !bytes.Equal(got, loopMade) {
		t.Error("LatestRaw re-encoded a frame the loop already encoded")
	}
}

func TestFrameBufferHTTPSource(t *testing.T) {
	fb := &FrameBuffer{}
	frame := []byte("a jpeg from elsewhere")
	fb.Update(frame)
	if !bytes.Equal(fb.Latest(), frame) || !bytes.Equal(fb.LatestRaw(), frame) {
		t.Error("Update did not set both frames")
	}
}

func TestFrameBufferClearReleases(t *testing.T) {
	fb := &FrameBuffer{}
	released := 0
	fb.UpdateRaw(testPicture(), func() { released++ }, image.Rectangle{})
	fb.Clear()
	if released != 1 {
		t.Errorf("Clear released %d times", released)
	}
	if fb.Latest() != nil {
		t.Error("a cleared buffer still has a frame")
	}
}

type countingSink struct{ n int }

func (c *countingSink) Frame(image.Image, image.Rectangle, time.Duration) { c.n++ }

func TestFrameBufferSinks(t *testing.T) {
	fb := &FrameBuffer{}
	sink := &countingSink{}
	var _ rtc.FrameSink = sink
	fb.deliver(testPicture(), image.Rectangle{}, 0)
	fb.AddSink(sink)
	fb.deliver(testPicture(), image.Rectangle{}, 0)
	fb.deliver(testPicture(), image.Rectangle{}, 0)
	fb.RemoveSink(sink)
	fb.deliver(testPicture(), image.Rectangle{}, 0)
	if sink.n != 2 {
		t.Errorf("sink saw %d frames, want 2", sink.n)
	}
}

// A picture that lives in a driver's buffer: Detach must copy it before
// the buffer goes back.
type driverPicture struct {
	*image.YCbCr
	copied bool
}

func (d *driverPicture) Copy() image.Image {
	d.copied = true
	cp := *d.YCbCr
	return &cp
}

func TestFrameBufferDetach(t *testing.T) {
	fb := &FrameBuffer{}
	fb.SetQuality(80)
	released := 0
	pic := &driverPicture{YCbCr: testPicture()}
	fb.UpdateRaw(pic, func() { released++ }, image.Rect(0, 0, 64, 48))
	fb.Detach()
	if !pic.copied || released != 1 {
		t.Errorf("copied %v, released %d", pic.copied, released)
	}
	// The copy serves snapshots after capture has stopped.
	if got := decodeSize(t, fb.Latest()); got != image.Pt(64, 48) {
		t.Errorf("JPEG after Detach is %v", got)
	}
	// A heap picture needs nothing.
	fb.UpdateRaw(testPicture(), func() { released++ }, image.Rectangle{})
	fb.Detach()
	if released != 1 {
		t.Errorf("a heap picture was released by Detach")
	}
}
