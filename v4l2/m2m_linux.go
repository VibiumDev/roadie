//go:build linux && (arm64 || amd64)

package v4l2

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// M2M is a memory-to-memory device: frames go in on the OUTPUT queue and
// results come back on the CAPTURE queue. The Pi 4's H.264 encoder
// (/dev/video11) and ISP (/dev/video12) are both of this kind, with
// multi-planar formats that in practice use one contiguous plane.
type M2M struct {
	dev *Device
	out m2mQueue
	cap m2mQueue
}

type m2mQueue struct {
	typ       uint32
	memory    uint32
	format    PixFormatMplane
	bufs      [][]byte       // mapped memory, MMAP only
	fds       []int          // dmabuf exports of our own buffers
	free      []uint32       // indices not held by the driver (OUTPUT queue)
	queued    map[uint32]int // OUTPUT DMABUF: index -> foreign fd queued
	streaming bool
}

// Result is one finished CAPTURE buffer. Data aliases the driver's buffer
// until Release.
type Result struct {
	Data      []byte
	Index     uint32
	Sequence  uint32
	Flags     uint32
	Timestamp time.Duration
}

// OpenM2M opens a memory-to-memory node.
func OpenM2M(path string) (*M2M, error) {
	dev, err := Open(path)
	if err != nil {
		return nil, err
	}
	cap, err := dev.QueryCap()
	if err != nil {
		dev.Close()
		return nil, err
	}
	if cap.Caps()&CapVideoM2MMplane == 0 {
		dev.Close()
		return nil, fmt.Errorf("%s: not a multi-planar memory-to-memory device", path)
	}
	return &M2M{
		dev: dev,
		out: m2mQueue{typ: BufTypeVideoOutputMplane, memory: MemoryMMAP},
		cap: m2mQueue{typ: BufTypeVideoCaptureMplane, memory: MemoryMMAP},
	}, nil
}

// Device is the underlying node.
func (m *M2M) Device() *Device { return m.dev }

func (m *M2M) setFormat(q *m2mQueue, width, height, pixfmt, sizeImage uint32) (PixFormatMplane, error) {
	f := Format{Type: q.typ}
	p := f.PixMP()
	p.Width, p.Height, p.PixelFormat, p.Field = width, height, pixfmt, FieldNone
	p.NumPlanes = 1
	p.PlaneFmt[0].SizeImage = sizeImage
	if err := m.dev.SetFormat(&f); err != nil {
		return PixFormatMplane{}, err
	}
	q.format = *f.PixMP()
	return q.format, nil
}

// SetInputFormat sets the format of frames going in and returns what the
// driver chose: its BytesPerLine and SizeImage say how to lay a frame out.
func (m *M2M) SetInputFormat(width, height, pixfmt uint32) (PixFormatMplane, error) {
	return m.setFormat(&m.out, width, height, pixfmt, 0)
}

// SetOutputFormat sets the format of results and returns what the driver
// chose. sizeImage is a hint for compressed formats (0 lets the driver pick).
func (m *M2M) SetOutputFormat(width, height, pixfmt, sizeImage uint32) (PixFormatMplane, error) {
	return m.setFormat(&m.cap, width, height, pixfmt, sizeImage)
}

// InputFormat is the negotiated input format.
func (m *M2M) InputFormat() PixFormatMplane { return m.out.format }

// OutputFormat is the negotiated output format.
func (m *M2M) OutputFormat() PixFormatMplane { return m.cap.format }

// SetInputCrop asks the device to use only a rectangle of each input frame
// and returns what it settled on.
func (m *M2M) SetInputCrop(r Rect) (Rect, error) {
	return m.dev.SetSelection(BufTypeVideoOutputMplane, SelTgtCrop, r)
}

// SetControl and GetControl reach the device's controls.
func (m *M2M) SetControl(id uint32, v int32) error { return m.dev.SetControl(id, v) }

// GetControl reads a control.
func (m *M2M) GetControl(id uint32) (int32, error) { return m.dev.GetControl(id) }

// Start allocates nIn input and nOut output buffers and starts both queues.
// With inputDMABUF, input buffers are not allocated here: QueueInputFD
// hands in dmabufs from another device instead.
func (m *M2M) Start(nIn, nOut int, inputDMABUF bool) error {
	if inputDMABUF {
		m.out.memory = MemoryDMABUF
		m.out.queued = make(map[uint32]int)
	}
	if err := m.prepare(&m.out, nIn, false); err != nil {
		return err
	}
	if err := m.prepare(&m.cap, nOut, true); err != nil {
		m.release(&m.out)
		return err
	}
	if err := m.dev.StreamOn(m.out.typ); err != nil {
		m.release(&m.out)
		m.release(&m.cap)
		return err
	}
	m.out.streaming = true
	if err := m.dev.StreamOn(m.cap.typ); err != nil {
		m.Stop()
		return err
	}
	m.cap.streaming = true
	return nil
}

func (m *M2M) prepare(q *m2mQueue, n int, queueAll bool) error {
	count, err := m.dev.RequestBuffers(q.typ, q.memory, uint32(n))
	if err != nil {
		return err
	}
	for i := uint32(0); i < count; i++ {
		if q.memory == MemoryMMAP {
			planes := make([]Plane, 1)
			b := Buffer{Index: i, Type: q.typ, Memory: q.memory}
			if err := m.dev.QueryBuffer(&b, planes); err != nil {
				return err
			}
			mem, err := m.dev.Mmap(planes[0].MemOffset(), planes[0].Length)
			if err != nil {
				return err
			}
			q.bufs = append(q.bufs, mem)
		}
		if queueAll {
			if err := m.queue(q, i, 0, 0, -1); err != nil {
				return err
			}
		} else {
			q.free = append(q.free, i)
		}
	}
	return nil
}

func (m *M2M) queue(q *m2mQueue, index uint32, bytesUsed int, ts time.Duration, fd int) error {
	planes := make([]Plane, 1)
	b := Buffer{Index: index, Type: q.typ, Memory: q.memory}
	planes[0].BytesUsed = uint32(bytesUsed)
	if q.memory == MemoryDMABUF {
		planes[0].SetFD(int32(fd))
		planes[0].Length = q.format.PlaneFmt[0].SizeImage
	}
	if ts != 0 {
		b.SetStamp(ts)
		b.Flags = BufFlagTimestampMonotonic
	}
	return m.dev.QueueBuffer(&b, planes)
}

// InputBuffer returns a free input buffer to fill, or ok false when the
// driver holds them all; ReapInput frees them as the device finishes.
func (m *M2M) InputBuffer() (index uint32, data []byte, ok bool) {
	if len(m.out.free) == 0 {
		return 0, nil, false
	}
	index = m.out.free[len(m.out.free)-1]
	m.out.free = m.out.free[:len(m.out.free)-1]
	if m.out.memory == MemoryMMAP {
		data = m.out.bufs[index]
	}
	return index, data, true
}

// QueueInput submits a filled input buffer with the frame's timestamp, which
// the device copies to the matching result.
func (m *M2M) QueueInput(index uint32, bytesUsed int, ts time.Duration) error {
	return m.queue(&m.out, index, bytesUsed, ts, -1)
}

// QueueInputFD submits a dmabuf from another device as input buffer index.
// ReapInput reports the fd again when the device is done reading it.
func (m *M2M) QueueInputFD(index uint32, fd int, bytesUsed int, ts time.Duration) error {
	if err := m.queue(&m.out, index, bytesUsed, ts, fd); err != nil {
		return err
	}
	m.out.queued[index] = fd
	return nil
}

// ReapInput takes back every input buffer the device has finished with and
// returns the dmabuf fds that were queued with them (-1 for MMAP buffers).
func (m *M2M) ReapInput() ([]int, error) {
	var fds []int
	for {
		planes := make([]Plane, 1)
		b := Buffer{Type: m.out.typ, Memory: m.out.memory}
		err := m.dev.DequeueBuffer(&b, planes)
		if err == ErrAgain {
			return fds, nil
		}
		if err != nil {
			return fds, err
		}
		m.out.free = append(m.out.free, b.Index)
		fd := -1
		if m.out.memory == MemoryDMABUF {
			fd = m.out.queued[b.Index]
			delete(m.out.queued, b.Index)
		}
		fds = append(fds, fd)
	}
}

// Dequeue takes one finished result without waiting, or returns ErrAgain.
func (m *M2M) Dequeue() (Result, error) {
	planes := make([]Plane, 1)
	b := Buffer{Type: m.cap.typ, Memory: m.cap.memory}
	if err := m.dev.DequeueBuffer(&b, planes); err != nil {
		return Result{}, err
	}
	n := int(planes[0].BytesUsed)
	if n > len(m.cap.bufs[b.Index]) {
		n = len(m.cap.bufs[b.Index])
	}
	return Result{
		Data:      m.cap.bufs[b.Index][:n],
		Index:     b.Index,
		Sequence:  b.Sequence,
		Flags:     b.Flags,
		Timestamp: b.Stamp(),
	}, nil
}

// Release returns a result's buffer to the device.
func (m *M2M) Release(r Result) error {
	return m.queue(&m.cap, r.Index, 0, 0, -1)
}

// ExportOutput returns a dmabuf per result buffer so another device can
// read results without a copy. Call after Start.
func (m *M2M) ExportOutput() ([]int, error) {
	if m.cap.fds != nil {
		return m.cap.fds, nil
	}
	for i := range m.cap.bufs {
		fd, err := m.dev.ExportBuffer(m.cap.typ, uint32(i), 0)
		if err != nil {
			return nil, err
		}
		m.cap.fds = append(m.cap.fds, fd)
	}
	return m.cap.fds, nil
}

// Poll waits up to timeout for a result (POLLIN) or a returned input buffer
// (POLLOUT).
func (m *M2M) Poll(timeout time.Duration) (int16, error) {
	return m.dev.Poll(unix.POLLIN|unix.POLLOUT, timeout)
}

// Stop ends streaming on both queues and frees the buffers.
func (m *M2M) Stop() error {
	var err error
	for _, q := range []*m2mQueue{&m.out, &m.cap} {
		if q.streaming {
			if e := m.dev.StreamOff(q.typ); e != nil && err == nil {
				err = e
			}
			q.streaming = false
		}
		m.release(q)
	}
	return err
}

func (m *M2M) release(q *m2mQueue) {
	for _, fd := range q.fds {
		unix.Close(fd)
	}
	q.fds = nil
	for _, b := range q.bufs {
		m.dev.Munmap(b)
	}
	q.bufs = nil
	q.free = nil
	q.queued = nil
	m.dev.RequestBuffers(q.typ, q.memory, 0)
}

// Close stops and closes the node.
func (m *M2M) Close() error {
	err := m.Stop()
	if e := m.dev.Close(); e != nil && err == nil {
		err = e
	}
	return err
}
