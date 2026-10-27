package v4l2

import (
	"image"
	"testing"
)

// A 4x4 YUYV image whose Y is the pixel's index and whose chroma is
// 100+row for U and 200+row for V, so a converted sample says where it
// came from.
func yuyvFixture(w, h int) []byte {
	b := make([]byte, w*h*2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x += 2 {
			i := (y*w + x) * 2
			b[i] = byte(y*w + x)
			b[i+1] = byte(100 + y)
			b[i+2] = byte(y*w + x + 1)
			b[i+3] = byte(200 + y)
		}
	}
	return b
}

func TestYUYVToNV12(t *testing.T) {
	src := yuyvFixture(8, 4)
	r := image.Rect(2, 1, 6, 4) // 4 wide, 3 high, odd height on purpose
	stride := 16
	uv := stride * 3
	dst := make([]byte, uv+stride*2)
	YUYVToNV12(dst, stride, uv, src, 8*2, r)
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			want := byte((y+1)*8 + x + 2)
			if got := dst[y*stride+x]; got != want {
				t.Errorf("Y(%d,%d) = %d, want %d", x, y, got, want)
			}
		}
	}
	// Chroma rows come from source rows 1 and 3 (the even rows of the crop).
	for cy, srcRow := range []int{1, 3} {
		for x := 0; x < 4; x += 2 {
			if got := dst[uv+cy*stride+x]; got != byte(100+srcRow) {
				t.Errorf("U row %d x %d = %d, want %d", cy, x, got, 100+srcRow)
			}
			if got := dst[uv+cy*stride+x+1]; got != byte(200+srcRow) {
				t.Errorf("V row %d x %d = %d, want %d", cy, x, got, 200+srcRow)
			}
		}
	}
}

func TestYUYVCrop(t *testing.T) {
	src := yuyvFixture(8, 4)
	r := image.Rect(4, 2, 8, 4)
	dst := make([]byte, 32*2)
	YUYVCrop(dst, 32, src, 16, r)
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			if got := dst[y*32+x*2]; got != byte((y+2)*8+x+4) {
				t.Errorf("Y(%d,%d) = %d", x, y, got)
			}
		}
	}
}

func BenchmarkYUYVToNV12Crop(b *testing.B) {
	src := make([]byte, 1920*1080*2)
	r := image.Rect(736, 54, 736+448, 54+972)
	stride := 448
	dst := make([]byte, stride*972*3/2)
	b.SetBytes(int64(r.Dx() * r.Dy() * 2))
	for i := 0; i < b.N; i++ {
		YUYVToNV12(dst, stride, stride*972, src, 1920*2, r)
	}
}

func BenchmarkYUYVToNV12Full(b *testing.B) {
	src := make([]byte, 1920*1080*2)
	dst := make([]byte, 1920*1088*3/2)
	b.SetBytes(1920 * 1080 * 2)
	for i := 0; i < b.N; i++ {
		YUYVToNV12(dst, 1920, 1920*1088, src, 1920*2, image.Rect(0, 0, 1920, 1080))
	}
}
