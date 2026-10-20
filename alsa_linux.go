//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// A minimal ALSA PCM client speaking the kernel's ioctl interface directly
// (include/uapi/sound/asound.h): interleaved S16_LE, non-blocking, with
// poll. No cgo and no libasound, so the binary still cross-compiles, and the
// software parameters that make an underrun play silence -- rather than stop
// the stream or replay stale audio -- are ours to set.

const (
	sndAccessRWInterleaved = 3
	sndFormatS16LE         = 2
	sndSubformatStd        = 0

	hwpAccess     = 0 // masks
	hwpFormat     = 1
	hwpSubformat  = 2
	hwpSampleBits = 8 // intervals
	hwpFrameBits  = 9
	hwpChannels   = 10
	hwpRate       = 11
	hwpPeriodSize = 13
	hwpPeriods    = 15
	hwpBufferSize = 17

	firstInterval   = 8
	intervalInteger = 1 << 2 // the "integer" bit of snd_interval's bitfield
)

type sndMask struct{ bits [8]uint32 }

type sndInterval struct {
	min, max uint32
	flags    uint32
}

// snd_pcm_hw_params. snd_pcm_uframes_t is unsigned long, which is Go's uint
// on Linux, so these structs have the kernel's layout on 32- and 64-bit
// alike; TestALSAStructSizes pins the 64-bit sizes.
type sndHwParams struct {
	flags     uint32
	masks     [3]sndMask
	mres      [5]sndMask
	intervals [12]sndInterval
	ires      [9]sndInterval
	rmask     uint32
	cmask     uint32
	info      uint32
	msbits    uint32
	rateNum   uint32
	rateDen   uint32
	fifoSize  uint
	sync      [16]byte
	reserved  [48]byte
}

type sndSwParams struct {
	tstampMode       int32
	periodStep       uint32
	sleepMin         uint32
	availMin         uint
	xferAlign        uint
	startThreshold   uint
	stopThreshold    uint
	silenceThreshold uint
	silenceSize      uint
	boundary         uint
	proto            uint32
	tstampType       uint32
	reserved         [56]byte
}

type sndXferi struct {
	result int
	buf    unsafe.Pointer
	frames uint
}

func ioc(dir, nr, size uintptr) uintptr { return dir<<30 | size<<16 | 'A'<<8 | nr }

const (
	iocNone  = 0
	iocWrite = 1
	iocRead  = 2
)

var (
	ioctlHwParams = ioc(iocRead|iocWrite, 0x11, unsafe.Sizeof(sndHwParams{}))
	ioctlSwParams = ioc(iocRead|iocWrite, 0x13, unsafe.Sizeof(sndSwParams{}))
	ioctlPrepare  = ioc(iocNone, 0x40, 0)
	ioctlStart    = ioc(iocNone, 0x42, 0)
	ioctlDrop     = ioc(iocNone, 0x43, 0)
	ioctlResume   = ioc(iocNone, 0x47, 0)
	ioctlWriteI   = ioc(iocWrite, 0x50, unsafe.Sizeof(sndXferi{}))
	ioctlReadI    = ioc(iocRead, 0x51, unsafe.Sizeof(sndXferi{}))
)

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
		if e == unix.EINTR {
			continue
		}
		if e != 0 {
			return e
		}
		return nil
	}
}

func (p *sndHwParams) any() {
	for i := range p.masks {
		for j := range p.masks[i].bits {
			p.masks[i].bits[j] = ^uint32(0)
		}
	}
	for i := range p.intervals {
		p.intervals[i] = sndInterval{min: 0, max: ^uint32(0)}
	}
	p.rmask = ^uint32(0)
	p.info = ^uint32(0)
}

func (p *sndHwParams) setMask(param, bit int) {
	m := &p.masks[param]
	m.bits = [8]uint32{}
	m.bits[bit>>5] = 1 << (bit & 31)
}

func (p *sndHwParams) setInt(param int, v uint32) {
	p.intervals[param-firstInterval] = sndInterval{min: v, max: v, flags: intervalInteger}
}

func (p *sndHwParams) setMin(param int, v uint32) {
	p.intervals[param-firstInterval].min = v
}

func (p *sndHwParams) get(param int) uint32 { return p.intervals[param-firstInterval].min }

// alsaPCM is one open PCM substream.
type alsaPCM struct {
	fd       int
	path     string
	playback bool
	params   pcmParams
}

func pcmPath(card, device int, playback bool) string {
	dir := 'c'
	if playback {
		dir = 'p'
	}
	return fmt.Sprintf("/dev/snd/pcmC%dD%d%c", card, device, dir)
}

// openPCM opens device 0 of a card for S16_LE interleaved I/O. want gives
// the rate and channels the device must have, and the period size and count
// to ask for; the result is what the driver granted.
func openPCM(card int, playback bool, want pcmParams) (pcmStream, error) {
	return openPCMDevice(card, 0, playback, want)
}

func openPCMDevice(card, device int, playback bool, want pcmParams) (pcmStream, error) {
	path := pcmPath(card, device, playback)
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	p := &alsaPCM{fd: fd, path: path, playback: playback}
	if err := p.setup(want); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return p, nil
}

func (p *alsaPCM) setup(want pcmParams) error {
	var hw sndHwParams
	hw.any()
	hw.setMask(hwpAccess, sndAccessRWInterleaved)
	hw.setMask(hwpFormat, sndFormatS16LE)
	hw.setMask(hwpSubformat, sndSubformatStd)
	hw.setInt(hwpSampleBits, 16)
	hw.setInt(hwpFrameBits, uint32(16*want.channels))
	hw.setInt(hwpChannels, uint32(want.channels))
	hw.setInt(hwpRate, uint32(want.rate))
	hw.setMin(hwpPeriodSize, uint32(want.periodFrames))
	hw.setInt(hwpPeriods, uint32(want.periods))
	if err := ioctl(p.fd, ioctlHwParams, unsafe.Pointer(&hw)); err != nil {
		return fmt.Errorf("%s: %d Hz, %d channel S16_LE, %d periods of %d frames not available: %w",
			p.path, want.rate, want.channels, want.periods, want.periodFrames, err)
	}
	p.params = pcmParams{
		rate:         int(hw.get(hwpRate)),
		channels:     int(hw.get(hwpChannels)),
		periodFrames: int(hw.get(hwpPeriodSize)),
		periods:      int(hw.get(hwpPeriods)),
	}
	buffer := uint(hw.get(hwpBufferSize))
	period := uint(p.params.periodFrames)

	sw := sndSwParams{
		periodStep: 1,
		availMin:   period,
		xferAlign:  1,
	}
	if p.playback {
		// Start once two periods are queued; never stop on underrun.
		sw.startThreshold = 2 * period
		sw.stopThreshold = buffer
	} else {
		sw.startThreshold = 1
		sw.stopThreshold = buffer
	}
	if err := ioctl(p.fd, ioctlSwParams, unsafe.Pointer(&sw)); err != nil {
		return fmt.Errorf("%s: software parameters: %w", p.path, err)
	}
	if p.playback {
		// The first call reported the boundary. A stop threshold at the
		// boundary means an underrun never stops the stream; silence_size
		// at the boundary with a zero threshold has ALSA zero everything
		// already played, so an underrun plays silence -- not the last
		// buffer's worth of audio again, and not an XRUN.
		sw.stopThreshold = sw.boundary
		sw.silenceThreshold = 0
		sw.silenceSize = sw.boundary
		if err := ioctl(p.fd, ioctlSwParams, unsafe.Pointer(&sw)); err != nil {
			return fmt.Errorf("%s: silence on underrun: %w", p.path, err)
		}
	}
	if err := ioctl(p.fd, ioctlPrepare, nil); err != nil {
		return fmt.Errorf("%s: prepare: %w", p.path, err)
	}
	if !p.playback {
		if err := ioctl(p.fd, ioctlStart, nil); err != nil {
			return fmt.Errorf("%s: start: %w", p.path, err)
		}
	}
	return nil
}

func (p *alsaPCM) Params() pcmParams { return p.params }

// Wait blocks until a period can be written (or read), the timeout passes,
// or the stream reports an error -- in which case the next transfer says
// which one.
func (p *alsaPCM) Wait(timeout time.Duration) (bool, error) {
	ev := int16(unix.POLLIN)
	if p.playback {
		ev = unix.POLLOUT
	}
	fds := []unix.PollFd{{Fd: int32(p.fd), Events: ev}}
	n, err := unix.Poll(fds, int(timeout/time.Millisecond))
	if err == unix.EINTR {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if fds[0].Revents&unix.POLLNVAL != 0 {
		return false, errPCMGone
	}
	return true, nil
}

func (p *alsaPCM) transfer(req uintptr, b []byte) (int, error) {
	frameBytes := 2 * p.params.channels
	frames := len(b) / frameBytes
	if frames == 0 {
		return 0, nil
	}
	x := sndXferi{buf: unsafe.Pointer(&b[0]), frames: uint(frames)}
	err := ioctl(p.fd, req, unsafe.Pointer(&x))
	runtime.KeepAlive(b)
	switch err {
	case nil:
		return x.result, nil
	case unix.EAGAIN:
		return 0, errPCMAgain
	case unix.EPIPE:
		return 0, errPCMXrun
	case unix.ESTRPIPE:
		return 0, errPCMSuspended
	case unix.ENODEV, unix.EBADFD, unix.EIO, unix.ENXIO:
		return 0, fmt.Errorf("%s: %w (%v)", p.path, errPCMGone, err)
	}
	return 0, fmt.Errorf("%s: %w", p.path, err)
}

func (p *alsaPCM) WriteFrames(b []byte) (int, error) { return p.transfer(ioctlWriteI, b) }
func (p *alsaPCM) ReadFrames(b []byte) (int, error)  { return p.transfer(ioctlReadI, b) }

// Recover brings the stream back after an xrun or a suspend.
func (p *alsaPCM) Recover(cause error) error {
	if errors.Is(cause, errPCMSuspended) {
		for i := 0; i < 50; i++ {
			err := ioctl(p.fd, ioctlResume, nil)
			if err == nil {
				return nil
			}
			if err != unix.EAGAIN {
				break // not resumable: fall through to prepare
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := ioctl(p.fd, ioctlPrepare, nil); err != nil {
		return fmt.Errorf("%s: prepare: %w", p.path, err)
	}
	if !p.playback {
		if err := ioctl(p.fd, ioctlStart, nil); err != nil {
			return fmt.Errorf("%s: start: %w", p.path, err)
		}
	}
	return nil
}

func (p *alsaPCM) Close() error {
	ioctl(p.fd, ioctlDrop, nil)
	return unix.Close(p.fd)
}
