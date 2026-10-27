//go:build linux && (arm64 || amd64)

package v4l2

import (
	"testing"
	"unsafe"
)

// An ioctl request number encodes the size of its argument, so the struct
// layouts can be checked against the kernel's without a device.
func ioctlSize(req uint) uintptr { return uintptr((req >> 16) & 0x3fff) }

func TestStructSizesMatchIoctls(t *testing.T) {
	cases := []struct {
		name string
		req  uint
		size uintptr
	}{
		{"Capability", VIDIOC_QUERYCAP, unsafe.Sizeof(Capability{})},
		{"FmtDesc", VIDIOC_ENUM_FMT, unsafe.Sizeof(FmtDesc{})},
		{"Format", VIDIOC_S_FMT, unsafe.Sizeof(Format{})},
		{"RequestBuffers", VIDIOC_REQBUFS, unsafe.Sizeof(RequestBuffers{})},
		{"Buffer", VIDIOC_QBUF, unsafe.Sizeof(Buffer{})},
		{"ExportBuffer", VIDIOC_EXPBUF, unsafe.Sizeof(ExportBuffer{})},
		{"StreamParm", VIDIOC_S_PARM, unsafe.Sizeof(StreamParm{})},
		{"Control", VIDIOC_S_CTRL, unsafe.Sizeof(Control{})},
		{"Selection", VIDIOC_S_SELECTION, unsafe.Sizeof(Selection{})},
		{"FrmSizeEnum", VIDIOC_ENUM_FRAMESIZES, unsafe.Sizeof(FrmSizeEnum{})},
		{"FrmIvalEnum", VIDIOC_ENUM_FRAMEINTERVALS, unsafe.Sizeof(FrmIvalEnum{})},
	}
	for _, c := range cases {
		if got := ioctlSize(c.req); got != c.size {
			t.Errorf("%s is %d bytes, the ioctl expects %d", c.name, c.size, got)
		}
	}
	if s := unsafe.Sizeof(Plane{}); s != 64 {
		t.Errorf("Plane is %d bytes, want 64", s)
	}
	if s := unsafe.Sizeof(PixFormatMplane{}); s > 200 {
		t.Errorf("PixFormatMplane is %d bytes, more than the union holds", s)
	}
	if s := unsafe.Sizeof(PixFormat{}); s > 200 {
		t.Errorf("PixFormat is %d bytes, more than the union holds", s)
	}
}

func TestFourCC(t *testing.T) {
	if got := FourCC(PixFmtYUYV); got != "YUYV" {
		t.Errorf("FourCC(YUYV) = %q", got)
	}
	if got := FourCC(PixFmtH264); got != "H264" {
		t.Errorf("FourCC(H264) = %q", got)
	}
}
