//go:build linux && (arm64 || amd64)

package v4l2

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// Capture streams frames from a single-plane capture node such as a UVC
// dongle, with memory-mapped buffers.
type Capture struct {
	dev       *Device
	format    PixFormat
	bufs      [][]byte
	fds       []int // dmabuf exports, when asked for
	streaming bool
}

// Frame is one captured frame. Data aliases the driver's buffer until
// Release puts it back.
type Frame struct {
	Data      []byte
	Index     uint32
	Sequence  uint32
	Flags     uint32
	Timestamp time.Duration // kernel monotonic, as Now
}

// OpenCapture opens a capture node and checks it can stream.
func OpenCapture(path string) (*Capture, error) {
	dev, err := Open(path)
	if err != nil {
		return nil, err
	}
	cap, err := dev.QueryCap()
	if err != nil {
		dev.Close()
		return nil, err
	}
	if cap.Caps()&CapVideoCapture == 0 || cap.Caps()&CapStreaming == 0 {
		dev.Close()
		return nil, fmt.Errorf("%s: not a streaming capture device", path)
	}
	return &Capture{dev: dev}, nil
}

// Device is the underlying node.
func (c *Capture) Device() *Device { return c.dev }

// SetFormat asks for a size and pixel format and returns what the driver
// chose, which may differ.
func (c *Capture) SetFormat(width, height, pixfmt uint32) (PixFormat, error) {
	f := Format{Type: BufTypeVideoCapture}
	p := f.Pix()
	p.Width, p.Height, p.PixelFormat, p.Field = width, height, pixfmt, FieldNone
	if err := c.dev.SetFormat(&f); err != nil {
		return PixFormat{}, err
	}
	c.format = *f.Pix()
	return c.format, nil
}

// Format is the negotiated format.
func (c *Capture) Format() PixFormat { return c.format }

// SetFrameRate asks for fps and returns what was granted.
func (c *Capture) SetFrameRate(fps uint32) (float64, error) { return c.dev.SetFrameRate(fps) }

// Start allocates n buffers, queues them all and starts streaming.
func (c *Capture) Start(n int) error {
	count, err := c.dev.RequestBuffers(BufTypeVideoCapture, MemoryMMAP, uint32(n))
	if err != nil {
		return err
	}
	for i := uint32(0); i < count; i++ {
		b := Buffer{Index: i, Type: BufTypeVideoCapture, Memory: MemoryMMAP}
		if err := c.dev.QueryBuffer(&b, nil); err != nil {
			c.unmap()
			return err
		}
		m, err := c.dev.Mmap(b.MemOffset(), b.Length)
		if err != nil {
			c.unmap()
			return err
		}
		c.bufs = append(c.bufs, m)
		if err := c.dev.QueueBuffer(&b, nil); err != nil {
			c.unmap()
			return err
		}
	}
	if err := c.dev.StreamOn(BufTypeVideoCapture); err != nil {
		c.unmap()
		return err
	}
	c.streaming = true
	return nil
}

// Buffers is how many buffers Start allocated.
func (c *Capture) Buffers() int { return len(c.bufs) }

// BufferSize is the mapped length of each buffer.
func (c *Capture) BufferSize() int {
	if len(c.bufs) == 0 {
		return 0
	}
	return len(c.bufs[0])
}

// Export returns a dmabuf file descriptor per buffer, so another device can
// read the frames without a copy. Call after Start.
func (c *Capture) Export() ([]int, error) {
	if c.fds != nil {
		return c.fds, nil
	}
	for i := range c.bufs {
		fd, err := c.dev.ExportBuffer(BufTypeVideoCapture, uint32(i), 0)
		if err != nil {
			return nil, err
		}
		c.fds = append(c.fds, fd)
	}
	return c.fds, nil
}

// Read waits up to timeout for a frame. It returns ErrAgain on timeout.
func (c *Capture) Read(timeout time.Duration) (Frame, error) {
	ev, err := c.dev.Poll(unix.POLLIN, timeout)
	if err != nil {
		return Frame{}, err
	}
	if ev == 0 {
		return Frame{}, ErrAgain
	}
	return c.Dequeue()
}

// Dequeue takes a ready frame without waiting, or returns ErrAgain.
func (c *Capture) Dequeue() (Frame, error) {
	b := Buffer{Type: BufTypeVideoCapture, Memory: MemoryMMAP}
	if err := c.dev.DequeueBuffer(&b, nil); err != nil {
		return Frame{}, err
	}
	n := int(b.BytesUsed)
	if n > len(c.bufs[b.Index]) {
		n = len(c.bufs[b.Index])
	}
	return Frame{
		Data:      c.bufs[b.Index][:n],
		Index:     b.Index,
		Sequence:  b.Sequence,
		Flags:     b.Flags,
		Timestamp: b.Stamp(),
	}, nil
}

// Release gives a frame's buffer back to the driver.
func (c *Capture) Release(f Frame) error {
	b := Buffer{Index: f.Index, Type: BufTypeVideoCapture, Memory: MemoryMMAP}
	return c.dev.QueueBuffer(&b, nil)
}

// Stop ends streaming and frees the buffers.
func (c *Capture) Stop() error {
	var err error
	if c.streaming {
		err = c.dev.StreamOff(BufTypeVideoCapture)
		c.streaming = false
	}
	c.unmap()
	if _, e := c.dev.RequestBuffers(BufTypeVideoCapture, MemoryMMAP, 0); e != nil && err == nil {
		err = e
	}
	return err
}

// Close stops streaming and closes the node.
func (c *Capture) Close() error {
	err := c.Stop()
	if e := c.dev.Close(); e != nil && err == nil {
		err = e
	}
	return err
}

func (c *Capture) unmap() {
	for _, fd := range c.fds {
		unix.Close(fd)
	}
	c.fds = nil
	for _, b := range c.bufs {
		c.dev.Munmap(b)
	}
	c.bufs = nil
}
