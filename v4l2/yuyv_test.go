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
	// Planar conversions of even and odd rectangles both match the packed
	// pixels; the odd one is what an odd crop column asks for.
	for _, r := range []image.Rectangle{image.Rect(2, 1, 6, 3), image.Rect(3, 1, 6, 4), image.Rect(1, 0, 8, 2), image.Rect(0, 0, 8, 4)} {
		planar := p.YCbCr(r)
		if planar.Bounds() != r {
			t.Errorf("planar bounds %v, want %v", planar.Bounds(), r)
		}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if got, want := planar.YCbCrAt(x, y), p.YCbCrAt(x, y); got != want {
					t.Errorf("planar %v (%d,%d) = %+v, want %+v", r, x, y, got, want)
				}
			}
		}
	}
	cp := p.Copy().(*YUYV)
	src[0] = 99
	if cp.Pix[0] == 99 {
		t.Error("Copy shares memory")
	}
}
