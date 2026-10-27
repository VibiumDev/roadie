package v4l2

import "image"

// YUYVToNV12 converts the rectangle r of a packed YUYV 4:2:2 image into
// NV12 4:2:0: a Y plane of dstStride bytes per row, then interleaved U and V
// at dst[uvOffset:], also dstStride bytes per row. Chroma rows are taken
// from the even lines and the odd lines' chroma dropped, which is the cheap
// conversion the spec allows. r.Min.X and r.Dx() must be even.
func YUYVToNV12(dst []byte, dstStride, uvOffset int, src []byte, srcStride int, r image.Rectangle) {
	w, h := r.Dx(), r.Dy()
	for y := 0; y < h; y++ {
		srow := src[(r.Min.Y+y)*srcStride+r.Min.X*2:]
		srow = srow[:w*2]
		yrow := dst[y*dstStride:]
		yrow = yrow[:w]
		if y&1 == 0 {
			uvrow := dst[uvOffset+(y>>1)*dstStride:]
			uvrow = uvrow[:w]
			for x := 0; x < w; x += 2 {
				s := srow[x*2 : x*2+4]
				yrow[x] = s[0]
				uvrow[x] = s[1]
				yrow[x+1] = s[2]
				uvrow[x+1] = s[3]
			}
		} else {
			for x := 0; x < w; x += 2 {
				s := srow[x*2 : x*2+4]
				yrow[x] = s[0]
				yrow[x+1] = s[2]
			}
		}
	}
}

// YUYVCrop copies the rectangle r of a packed YUYV image into dst, with
// dstStride bytes per row. r.Min.X must be even.
func YUYVCrop(dst []byte, dstStride int, src []byte, srcStride int, r image.Rectangle) {
	w, h := r.Dx(), r.Dy()
	for y := 0; y < h; y++ {
		srow := src[(r.Min.Y+y)*srcStride+r.Min.X*2:]
		copy(dst[y*dstStride:y*dstStride+w*2], srow[:w*2])
	}
}

// NV12Copy copies an NV12 image of w by h pixels between buffers with
// different strides. The UV plane follows the Y plane at srcUV and dstUV.
func NV12Copy(dst []byte, dstStride, dstUV int, src []byte, srcStride, srcUV int, w, h int) {
	for y := 0; y < h; y++ {
		copy(dst[y*dstStride:y*dstStride+w], src[y*srcStride:y*srcStride+w])
	}
	for y := 0; y < h/2; y++ {
		copy(dst[dstUV+y*dstStride:dstUV+y*dstStride+w], src[srcUV+y*srcStride:srcUV+y*srcStride+w])
	}
}
