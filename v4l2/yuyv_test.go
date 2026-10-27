package v4l2

import (
	"image"
	"image/color"
	"testing"
)

func TestYUYVImage(t *testing.T) {
	src := yuyvFixture(8, 4)
	p := NewYUYV(src, 16, 8, 4, 0)
	if p.Bounds() != image.Rect(0, 0, 8, 4) {
		t.Errorf("bounds %v", p.Bounds())
	}
	// Pixel (3,2): Y is its index, chroma is the pair's (row 2).
	if c := p.YCbCrAt(3, 2); c != (color.YCbCr{Y: 2*8 + 3, Cb: 102, Cr: 202}) {
		t.Errorf("YCbCrAt(3,2) = %+v", c)
	}
	if p.At(9, 0) != (color.YCbCr{}) {
		t.Error("out of bounds is not empty")
	}
	sub := p.SubImage(image.Rect(2, 1, 6, 3)).(*YUYV)
	if sub.Bounds() != image.Rect(2, 1, 6, 3) || sub.YCbCrAt(2, 1).Y != 1*8+2 {
		t.Errorf("sub image %v, pixel %+v", sub.Bounds(), sub.YCbCrAt(2, 1))
	}
	planar := p.YCbCr(image.Rect(2, 1, 6, 3))
	if planar.Bounds() != image.Rect(2, 1, 6, 3) {
		t.Errorf("planar bounds %v", planar.Bounds())
	}
	for y := 1; y < 3; y++ {
		for x := 2; x < 6; x++ {
			if got, want := planar.YCbCrAt(x, y), p.YCbCrAt(x, y); got != want {
				t.Errorf("planar (%d,%d) = %+v, want %+v", x, y, got, want)
			}
		}
	}
	cp := p.Copy().(*YUYV)
	src[0] = 99
	if cp.Pix[0] == 99 {
		t.Error("Copy shares memory")
	}
}
