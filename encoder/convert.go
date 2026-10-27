package encoder

import (
	"image"
	"image/color"

	"github.com/VibiumDev/roadie/v4l2"
)

// NV12 writes the rectangle r of img as NV12 into dst: a Y plane of stride
// bytes per row, then interleaved Cb and Cr rows at dst[uvOffset:], also
// stride bytes per row. r has even sides. YCbCr images of any subsampling
// take a direct path; anything else goes pixel by pixel.
func NV12(dst []byte, stride, uvOffset int, img image.Image, r image.Rectangle) {
	w, h := r.Dx(), r.Dy()
	switch m := img.(type) {
	case *v4l2.YUYV:
		// Straight from the dongle's buffer.
		v4l2.YUYVToNV12(dst, stride, uvOffset, m.Pix, m.Stride, r)
	case *image.Gray:
		// A dark frame while there is no signal: grey, no colour.
		for y := 0; y < h; y++ {
			i := m.PixOffset(r.Min.X, r.Min.Y+y)
			copy(dst[y*stride:y*stride+w], m.Pix[i:i+w])
		}
		for cy := 0; cy < h/2; cy++ {
			row := dst[uvOffset+cy*stride : uvOffset+cy*stride+w]
			for i := range row {
				row[i] = 128
			}
		}
	case *image.YCbCr:
		for y := 0; y < h; y++ {
			yi := m.YOffset(r.Min.X, r.Min.Y+y)
			copy(dst[y*stride:y*stride+w], m.Y[yi:yi+w])
		}
		switch m.SubsampleRatio {
		case image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420:
			// Chroma is already halved across; take every other row down.
			for cy := 0; cy < h/2; cy++ {
				ci := m.COffset(r.Min.X, r.Min.Y+2*cy)
				cb, cr := m.Cb[ci:ci+w/2], m.Cr[ci:ci+w/2]
				row := dst[uvOffset+cy*stride : uvOffset+cy*stride+w]
				for x := 0; x < w/2; x++ {
					row[2*x] = cb[x]
					row[2*x+1] = cr[x]
				}
			}
		default:
			for cy := 0; cy < h/2; cy++ {
				row := dst[uvOffset+cy*stride : uvOffset+cy*stride+w]
				for x := 0; x < w; x += 2 {
					ci := m.COffset(r.Min.X+x, r.Min.Y+2*cy)
					row[x] = m.Cb[ci]
					row[x+1] = m.Cr[ci]
				}
			}
		}
	default:
		for y := 0; y < h; y++ {
			row := dst[y*stride : y*stride+w]
			var uv []byte
			if y&1 == 0 {
				uv = dst[uvOffset+(y/2)*stride : uvOffset+(y/2)*stride+w]
			}
			for x := 0; x < w; x++ {
				cr, cg, cbl, _ := img.At(r.Min.X+x, r.Min.Y+y).RGBA()
				yy, cb, crr := color.RGBToYCbCr(uint8(cr>>8), uint8(cg>>8), uint8(cbl>>8))
				row[x] = yy
				if uv != nil && x&1 == 0 {
					uv[x] = cb
					uv[x+1] = crr
				}
			}
		}
	}
}

// NV12Size is the buffer size NV12 needs for a stride and a padded height.
func NV12Size(stride, height int) int { return stride * height * 3 / 2 }
