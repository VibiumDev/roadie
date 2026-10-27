//go:build linux && (arm64 || amd64)

package v4l2

import (
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Device is an open V4L2 device node.
type Device struct {
	fd   int
	path string
}

// Open opens a device node non-blocking, so dequeues never stall and Poll
// decides when to wait.
func Open(path string) (*Device, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &Device{fd: fd, path: path}, nil
}

// Close closes the node.
func (d *Device) Close() error {
	if d.fd < 0 {
		return nil
	}
	err := unix.Close(d.fd)
	d.fd = -1
	return err
}

// FD is the file descriptor, for polling alongside other devices.
func (d *Device) FD() int { return d.fd }

// Path is the device node.
func (d *Device) Path() string { return d.path }

func (d *Device) ioctl(req uint, arg unsafe.Pointer) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(d.fd), uintptr(req), uintptr(arg))
		if errno == 0 {
			return nil
		}
		if errno == unix.EINTR {
			continue
		}
		return errno
	}
}

// QueryCap returns the device's capabilities.
func (d *Device) QueryCap() (Capability, error) {
	var c Capability
	if err := d.ioctl(VIDIOC_QUERYCAP, unsafe.Pointer(&c)); err != nil {
		return c, fmt.Errorf("%s: QUERYCAP: %w", d.path, err)
	}
	return c, nil
}

// Caps are the capabilities that apply to this node (device_caps when the
// driver reports them, else the whole device's).
func (c Capability) Caps() uint32 {
	if c.Capabilities&CapDeviceCaps != 0 {
		return c.DeviceCaps
	}
	return c.Capabilities
}

// EnumFormats lists the pixel formats of a buffer type.
func (d *Device) EnumFormats(bufType uint32) ([]FmtDesc, error) {
	var out []FmtDesc
	for i := uint32(0); ; i++ {
		fd := FmtDesc{Index: i, Type: bufType}
		if err := d.ioctl(VIDIOC_ENUM_FMT, unsafe.Pointer(&fd)); err != nil {
			if errors.Is(err, unix.EINVAL) {
				return out, nil
			}
			return out, fmt.Errorf("%s: ENUM_FMT: %w", d.path, err)
		}
		out = append(out, fd)
	}
}

// FrameSizes lists the discrete frame sizes of a pixel format.
func (d *Device) FrameSizes(pixfmt uint32) ([][2]uint32, error) {
	var out [][2]uint32
	for i := uint32(0); ; i++ {
		fs := FrmSizeEnum{Index: i, PixelFormat: pixfmt}
		if err := d.ioctl(VIDIOC_ENUM_FRAMESIZES, unsafe.Pointer(&fs)); err != nil {
			if errors.Is(err, unix.EINVAL) {
				return out, nil
			}
			return out, fmt.Errorf("%s: ENUM_FRAMESIZES: %w", d.path, err)
		}
		if fs.Type != FrmSizeTypeDiscrete {
			return out, nil
		}
		out = append(out, [2]uint32{fs.Discrete.Width, fs.Discrete.Height})
	}
}

// FrameRates lists the discrete frame rates of a size, in frames per second.
func (d *Device) FrameRates(pixfmt, width, height uint32) ([]float64, error) {
	var out []float64
	for i := uint32(0); ; i++ {
		fi := FrmIvalEnum{Index: i, PixelFormat: pixfmt, Width: width, Height: height}
		if err := d.ioctl(VIDIOC_ENUM_FRAMEINTERVALS, unsafe.Pointer(&fi)); err != nil {
			if errors.Is(err, unix.EINVAL) {
				return out, nil
			}
			return out, fmt.Errorf("%s: ENUM_FRAMEINTERVALS: %w", d.path, err)
		}
		if fi.Type != FrmIvalTypeDiscrete || fi.Discrete.Numerator == 0 {
			return out, nil
		}
		out = append(out, float64(fi.Discrete.Denominator)/float64(fi.Discrete.Numerator))
	}
}

// GetFormat reads the current format of a buffer type.
func (d *Device) GetFormat(bufType uint32) (Format, error) {
	f := Format{Type: bufType}
	if err := d.ioctl(VIDIOC_G_FMT, unsafe.Pointer(&f)); err != nil {
		return f, fmt.Errorf("%s: G_FMT: %w", d.path, err)
	}
	return f, nil
}

// SetFormat negotiates a format; the driver writes back what it chose.
func (d *Device) SetFormat(f *Format) error {
	if err := d.ioctl(VIDIOC_S_FMT, unsafe.Pointer(f)); err != nil {
		return fmt.Errorf("%s: S_FMT: %w", d.path, err)
	}
	return nil
}

// TryFormat asks what SetFormat would choose without changing anything.
func (d *Device) TryFormat(f *Format) error {
	if err := d.ioctl(VIDIOC_TRY_FMT, unsafe.Pointer(f)); err != nil {
		return fmt.Errorf("%s: TRY_FMT: %w", d.path, err)
	}
	return nil
}

// SetFrameRate asks a capture node for fps frames per second and returns
// what it granted.
func (d *Device) SetFrameRate(fps uint32) (float64, error) {
	p := StreamParm{Type: BufTypeVideoCapture}
	p.Capture().TimePerFrame = Fract{Numerator: 1, Denominator: fps}
	if err := d.ioctl(VIDIOC_S_PARM, unsafe.Pointer(&p)); err != nil {
		return 0, fmt.Errorf("%s: S_PARM: %w", d.path, err)
	}
	tpf := p.Capture().TimePerFrame
	if tpf.Numerator == 0 {
		return 0, nil
	}
	return float64(tpf.Denominator) / float64(tpf.Numerator), nil
}

// GetControl reads a control.
func (d *Device) GetControl(id uint32) (int32, error) {
	c := Control{ID: id}
	if err := d.ioctl(VIDIOC_G_CTRL, unsafe.Pointer(&c)); err != nil {
		return 0, fmt.Errorf("%s: G_CTRL %#x: %w", d.path, id, err)
	}
	return c.Value, nil
}

// SetControl writes a control.
func (d *Device) SetControl(id uint32, value int32) error {
	c := Control{ID: id, Value: value}
	if err := d.ioctl(VIDIOC_S_CTRL, unsafe.Pointer(&c)); err != nil {
		return fmt.Errorf("%s: S_CTRL %#x=%d: %w", d.path, id, value, err)
	}
	return nil
}

// SetSelection sets a crop or compose rectangle and returns what the driver
// settled on.
func (d *Device) SetSelection(bufType, target uint32, r Rect) (Rect, error) {
	s := Selection{Type: bufType, Target: target, R: r}
	if err := d.ioctl(VIDIOC_S_SELECTION, unsafe.Pointer(&s)); err != nil {
		return s.R, fmt.Errorf("%s: S_SELECTION: %w", d.path, err)
	}
	return s.R, nil
}

// GetSelection reads a crop or compose rectangle.
func (d *Device) GetSelection(bufType, target uint32) (Rect, error) {
	s := Selection{Type: bufType, Target: target}
	if err := d.ioctl(VIDIOC_G_SELECTION, unsafe.Pointer(&s)); err != nil {
		return s.R, fmt.Errorf("%s: G_SELECTION: %w", d.path, err)
	}
	return s.R, nil
}

// RequestBuffers allocates (or, with count 0, frees) buffers on a queue and
// returns how many the driver gave.
func (d *Device) RequestBuffers(bufType, memory, count uint32) (uint32, error) {
	rb := RequestBuffers{Count: count, Type: bufType, Memory: memory}
	if err := d.ioctl(VIDIOC_REQBUFS, unsafe.Pointer(&rb)); err != nil {
		return 0, fmt.Errorf("%s: REQBUFS: %w", d.path, err)
	}
	return rb.Count, nil
}

// QueryBuffer fills in a buffer's description. For multi-planar types,
// planes receives the per-plane details.
func (d *Device) QueryBuffer(b *Buffer, planes []Plane) error {
	if planes != nil {
		b.SetPlanes(planes)
	}
	err := d.ioctl(VIDIOC_QUERYBUF, unsafe.Pointer(b))
	runtime.KeepAlive(planes)
	if err != nil {
		return fmt.Errorf("%s: QUERYBUF %d: %w", d.path, b.Index, err)
	}
	return nil
}

// Mmap maps a buffer's memory.
func (d *Device) Mmap(offset uint32, length uint32) ([]byte, error) {
	b, err := unix.Mmap(d.fd, int64(offset), int(length), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("%s: mmap: %w", d.path, err)
	}
	return b, nil
}

// Munmap unmaps a buffer mapped with Mmap.
func (d *Device) Munmap(b []byte) error { return unix.Munmap(b) }

// QueueBuffer hands a buffer to the driver.
func (d *Device) QueueBuffer(b *Buffer, planes []Plane) error {
	if planes != nil {
		b.SetPlanes(planes)
	}
	err := d.ioctl(VIDIOC_QBUF, unsafe.Pointer(b))
	runtime.KeepAlive(planes)
	if err != nil {
		return fmt.Errorf("%s: QBUF %d: %w", d.path, b.Index, err)
	}
	return nil
}

// ErrAgain is returned by DequeueBuffer when nothing is ready.
var ErrAgain = unix.EAGAIN

// DequeueBuffer takes a finished buffer back from the driver, or ErrAgain.
func (d *Device) DequeueBuffer(b *Buffer, planes []Plane) error {
	if planes != nil {
		b.SetPlanes(planes)
	}
	err := d.ioctl(VIDIOC_DQBUF, unsafe.Pointer(b))
	runtime.KeepAlive(planes)
	if err != nil {
		if errors.Is(err, unix.EAGAIN) {
			return ErrAgain
		}
		return fmt.Errorf("%s: DQBUF: %w", d.path, err)
	}
	return nil
}

// ExportBuffer exports a buffer plane as a dmabuf file descriptor.
func (d *Device) ExportBuffer(bufType, index, plane uint32) (int, error) {
	e := ExportBuffer{Type: bufType, Index: index, Plane: plane, Flags: unix.O_RDWR | unix.O_CLOEXEC}
	if err := d.ioctl(VIDIOC_EXPBUF, unsafe.Pointer(&e)); err != nil {
		return -1, fmt.Errorf("%s: EXPBUF %d: %w", d.path, index, err)
	}
	return int(e.FD), nil
}

// StreamOn starts a queue.
func (d *Device) StreamOn(bufType uint32) error {
	t := bufType
	if err := d.ioctl(VIDIOC_STREAMON, unsafe.Pointer(&t)); err != nil {
		return fmt.Errorf("%s: STREAMON: %w", d.path, err)
	}
	return nil
}

// StreamOff stops a queue and returns every buffer to the application.
func (d *Device) StreamOff(bufType uint32) error {
	t := bufType
	if err := d.ioctl(VIDIOC_STREAMOFF, unsafe.Pointer(&t)); err != nil {
		return fmt.Errorf("%s: STREAMOFF: %w", d.path, err)
	}
	return nil
}

// Poll waits up to timeout for any of events (unix.POLLIN for a capture
// buffer, unix.POLLOUT for a returned output buffer) and returns the ones
// that happened, zero on timeout.
func (d *Device) Poll(events int16, timeout time.Duration) (int16, error) {
	fds := []unix.PollFd{{Fd: int32(d.fd), Events: events}}
	ms := int(timeout / time.Millisecond)
	if timeout < 0 {
		ms = -1
	}
	for {
		n, err := unix.Poll(fds, ms)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("%s: poll: %w", d.path, err)
		}
		if n == 0 {
			return 0, nil
		}
		return fds[0].Revents, nil
	}
}

// Now is the kernel's monotonic clock, the one buffer timestamps use.
func Now() time.Duration {
	var ts unix.Timespec
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return time.Duration(ts.Sec)*time.Second + time.Duration(ts.Nsec)
}

// Stamp converts a buffer timestamp to the same clock as Now.
func (b *Buffer) Stamp() time.Duration {
	return time.Duration(b.Timestamp.Sec)*time.Second + time.Duration(b.Timestamp.Usec)*time.Microsecond
}

// SetStamp sets a buffer's timestamp from a Now-style duration.
func (b *Buffer) SetStamp(t time.Duration) {
	b.Timestamp.Sec = int64(t / time.Second)
	b.Timestamp.Usec = int64((t % time.Second) / time.Microsecond)
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// Name is the driver's card name.
func (c Capability) Name() string { return cstr(c.Card[:]) }

// DriverName is the kernel driver's name.
func (c Capability) DriverName() string { return cstr(c.Driver[:]) }
