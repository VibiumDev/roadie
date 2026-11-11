// Package mp4 stores an encoder's H.264 in an MP4 file as it comes, without
// encoding it again.
package mp4

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"time"
)

// clock is the track's timescale.
const clock = 90000

// mdatHeader is the 64-bit form of the box header, so the size written at
// the end never outgrows it.
const mdatHeader = 16

// longestFrame is how long one picture may stay up. A capture that stalled
// for longer shows as this much of a still.
const longestFrame = time.Hour

// lastFrame is the most the final picture stays up: nothing follows it to
// say how long it lasted.
const lastFrame = 100 * time.Millisecond

// ErrChanged means an access unit starts another stream: a picture of
// another size or profile. A file holds one stream: finish this one and
// put the unit in the next.
var ErrChanged = errors.New("mp4: the stream changed")

type frame struct {
	size uint32
	key  bool
	at   time.Duration
}

// Writer writes one H.264 video track: the frames as they are added, the
// index once at the end.
type Writer struct {
	path     string
	f        *os.File
	w        *bufio.Writer
	frames   []frame
	sps, pps []byte
	width    int
	height   int
	bytes    int64
	head     int64
	err      error
}

// Create starts a file at path, replacing what is there.
func Create(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	m := &Writer{path: path, f: f, w: bufio.NewWriterSize(f, 256<<10)}
	ftyp := box("ftyp", []byte("isom"), u32(0x200), []byte("isomiso2avc1mp41"))
	m.head = int64(len(ftyp)) + mdatHeader
	m.w.Write(ftyp)
	m.w.Write(u32(1))
	m.w.WriteString("mdat")
	if _, m.err = m.w.Write(make([]byte, 8)); m.err != nil {
		m.Discard()
		return nil, m.err
	}
	return m, nil
}

// Add appends one access unit in Annex-B form, as an encoder emits it. at
// is when its picture was taken, on any clock that only moves forward.
//
// The file starts at the first keyframe that carries its parameter sets;
// units before it are dropped. A later keyframe whose sets describe another
// stream is not written and gives ErrChanged.
func (m *Writer) Add(au []byte, at time.Duration) error {
	if m.err != nil {
		return m.err
	}
	var sps, pps []byte
	key := false
	size := 0
	eachNAL(au, func(nal []byte) {
		switch nal[0] & 0x1f {
		case 7:
			sps = nal
		case 8:
			pps = nal
		case 9:
		default:
			if nal[0]&0x1f == 5 {
				key = true
			}
			size += 4 + len(nal)
		}
	})
	if m.sps == nil {
		if !key || sps == nil || pps == nil {
			return nil
		}
		width, height, err := spsSize(sps)
		if err != nil {
			return nil
		}
		m.sps, m.pps, m.width, m.height = bytes.Clone(sps), bytes.Clone(pps), width, height
	} else if key && sps != nil && pps != nil && !m.same(sps, pps) {
		return ErrChanged
	}
	if size == 0 {
		return nil
	}
	eachNAL(au, func(nal []byte) {
		if t := nal[0] & 0x1f; t == 7 || t == 8 || t == 9 || m.err != nil {
			return
		}
		if _, m.err = m.w.Write(u32(uint32(len(nal)))); m.err == nil {
			_, m.err = m.w.Write(nal)
		}
	})
	if m.err != nil {
		return m.err
	}
	m.bytes += int64(size)
	m.frames = append(m.frames, frame{size: uint32(size), key: key, at: at})
	return nil
}

// same says whether parameter sets describe the stream this file holds.
// An encoder writes its bitrate into the SPS and sends a new one each time
// the rate moves; the pictures decode as before.
func (m *Writer) same(sps, pps []byte) bool {
	if bytes.Equal(sps, m.sps) && bytes.Equal(pps, m.pps) {
		return true
	}
	width, height, err := spsSize(sps)
	return err == nil && width == m.width && height == m.height &&
		bytes.Equal(sps[:4], m.sps[:4]) && bytes.Equal(pps, m.pps)
}

// Bytes is how much picture data the file holds so far.
func (m *Writer) Bytes() int64 { return m.bytes }

// Info describes a finished file.
type Info struct {
	Width, Height int
	Duration      time.Duration
	Frames        int
	Bytes         int64
}

// Finish writes the index and closes the file. The last frame stays up for
// as long as the one before it did, and no longer than lastFrame.
func (m *Writer) Finish() (Info, error) {
	defer m.f.Close()
	if m.err != nil {
		return Info{}, m.err
	}
	if len(m.frames) == 0 {
		return Info{}, errors.New("mp4: no video arrived")
	}
	width, height := m.width, m.height
	durs := make([]uint32, len(m.frames))
	var total int64
	for i := range m.frames {
		d := time.Second / 30
		if i+1 < len(m.frames) {
			d = m.frames[i+1].at - m.frames[i].at
		} else if i > 0 {
			d = min(time.Duration(durs[i-1])*time.Second/clock, lastFrame)
		}
		d = min(max(d, time.Millisecond), longestFrame)
		durs[i] = uint32(int64(d) * clock / int64(time.Second))
		total += int64(durs[i])
	}
	moov := m.moov(width, height, durs, total)
	if _, err := m.w.Write(moov); err != nil {
		return Info{}, err
	}
	if err := m.w.Flush(); err != nil {
		return Info{}, err
	}
	size := make([]byte, 8)
	binary.BigEndian.PutUint64(size, uint64(mdatHeader+m.bytes))
	if _, err := m.f.WriteAt(size, m.head-8); err != nil {
		return Info{}, err
	}
	return Info{
		Width: width, Height: height, Duration: time.Duration(total) * time.Second / clock,
		Frames: len(m.frames), Bytes: m.head + m.bytes + int64(len(moov)),
	}, nil
}

// Discard drops the file.
func (m *Writer) Discard() {
	m.f.Close()
	os.Remove(m.path)
}

// eachNAL calls fn with every NAL unit of an Annex-B access unit, start
// codes and the zeros before them left out.
func eachNAL(au []byte, fn func(nal []byte)) {
	start := -1
	for i := 0; i+2 < len(au); i++ {
		if au[i] != 0 || au[i+1] != 0 || au[i+2] != 1 {
			continue
		}
		if start >= 0 {
			if nal := bytes.TrimRight(au[start:i], "\x00"); len(nal) > 0 {
				fn(nal)
			}
		}
		start = i + 3
		i += 2
	}
	if start >= 0 && start < len(au) {
		if nal := bytes.TrimRight(au[start:], "\x00"); len(nal) > 0 {
			fn(nal)
		}
	}
}

var matrix = cat(u32(0x10000), u32(0), u32(0), u32(0), u32(0x10000), u32(0), u32(0), u32(0), u32(0x40000000))

func (m *Writer) moov(width, height int, durs []uint32, total int64) []byte {
	ms := uint32(total * 1000 / clock)

	mvhd := full("mvhd", 0, 0, u32(0), u32(0), u32(1000), u32(ms),
		u32(0x10000), u16(0x100), make([]byte, 10), matrix, make([]byte, 24), u32(2))
	tkhd := full("tkhd", 0, 3, u32(0), u32(0), u32(1), u32(0), u32(ms),
		make([]byte, 8), u16(0), u16(0), u16(0), u16(0), matrix, u32(uint32(width)<<16), u32(uint32(height)<<16))
	mdhd := full("mdhd", 0, 0, u32(0), u32(0), u32(clock), u32(uint32(total)), u16(0x55c4), u16(0))
	hdlr := full("hdlr", 0, 0, u32(0), []byte("vide"), make([]byte, 12), []byte("VideoHandler\x00"))
	vmhd := full("vmhd", 0, 1, make([]byte, 8))
	dinf := box("dinf", full("dref", 0, 0, u32(1), full("url ", 0, 1)))

	avcC := box("avcC", []byte{1, m.sps[1], m.sps[2], m.sps[3], 0xff, 0xe1},
		u16(uint16(len(m.sps))), m.sps, []byte{1}, u16(uint16(len(m.pps))), m.pps)
	avc1 := box("avc1", make([]byte, 6), u16(1), make([]byte, 16),
		u16(uint16(width)), u16(uint16(height)), u32(0x480000), u32(0x480000), u32(0), u16(1),
		make([]byte, 32), u16(0x18), u16(0xffff), avcC)
	stsd := full("stsd", 0, 0, u32(1), avc1)

	var stts, stss, stsz []byte
	runs, keys := uint32(0), uint32(0)
	for i := 0; i < len(durs); {
		j := i
		for j < len(durs) && durs[j] == durs[i] {
			j++
		}
		stts = append(stts, cat(u32(uint32(j-i)), u32(durs[i]))...)
		runs++
		i = j
	}
	for i, f := range m.frames {
		if f.key {
			stss = append(stss, u32(uint32(i+1))...)
			keys++
		}
		stsz = append(stsz, u32(f.size)...)
	}
	n := uint32(len(m.frames))
	stbl := box("stbl", stsd,
		full("stts", 0, 0, u32(runs), stts),
		full("stss", 0, 0, u32(keys), stss),
		full("stsc", 0, 0, u32(1), u32(1), u32(n), u32(1)),
		full("stsz", 0, 0, u32(0), u32(n), stsz),
		full("stco", 0, 0, u32(1), u32(uint32(m.head))))

	trak := box("trak", tkhd, box("mdia", mdhd, hdlr, box("minf", vmhd, dinf, stbl)))
	return box("moov", mvhd, trak)
}

func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func box(kind string, parts ...[]byte) []byte {
	body := cat(parts...)
	return cat(u32(uint32(8+len(body))), []byte(kind), body)
}

func full(kind string, version byte, flags uint32, parts ...[]byte) []byte {
	head := u32(flags)
	head[0] = version
	return box(kind, append([][]byte{head}, parts...)...)
}
