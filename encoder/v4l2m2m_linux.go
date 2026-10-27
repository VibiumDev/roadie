//go:build linux && (arm64 || amd64)

package encoder

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/VibiumDev/roadie/v4l2"
	"golang.org/x/sys/unix"
)

const defaultEncoderDevice = "/dev/video11"

// v4l2m2m is the Pi 4's hardware encoder, bcm2835-codec, driven directly.
// Frames go in as NV12; the device copies each frame's timestamp to its
// output. Rate control is VBR: the hardware's CBR mode stalls above about
// 1.4 megapixels (see docs/how-to/measure-encoder.md).
type v4l2m2m struct {
	m       *v4l2.M2M
	in      v4l2.PixFormatMplane
	uvOff   int
	cfg     Config
	mu      sync.Mutex // the M2M queues are not safe for two goroutines
	dropped int
	closed  bool
	samples chan Sample
	stop    chan struct{}
	done    chan struct{}
}

// probeV4L2M2M checks that the device exists, is a memory-to-memory codec
// and produces H.264.
func probeV4L2M2M(device string) error {
	if device == "" {
		device = defaultEncoderDevice
	}
	if _, err := os.Stat(device); err != nil {
		return fmt.Errorf("%s: %w", device, ErrUnavailable)
	}
	m, err := v4l2.OpenM2M(device)
	if err != nil {
		return fmt.Errorf("%v: %w", err, ErrUnavailable)
	}
	defer m.Close()
	fmts, err := m.Device().EnumFormats(v4l2.BufTypeVideoCaptureMplane)
	if err != nil {
		return fmt.Errorf("%v: %w", err, ErrUnavailable)
	}
	for _, f := range fmts {
		if f.PixelFormat == v4l2.PixFmtH264 {
			return nil
		}
	}
	return fmt.Errorf("%s does not encode H.264: %w", device, ErrUnavailable)
}

func newV4L2M2M(cfg Config) (Encoder, error) {
	if err := probeV4L2M2M(cfg.Device); err != nil {
		return nil, err
	}
	device := cfg.Device
	if device == "" {
		device = defaultEncoderDevice
	}
	if cfg.FPS <= 0 {
		cfg.FPS = 30
	}
	if cfg.Bitrate <= 0 {
		cfg.Bitrate = 2_000_000
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width&1 != 0 || cfg.Height&1 != 0 {
		return nil, fmt.Errorf("v4l2m2m: bad size %dx%d", cfg.Width, cfg.Height)
	}
	m, err := v4l2.OpenM2M(device)
	if err != nil {
		return nil, err
	}
	e := &v4l2m2m{m: m, cfg: cfg, samples: make(chan Sample, 16), stop: make(chan struct{}), done: make(chan struct{})}
	if err := e.setup(); err != nil {
		m.Close()
		return nil, err
	}
	go e.read()
	return e, nil
}

func (e *v4l2m2m) setup() error {
	in, err := e.m.SetInputFormat(uint32(e.cfg.Width), uint32(e.cfg.Height), v4l2.PixFmtNV12)
	if err != nil {
		return err
	}
	if int(in.Width) != e.cfg.Width || int(in.Height) != e.cfg.Height || in.PixelFormat != v4l2.PixFmtNV12 {
		return fmt.Errorf("v4l2m2m: device wants %s %dx%d, not NV12 %dx%d", v4l2.FourCC(in.PixelFormat), in.Width, in.Height, e.cfg.Width, e.cfg.Height)
	}
	e.in = in
	stride := int(in.PlaneFmt[0].BytesPerLine)
	e.uvOff = int(in.PlaneFmt[0].SizeImage) * 2 / 3 / stride * stride
	if _, err := e.m.SetOutputFormat(uint32(e.cfg.Width), uint32(e.cfg.Height), v4l2.PixFmtH264, 1<<20); err != nil {
		return err
	}
	gop := int32(e.cfg.GOPFrames())
	for _, c := range []struct {
		id uint32
		v  int32
	}{
		{v4l2.CIDH264Profile, v4l2.H264ProfileConstrainedBaseline},
		{v4l2.CIDH264Level, v4l2.H264Level40},
		{v4l2.CIDBitrateMode, v4l2.BitrateModeVBR},
		{v4l2.CIDBitrate, int32(e.cfg.Bitrate)},
		{v4l2.CIDGOPSize, gop},
		{v4l2.CIDH264IPeriod, gop},
		{v4l2.CIDRepeatSeqHeader, 1},
		{v4l2.CIDHeaderMode, v4l2.HeaderModeJoinedWithFirstFrame},
	} {
		if err := e.m.SetControl(c.id, c.v); err != nil {
			return err
		}
	}
	return e.m.Start(4, 4, false)
}

// read collects results and returns finished input buffers until stop.
func (e *v4l2m2m) read() {
	defer close(e.done)
	for {
		select {
		case <-e.stop:
			return
		default:
		}
		ev, err := e.m.Poll(100 * time.Millisecond)
		if err != nil {
			return
		}
		if ev == 0 {
			continue
		}
		e.mu.Lock()
		if e.closed {
			e.mu.Unlock()
			return
		}
		if ev&unix.POLLOUT != 0 {
			e.m.ReapInput()
		}
		var out *Sample
		if ev&unix.POLLIN != 0 {
			res, err := e.m.Dequeue()
			if err == nil {
				data := make([]byte, len(res.Data))
				copy(data, res.Data)
				out = &Sample{Data: data, Keyframe: res.Flags&v4l2.BufFlagKeyframe != 0, Timestamp: res.Timestamp}
				e.m.Release(res)
			} else if err != v4l2.ErrAgain {
				e.mu.Unlock()
				return
			}
		}
		e.mu.Unlock()
		if out != nil {
			select {
			case e.samples <- *out:
			case <-e.stop:
				return
			}
		}
	}
}

func (e *v4l2m2m) Encode(f Frame) error {
	if f.Rect.Dx() != e.cfg.Width || f.Rect.Dy() != e.cfg.Height {
		return fmt.Errorf("frame is %dx%d, encoder wants %dx%d", f.Rect.Dx(), f.Rect.Dy(), e.cfg.Width, e.cfg.Height)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errors.New("encoder closed")
	}
	e.m.ReapInput()
	idx, buf, ok := e.m.InputBuffer()
	if !ok {
		e.dropped++
		return nil
	}
	NV12(buf, int(e.in.PlaneFmt[0].BytesPerLine), e.uvOff, f.Img, f.Rect)
	ts := f.Timestamp
	if ts == 0 {
		ts = 1 // zero means "no timestamp" to the driver
	}
	return e.m.QueueInput(idx, int(e.in.PlaneFmt[0].SizeImage), ts)
}

func (e *v4l2m2m) Samples() <-chan Sample { return e.samples }

func (e *v4l2m2m) Dropped() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dropped
}

func (e *v4l2m2m) SetBitrate(bps int) error {
	if bps <= 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	return e.m.SetControl(v4l2.CIDBitrate, int32(bps))
}

func (e *v4l2m2m) ForceKeyframe() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	return e.m.SetControl(v4l2.CIDForceKeyFrame, 1)
}

func (e *v4l2m2m) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	close(e.stop)
	e.mu.Unlock()
	<-e.done
	err := e.m.Close()
	close(e.samples)
	return err
}
