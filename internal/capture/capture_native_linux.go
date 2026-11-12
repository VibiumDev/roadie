//go:build linux && (arm64 || amd64)

package capture

import (
	"errors"
	"fmt"
	"image"
	"log"
	"sync"
	"time"

	"github.com/VibiumDev/roadie/internal/devices"
	"github.com/VibiumDev/roadie/v4l2"
	"github.com/pion/mediadevices/pkg/driver"
)

// Native capture on Linux: the dongle's YUYV frames straight from V4L2's
// memory-mapped buffers, in pure Go. It replaces the mediadevices path
// when the dongle offers the wanted size and rate in YUYV, and saves the
// copy, the pixel shuffle and the 4 MB of fresh memory per frame that path
// costs: about a third of a core at 1080p30. The frames stay the driver's
// until released, so FrameBuffer copies the last one when capture stops.

// openNativeCapture starts YUYV capture on the first of the device's V4L2
// nodes that streams video. The node the device was found by may be its
// metadata node.
func openNativeCapture(dev devices.DeviceInfo, width, height, fps int) (frameReader, error) {
	all := driver.GetManager().Query(func(driver.Driver) bool { return true })
	err := errNoNativeCapture
	for _, node := range nativeNodes(all, dev) {
		r, nodeErr := openNativeNode(dev, node, width, height, fps)
		if nodeErr == nil {
			return r, nil
		}
		if errors.Is(nodeErr, v4l2.ErrNotCapture) {
			nodeErr = fmt.Errorf("%w: %v", errNoNativeCapture, nodeErr)
		}
		if errors.Is(err, errNoNativeCapture) {
			err = nodeErr
		}
	}
	return nil, err
}

func openNativeNode(dev devices.DeviceInfo, node string, width, height, fps int) (frameReader, error) {
	cap, err := v4l2.OpenCapture("/dev/" + node)
	if err != nil {
		return nil, err
	}
	f, err := cap.SetFormat(uint32(width), uint32(height), v4l2.PixFmtYUYV)
	if err != nil {
		cap.Close()
		return nil, err
	}
	if f.PixelFormat != v4l2.PixFmtYUYV || int(f.Width) != width || int(f.Height) != height {
		cap.Close()
		return nil, fmt.Errorf("%w: %s offers %s %dx%d, not YUYV %dx%d", errNoNativeCapture, node, v4l2.FourCC(f.PixelFormat), f.Width, f.Height, width, height)
	}
	got, err := cap.SetFrameRate(uint32(fps))
	if err != nil {
		cap.Close()
		return nil, err
	}
	if got < float64(fps)*0.9 {
		cap.Close()
		return nil, fmt.Errorf("%w: %s gives %.0f fps in YUYV %dx%d, not %d", errNoNativeCapture, node, got, width, height, fps)
	}
	if err := cap.Start(4); err != nil {
		cap.Close()
		return nil, err
	}
	log.Printf("device %q: YUYV %dx%d at %.0f fps through V4L2 (%s)", dev.Name, width, height, got, node)
	return &nativeReader{cap: cap, stride: int(f.BytesPerLine), size: int(f.SizeImage), w: width, h: height}, nil
}

// nativeReader hands out frames that alias the driver's buffers.
type nativeReader struct {
	cap    *v4l2.Capture
	stride int
	size   int
	w, h   int
	mu     sync.Mutex
	closed bool
}

func (r *nativeReader) Read() (image.Image, func(), error) {
	frame, err := r.cap.Read(2 * time.Second)
	if err != nil {
		if errors.Is(err, v4l2.ErrAgain) {
			return nil, func() {}, errors.New("no frame in 2 s")
		}
		return nil, func() {}, err
	}
	if len(frame.Data) < r.size {
		// A short frame is a torn one.
		r.cap.Release(frame)
		return nil, func() {}, fmt.Errorf("short frame: %d of %d bytes", len(frame.Data), r.size)
	}
	img := v4l2.NewYUYV(frame.Data, r.stride, r.w, r.h, frame.Timestamp)
	var once sync.Once
	release := func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if !r.closed {
				r.cap.Release(frame)
			}
		})
	}
	return img, release, nil
}

func (r *nativeReader) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return r.cap.Close()
}
