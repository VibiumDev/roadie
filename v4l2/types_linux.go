//go:build linux && (arm64 || amd64)

// Package v4l2 talks to Video4Linux2 devices with plain ioctls, in pure Go:
// UVC capture (the HDMI dongle) and memory-to-memory codecs (the Pi 4's
// H.264 encoder on /dev/video11 and its ISP on /dev/video12).
//
// The struct layouts and constants in this file were generated with
// `go tool cgo -godefs` from linux/videodev2.h (Linux 6.18) and are the
// same on every 64-bit Linux. Unions are kept as byte arrays with accessors.
package v4l2

import "unsafe"

// Capability is struct v4l2_capability.
type Capability struct {
	Driver       [16]byte
	Card         [32]byte
	BusInfo      [32]byte
	Version      uint32
	Capabilities uint32
	DeviceCaps   uint32
	Reserved     [3]uint32
}

// PixFormat is struct v4l2_pix_format, the single-plane image format.
type PixFormat struct {
	Width        uint32
	Height       uint32
	PixelFormat  uint32
	Field        uint32
	BytesPerLine uint32
	SizeImage    uint32
	Colorspace   uint32
	Priv         uint32
	Flags        uint32
	YCbCrEnc     uint32
	Quantization uint32
	XferFunc     uint32
}

// PlanePixFormat is struct v4l2_plane_pix_format.
type PlanePixFormat struct {
	SizeImage    uint32
	BytesPerLine uint32
	Reserved     [6]uint16
}

// PixFormatMplane is struct v4l2_pix_format_mplane, the multi-planar image
// format the codec devices use.
type PixFormatMplane struct {
	Width        uint32
	Height       uint32
	PixelFormat  uint32
	Field        uint32
	Colorspace   uint32
	PlaneFmt     [8]PlanePixFormat
	NumPlanes    uint8
	Flags        uint8
	YCbCrEnc     uint8
	Quantization uint8
	XferFunc     uint8
	Reserved     [7]uint8
}

// Format is struct v4l2_format; Fmt is the union, read through Pix or PixMP.
type Format struct {
	Type uint32
	_    [4]byte
	Fmt  [200]byte
}

// Pix views the union as a single-plane format.
func (f *Format) Pix() *PixFormat { return (*PixFormat)(unsafe.Pointer(&f.Fmt[0])) }

// PixMP views the union as a multi-planar format.
func (f *Format) PixMP() *PixFormatMplane { return (*PixFormatMplane)(unsafe.Pointer(&f.Fmt[0])) }

// RequestBuffers is struct v4l2_requestbuffers.
type RequestBuffers struct {
	Count        uint32
	Type         uint32
	Memory       uint32
	Capabilities uint32
	Flags        uint8
	Reserved     [3]uint8
}

// Plane is struct v4l2_plane. M is the union of mem_offset, userptr and fd.
type Plane struct {
	BytesUsed  uint32
	Length     uint32
	M          [8]byte
	DataOffset uint32
	Reserved   [11]uint32
}

// MemOffset is the mmap offset of an MMAP plane.
func (p *Plane) MemOffset() uint32 { return *(*uint32)(unsafe.Pointer(&p.M[0])) }

// SetFD sets the dmabuf file descriptor of a DMABUF plane.
func (p *Plane) SetFD(fd int32) { *(*int32)(unsafe.Pointer(&p.M[0])) = fd }

// Timeval is struct timeval as the kernel lays it out on 64-bit Linux.
type Timeval struct {
	Sec  int64
	Usec int64
}

// Timecode is struct v4l2_timecode.
type Timecode struct {
	Type     uint32
	Flags    uint32
	Frames   uint8
	Seconds  uint8
	Minutes  uint8
	Hours    uint8
	Userbits [4]uint8
}

// Buffer is struct v4l2_buffer. M is the union of offset, userptr, planes
// pointer and fd.
type Buffer struct {
	Index     uint32
	Type      uint32
	BytesUsed uint32
	Flags     uint32
	Field     uint32
	Timestamp Timeval
	Timecode  Timecode
	Sequence  uint32
	Memory    uint32
	M         [8]byte
	Length    uint32
	Reserved2 uint32
	RequestFD int32
	_         [4]byte
}

// MemOffset is the mmap offset of a single-plane MMAP buffer.
func (b *Buffer) MemOffset() uint32 { return *(*uint32)(unsafe.Pointer(&b.M[0])) }

// SetPlanes points a multi-planar buffer at its planes array. The caller
// keeps planes alive across the ioctl.
func (b *Buffer) SetPlanes(planes []Plane) {
	*(*uintptr)(unsafe.Pointer(&b.M[0])) = uintptr(unsafe.Pointer(&planes[0]))
	b.Length = uint32(len(planes))
}

// Fract is struct v4l2_fract.
type Fract struct {
	Numerator   uint32
	Denominator uint32
}

// CaptureParm is struct v4l2_captureparm.
type CaptureParm struct {
	Capability   uint32
	CaptureMode  uint32
	TimePerFrame Fract
	ExtendedMode uint32
	ReadBuffers  uint32
	Reserved     [4]uint32
}

// StreamParm is struct v4l2_streamparm; Parm is the union.
type StreamParm struct {
	Type uint32
	Parm [200]byte
}

// Capture views the union as capture parameters.
func (p *StreamParm) Capture() *CaptureParm { return (*CaptureParm)(unsafe.Pointer(&p.Parm[0])) }

// Control is struct v4l2_control.
type Control struct {
	ID    uint32
	Value int32
}

// Rect is struct v4l2_rect.
type Rect struct {
	Left   int32
	Top    int32
	Width  uint32
	Height uint32
}

// Selection is struct v4l2_selection.
type Selection struct {
	Type     uint32
	Target   uint32
	Flags    uint32
	R        Rect
	Reserved [9]uint32
}

// ExportBuffer is struct v4l2_exportbuffer.
type ExportBuffer struct {
	Type     uint32
	Index    uint32
	Plane    uint32
	Flags    uint32
	FD       int32
	Reserved [11]uint32
}

// FmtDesc is struct v4l2_fmtdesc.
type FmtDesc struct {
	Index       uint32
	Type        uint32
	Flags       uint32
	Description [32]byte
	PixelFormat uint32
	MBusCode    uint32
	Reserved    [3]uint32
}

// FrmSizeEnum is struct v4l2_frmsizeenum with the union sized for stepwise.
type FrmSizeEnum struct {
	Index       uint32
	PixelFormat uint32
	Type        uint32
	Discrete    struct{ Width, Height uint32 }
	_           [16]byte
	Reserved    [2]uint32
}

// FrmIvalEnum is struct v4l2_frmivalenum with the union sized for stepwise.
type FrmIvalEnum struct {
	Index       uint32
	PixelFormat uint32
	Width       uint32
	Height      uint32
	Type        uint32
	Discrete    Fract
	_           [16]byte
	Reserved    [2]uint32
}

// ioctl request numbers (VIDIOC_*).
const (
	VIDIOC_QUERYCAP            = 0x80685600
	VIDIOC_ENUM_FMT            = 0xc0405602
	VIDIOC_G_FMT               = 0xc0d05604
	VIDIOC_S_FMT               = 0xc0d05605
	VIDIOC_TRY_FMT             = 0xc0d05640
	VIDIOC_REQBUFS             = 0xc0145608
	VIDIOC_QUERYBUF            = 0xc0585609
	VIDIOC_QBUF                = 0xc058560f
	VIDIOC_DQBUF               = 0xc0585611
	VIDIOC_EXPBUF              = 0xc0405610
	VIDIOC_STREAMON            = 0x40045612
	VIDIOC_STREAMOFF           = 0x40045613
	VIDIOC_G_PARM              = 0xc0cc5615
	VIDIOC_S_PARM              = 0xc0cc5616
	VIDIOC_G_CTRL              = 0xc008561b
	VIDIOC_S_CTRL              = 0xc008561c
	VIDIOC_G_SELECTION         = 0xc040565e
	VIDIOC_S_SELECTION         = 0xc040565f
	VIDIOC_ENUM_FRAMESIZES     = 0xc02c564a
	VIDIOC_ENUM_FRAMEINTERVALS = 0xc034564b
)

// Buffer types, memory types and fields.
const (
	BufTypeVideoCapture       = 0x1
	BufTypeVideoOutput        = 0x2
	BufTypeVideoCaptureMplane = 0x9
	BufTypeVideoOutputMplane  = 0xa

	MemoryMMAP   = 0x1
	MemoryDMABUF = 0x4

	FieldAny  = 0x0
	FieldNone = 0x1

	FrmSizeTypeDiscrete = 1
	FrmIvalTypeDiscrete = 1
)

// Capability bits.
const (
	CapVideoCapture   = 0x1
	CapVideoM2MMplane = 0x4000
	CapVideoM2M       = 0x8000
	CapStreaming      = 0x4000000
	CapDeviceCaps     = 0x80000000
)

// Pixel formats (fourcc).
const (
	PixFmtYUYV   = 0x56595559 // YUYV 4:2:2 packed
	PixFmtUYVY   = 0x59565955
	PixFmtNV12   = 0x3231564e // Y plane then interleaved UV, 4:2:0
	PixFmtYUV420 = 0x32315559 // I420 planar
	PixFmtMJPEG  = 0x47504a4d
	PixFmtH264   = 0x34363248
)

// Buffer flags.
const (
	BufFlagKeyframe           = 0x8
	BufFlagPFrame             = 0x10
	BufFlagError              = 0x40
	BufFlagTimestampMonotonic = 0x2000
	BufFlagTimestampCopy      = 0x4000
	BufFlagLast               = 0x100000
)

// Selection targets.
const (
	SelTgtCrop        = 0x0
	SelTgtCropDefault = 0x1
	SelTgtCropBounds  = 0x2
	SelTgtCompose     = 0x100
)

// Codec controls (V4L2_CID_MPEG_VIDEO_*) and their menu values.
const (
	CIDBitrateMode     = 0x9909ce
	CIDBitrate         = 0x9909cf
	CIDGOPSize         = 0x9909cb
	CIDBFrames         = 0x9909ca
	CIDHeaderMode      = 0x9909d8
	CIDRepeatSeqHeader = 0x9909e2
	CIDForceKeyFrame   = 0x9909e5
	CIDH264IPeriod     = 0x990a66
	CIDH264Level       = 0x990a67
	CIDH264Profile     = 0x990a6b
	CIDH264MinQP       = 0x990a61
	CIDH264MaxQP       = 0x990a62

	BitrateModeVBR = 0
	BitrateModeCBR = 1

	HeaderModeSeparate             = 0
	HeaderModeJoinedWithFirstFrame = 1

	H264ProfileBaseline            = 0
	H264ProfileConstrainedBaseline = 1
	H264ProfileMain                = 2
	H264ProfileHigh                = 4

	H264Level31 = 9
	H264Level40 = 11
	H264Level41 = 12
	H264Level42 = 13
)

// FourCC renders a pixel format for logs.
func FourCC(f uint32) string {
	return string([]byte{byte(f), byte(f >> 8), byte(f >> 16), byte(f >> 24)})
}
