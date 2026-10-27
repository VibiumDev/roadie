package encoder

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"github.com/VibiumDev/roadie/v4l2"
)

// ycbcr builds a w by h image of the given subsampling whose Y is the pixel
// index and whose Cb and Cr encode the chroma sample's row and column.
func ycbcr(w, h int, ratio image.YCbCrSubsampleRatio) *image.YCbCr {
	m := image.NewYCbCr(image.Rect(0, 0, w, h), ratio)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Y[m.YOffset(x, y)] = byte(y*w + x)
		}
	}
	cw, ch := w, h
	switch ratio {
	case image.YCbCrSubsampleRatio422:
		cw = w / 2
	case image.YCbCrSubsampleRatio420:
		cw, ch = w/2, h/2
	}
	for cy := 0; cy < ch; cy++ {
		for cx := 0; cx < cw; cx++ {
			m.Cb[cy*m.CStride+cx] = byte(100 + cy)
			m.Cr[cy*m.CStride+cx] = byte(200 + cx)
		}
	}
	return m
}

func TestNV12FromYCbCr(t *testing.T) {
	for _, ratio := range []image.YCbCrSubsampleRatio{image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420, image.YCbCrSubsampleRatio444} {
		m := ycbcr(16, 8, ratio)
		r := image.Rect(4, 2, 12, 6) // 8x4
		stride, uv := 8, 8*4
		dst := make([]byte, NV12Size(stride, 4))
		NV12(dst, stride, uv, m, r)
		for y := 0; y < 4; y++ {
			for x := 0; x < 8; x++ {
				if got, want := dst[y*stride+x], byte((y+2)*16+x+4); got != want {
					t.Errorf("%v Y(%d,%d) = %d, want %d", ratio, x, y, got, want)
				}
			}
		}
		for cy := 0; cy < 2; cy++ {
			for x := 0; x < 8; x += 2 {
				ci := m.COffset(4+x, 2+2*cy)
				if got := dst[uv+cy*stride+x]; got != m.Cb[ci] {
					t.Errorf("%v Cb row %d col %d = %d, want %d", ratio, cy, x, got, m.Cb[ci])
				}
				if got := dst[uv+cy*stride+x+1]; got != m.Cr[ci] {
					t.Errorf("%v Cr row %d col %d = %d, want %d", ratio, cy, x, got, m.Cr[ci])
				}
			}
		}
	}
}

func TestNV12FromYUYVAndGray(t *testing.T) {
	// The dongle's packed form and the planar form of the same picture
	// convert alike.
	w, h := 16, 8
	pix := make([]byte, w*h*2)
	for i := range pix {
		pix[i] = byte(i * 7)
	}
	packed := v4l2.NewYUYV(pix, w*2, w, h, 0)
	r := image.Rect(4, 2, 12, 6)
	a := make([]byte, NV12Size(8, 4))
	b := make([]byte, NV12Size(8, 4))
	NV12(a, 8, 32, packed, r)
	NV12(b, 8, 32, packed.YCbCr(packed.Bounds()), r)
	if !bytes.Equal(a, b) {
		t.Errorf("packed and planar differ:\n% x\n% x", a, b)
	}
	g := image.NewGray(image.Rect(0, 0, 4, 2))
	g.Pix[5] = 200
	NV12(a[:NV12Size(4, 2)], 4, 8, g, g.Bounds())
	if a[5] != 200 || a[8] != 128 || a[11] != 128 {
		t.Errorf("gray: % x", a[:12])
	}
}

func TestNV12FromRGBA(t *testing.T) {
	m := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			m.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	dst := make([]byte, NV12Size(4, 2))
	NV12(dst, 4, 8, m, m.Bounds())
	yy, cb, cr := color.RGBToYCbCr(200, 30, 30)
	if dst[0] != yy || dst[8] != cb || dst[9] != cr {
		t.Errorf("got Y %d Cb %d Cr %d, want %d %d %d", dst[0], dst[8], dst[9], yy, cb, cr)
	}
}

func TestEvenRect(t *testing.T) {
	got := EvenRect(image.Rect(737, 54, 737+446, 54+972))
	if got != image.Rect(738, 54, 738+444, 54+972) {
		t.Errorf("EvenRect = %v", got)
	}
	if EvenRect(image.Rect(0, 0, 1, 1)) != (image.Rectangle{}) {
		t.Error("a 1x1 rect should vanish")
	}
}

func BenchmarkNV12Crop422(b *testing.B) {
	m := image.NewYCbCr(image.Rect(0, 0, 1920, 1080), image.YCbCrSubsampleRatio422)
	r := image.Rect(738, 54, 738+444, 54+972)
	dst := make([]byte, NV12Size(448, 976))
	for i := 0; i < b.N; i++ {
		NV12(dst, 448, 448*976, m, r)
	}
}
