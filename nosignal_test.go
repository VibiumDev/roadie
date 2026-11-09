package main

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"testing"
)

func loadJPEG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// Frames the UGREEN 15389 sent as the iPhone locked: its own No Signal
// screen, and the all-dark frame before it. A phone's picture with a
// yellow middle (an iPhone's wallpaper) is neither.
func TestDongleNoSignalScreen(t *testing.T) {
	phone := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for y := 0; y < 1080; y++ {
		for x := 0; x < 1920; x++ {
			c := color.RGBA{16, 16, 16, 255}
			if x >= 734 && x < 1186 && y >= 54 && y < 1026 {
				c = color.RGBA{200, 60, 120, 255}
				if y > 400 && y < 680 {
					c = color.RGBA{240, 200, 40, 255} // yellow, in the middle
				}
			}
			phone.Set(x, y, c)
		}
	}
	white := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for y := 0; y < 1080; y++ {
		for x := 0; x < 1920; x++ {
			c := color.RGBA{16, 16, 16, 255}
			if x >= 860 && x < 1060 && y >= 500 && y < 600 {
				c = color.RGBA{230, 230, 230, 255} // a white message, not the dongle's yellow
			}
			white.Set(x, y, c)
		}
	}
	for _, tc := range []struct {
		name           string
		img            image.Image
		noSignal, dark bool
	}{
		{"the dongle's No Signal screen", loadJPEG(t, "testdata/dongle-no-signal.jpg"), true, false},
		{"the dark frame before it", loadJPEG(t, "testdata/dongle-dark.jpg"), false, true},
		{"a phone with a yellow middle", phone, false, false},
		{"a white message at the centre", white, false, false},
	} {
		if got := dongleNoSignal(tc.img); got != tc.noSignal {
			t.Errorf("%s: dongleNoSignal = %v", tc.name, got)
		}
		if got := brightBox(tc.img).Empty(); got != tc.dark {
			t.Errorf("%s: dark = %v", tc.name, got)
		}
	}
}

// A small, wide detection (the dongle's message, a dark app's one bright
// corner) doesn't turn an upright phone on its side; a wide one spanning
// the frame does.
func TestSmallDetectionKeepsOrientation(t *testing.T) {
	fb := &FrameBuffer{}
	fb.setFrameSize(hd)
	fb.SetContentRect(tall)
	fb.SetContentRect(tall)
	message := image.Rect(856, 496, 1058, 612)
	fb.SetContentRect(message)
	fb.SetContentRect(message)
	fb.SetContentRect(message)
	if fb.Orientation() != OrientationPortrait {
		t.Errorf("after a small wide box: %s, want portrait", fb.Orientation())
	}
	fb.SetContentRect(wide)
	fb.SetContentRect(wide)
	if fb.Orientation() != OrientationLandscape {
		t.Errorf("after the phone turned: %s, want landscape", fb.Orientation())
	}
}

// Frame sizes shared with the crop tests.
var (
	hd   = image.Pt(1920, 1080)
	tall = image.Rect(734, 54, 1186, 1026) // an iPhone held upright
	wide = image.Rect(0, 97, 1920, 983)    // the iPhone turned sideways
)
