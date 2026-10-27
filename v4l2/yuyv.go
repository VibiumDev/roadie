package v4l2

import (
	"image"
	"image/color"
	"time"
)

// YUYV is a packed 4:2:2 picture as a UVC dongle delivers it, usable as an
// image.Image without copying. Pix is the whole buffer; Rect is the part
// shown, so a SubImage shares the memory. The memory may belong to the
// capture driver: Copy takes it onto the heap when it must outlive the
// buffer.
type YUYV struct {
	Pix       []byte
	Stride    int // bytes per row
	Rect      image.Rectangle
	Timestamp time.Duration // the driver's, as Now
}

// NewYUYV wraps a buffer of w by h pixels with stride bytes per row.
func NewYUYV(pix []byte, stride, w, h int, ts time.Duration) *YUYV {
	return &YUYV{Pix: pix, Stride: stride, Rect: image.Rect(0, 0, w, h), Timestamp: ts}
}

func (p *YUYV) ColorModel() color.Model { return color.YCbCrModel }

func (p *YUYV) Bounds() image.Rectangle { return p.Rect }

// At is the pixel as YCbCr; the chroma is the pair's.
func (p *YUYV) At(x, y int) color.Color { return p.YCbCrAt(x, y) }

// YCbCrAt is At without the interface.
func (p *YUYV) YCbCrAt(x, y int) color.YCbCr {
	if !(image.Point{x, y}.In(p.Rect)) {
		return color.YCbCr{}
	}
	i := y*p.Stride + x*2
	pair := y*p.Stride + (x&^1)*2
	return color.YCbCr{Y: p.Pix[i], Cb: p.Pix[pair+1], Cr: p.Pix[pair+3]}
}

// SubImage is the rectangle r of the picture, sharing its memory.
func (p *YUYV) SubImage(r image.Rectangle) image.Image {
	r = r.Intersect(p.Rect)
	return &YUYV{Pix: p.Pix, Stride: p.Stride, Rect: r, Timestamp: p.Timestamp}
}

// YCbCr converts the rectangle r to a planar 4:2:2 image, which image/jpeg
// encodes quickly. Chroma belongs to pairs of columns counted from the
// picture's left edge, so a rectangle starting on an odd column keeps the
// pairing: its first pixel shares chroma with the one before it, as
// image.YCbCr's own layout expects.
func (p *YUYV) YCbCr(r image.Rectangle) *image.YCbCr {
	r = r.Intersect(p.Rect)
	m := image.NewYCbCr(r, image.YCbCrSubsampleRatio422)
	if r.Empty() {
		return m
	}
	firstPair, lastPair := r.Min.X>>1, (r.Max.X-1)>>1
	for y := 0; y < r.Dy(); y++ {
		src := p.Pix[(r.Min.Y+y)*p.Stride:]
		yrow := m.Y[y*m.YStride:]
		cb := m.Cb[y*m.CStride:]
		cr := m.Cr[y*m.CStride:]
		for k := firstPair; k <= lastPair; k++ {
			s := src[k*4 : k*4+4]
			ci := k - firstPair
			cb[ci] = s[1]
			cr[ci] = s[3]
			if x := 2 * k; x >= r.Min.X {
				yrow[x-r.Min.X] = s[0]
			}
			if x := 2*k + 1; x < r.Max.X {
				yrow[x-r.Min.X] = s[2]
			}
		}
	}
	return m
}

// Copy is the picture on the heap, for when the buffer goes back to its
// driver.
func (p *YUYV) Copy() image.Image {
	pix := make([]byte, len(p.Pix))
	copy(pix, p.Pix)
	return &YUYV{Pix: pix, Stride: p.Stride, Rect: p.Rect, Timestamp: p.Timestamp}
}

// Stamp is when the driver captured the picture.
func (p *YUYV) Stamp() time.Duration { return p.Timestamp }
