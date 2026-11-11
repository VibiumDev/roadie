package mp4

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VibiumDev/roadie/aac"
)

// What a Roadie's encoder sends for an iPhone: 448x976 coded, cropped to
// 444x972.
var testSPS, _ = hex.DecodeString("2742c02895a0701efbb840000003004000000f382800162000005f5e6f7b80789135")

var (
	testPPS   = []byte{0x28, 0xce, 0x3c, 0x80}
	testIDR   = []byte{0x25, 0x88, 0x84, 0x21, 0xa0}
	testSlice = []byte{0x21, 0x9a, 0x02, 0x07}
	testAUD   = []byte{0x09, 0xf0}
)

// unit joins NAL units into an Annex-B access unit.
func unit(nals ...[]byte) []byte {
	var au []byte
	for i, n := range nals {
		if i%2 == 0 {
			au = append(au, 0)
		}
		au = append(append(au, 0, 0, 1), n...)
	}
	return au
}

// boxes lists the boxes of an MP4 by path, such as moov/trak/mdia.
func boxes(t *testing.T, data []byte, prefix string, out map[string][]byte) {
	t.Helper()
	for len(data) > 0 {
		if len(data) < 8 {
			t.Fatalf("%s: %d stray bytes", prefix, len(data))
		}
		size, head := int(binary.BigEndian.Uint32(data)), 8
		kind := string(data[4:8])
		if size == 1 {
			size, head = int(binary.BigEndian.Uint64(data[8:])), 16
		}
		if size < head || size > len(data) {
			t.Fatalf("%s%s: size %d of %d", prefix, kind, size, len(data))
		}
		body := data[head:size]
		out[prefix+kind] = body
		switch kind {
		case "moov", "trak", "mdia", "minf", "stbl":
			boxes(t, body, prefix+kind+"/", out)
		}
		data = data[size:]
	}
}

func TestWriterStoresTheStreamAsItComes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.mp4")
	m, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ms := time.Millisecond
	steps := []struct {
		au []byte
		at time.Duration
	}{
		{unit(testSlice), 0}, // before the first keyframe: dropped
		{unit(testAUD, testSPS, testPPS, testIDR), 100 * ms},
		{unit(testAUD, testSlice), 133 * ms},
		{unit(testSlice), 166 * ms},
		{unit(testSPS, testPPS, testIDR), 2100 * ms},
		{unit(testSlice), 2133 * ms},
	}
	for i, s := range steps {
		if err := m.Add(s.au, s.at); err != nil {
			t.Fatalf("unit %d: %v", i, err)
		}
	}
	info, err := m.Finish(nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 444 || info.Height != 972 || info.Frames != 5 {
		t.Errorf("info %+v; want 444x972 and 5 frames", info)
	}
	// 100 ms to 2133 ms, and the last frame as long as the one before it.
	if want := 2066 * ms; info.Duration < want-ms || info.Duration > want+ms {
		t.Errorf("duration %v; want about %v", info.Duration, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != info.Bytes {
		t.Errorf("the file is %d bytes, the info says %d", len(data), info.Bytes)
	}
	got := map[string][]byte{}
	boxes(t, data, "", got)
	stbl := "moov/trak/mdia/minf/stbl/"
	for _, name := range []string{"ftyp", "mdat", "moov/mvhd", stbl + "stsd", stbl + "stts", stbl + "stss", stbl + "stsz", stbl + "stco"} {
		if got[name] == nil {
			t.Fatalf("no %s box", name)
		}
	}
	if n := binary.BigEndian.Uint32(got[stbl+"stsz"][8:]); n != 5 {
		t.Errorf("stsz lists %d frames", n)
	}
	stss := got[stbl+"stss"]
	if n := binary.BigEndian.Uint32(stss[4:]); n != 2 || binary.BigEndian.Uint32(stss[8:]) != 1 || binary.BigEndian.Uint32(stss[12:]) != 4 {
		t.Errorf("stss % x; want keyframes 1 and 4", stss)
	}
	// The picture data is length-prefixed NAL units, parameter sets and
	// delimiters left to the index.
	mdat := got["mdat"]
	var kinds []byte
	for len(mdat) > 0 {
		n := int(binary.BigEndian.Uint32(mdat))
		if n == 0 || 4+n > len(mdat) {
			t.Fatalf("mdat: a unit of %d bytes with %d left", n, len(mdat)-4)
		}
		kinds = append(kinds, mdat[4]&0x1f)
		mdat = mdat[4+n:]
	}
	if string(kinds) != string([]byte{5, 1, 1, 5, 1}) {
		t.Errorf("mdat holds units of kinds %v", kinds)
	}
	if off := binary.BigEndian.Uint32(got[stbl+"stco"][8:]); string(data[off+4:off+4+uint32(len(testIDR))]) != string(testIDR) {
		t.Errorf("stco points at %d, which is not the first frame", off)
	}
}

func TestWriterRefusesAnotherStream(t *testing.T) {
	m, err := Create(filepath.Join(t.TempDir(), "v.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Add(unit(testSPS, testPPS, testIDR), 0); err != nil {
		t.Fatal(err)
	}
	// An encoder told to change its bitrate sends a new SPS that differs
	// only in what it says about the rate. It is the same stream.
	rate, _ := hex.DecodeString("2742c02895a0701efbb840000003004000000f3828000bd3500017d79bdee01e244d40")
	if w, h, err := spsSize(rate); err != nil || w != 444 || h != 972 {
		t.Fatalf("the second SPS: %dx%d, %v", w, h, err)
	}
	if err := m.Add(unit(rate, testPPS, testIDR), time.Second); err != nil {
		t.Fatalf("a keyframe at another bitrate: %v", err)
	}
	// A picture of another size is another stream.
	small, _ := hex.DecodeString("2742c01eda0507e4")
	if w, h, err := spsSize(small); err != nil || w != 320 || h != 240 {
		t.Fatalf("the small SPS: %dx%d, %v", w, h, err)
	}
	if err := m.Add(unit(small, testPPS, testIDR), 2*time.Second); !errors.Is(err, ErrChanged) {
		t.Fatalf("a keyframe of another size: %v", err)
	}
	if err := m.Add(unit(testSPS, []byte{0x28, 0xce, 0x3c, 0x81}, testIDR), 2*time.Second); !errors.Is(err, ErrChanged) {
		t.Fatalf("a keyframe with another PPS: %v", err)
	}
	if info, err := m.Finish(nil); err != nil || info.Frames != 2 || info.Width != 444 {
		t.Errorf("%+v, %v; want 2 frames of the first stream", info, err)
	}
}

func TestWriterAddsTheSound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.mp4")
	m, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		au := unit(testSlice)
		if i%30 == 0 {
			au = unit(testSPS, testPPS, testIDR)
		}
		if err := m.Add(au, time.Duration(i)*time.Second/30); err != nil {
			t.Fatal(err)
		}
	}
	// Three seconds of a tone for two seconds of video: the rest is cut.
	pcm := make([]int16, 3*aac.Rate)
	for i := range pcm {
		pcm[i] = int16(8000 * math.Sin(float64(i)*2*math.Pi*440/aac.Rate))
	}
	info, err := m.Finish(pcm)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Sound || info.Frames != 60 {
		t.Fatalf("info %+v; want sound and 60 frames", info)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != info.Bytes {
		t.Errorf("the file is %d bytes, the info says %d", len(data), info.Bytes)
	}
	got := map[string][]byte{}
	boxes(t, data, "", got)
	// The walk keeps the last track of each kind: the sound's.
	mdia := "moov/trak/mdia/"
	if string(got[mdia+"hdlr"][8:12]) != "soun" {
		t.Fatalf("the second track's handler is %q", got[mdia+"hdlr"][8:12])
	}
	mdhd := got[mdia+"mdhd"]
	rate, length := binary.BigEndian.Uint32(mdhd[12:]), binary.BigEndian.Uint32(mdhd[16:])
	if rate != aac.Rate {
		t.Errorf("the sound's timescale is %d", rate)
	}
	// Two seconds of sound, in whole frames, with the encoder's delay.
	if seconds := float64(length) / float64(rate); seconds < 2 || seconds > 2.2 {
		t.Errorf("the sound runs %.2f s; want about 2", seconds)
	}
	off := binary.BigEndian.Uint32(got[mdia+"minf/stbl/stco"][8:])
	first := binary.BigEndian.Uint32(got[mdia+"minf/stbl/stsz"][12:])
	if int(off+first) > len(data) || off < 48 {
		t.Errorf("the sound's first frame is at %d, %d bytes long, in a file of %d", off, first, len(data))
	}
}

func TestWriterWithNoVideo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.mp4")
	m, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	m.Add(unit(testSlice), 0)
	if _, err := m.Finish(nil); err == nil {
		t.Error("a file with no keyframe finished without an error")
	}
	m.Discard()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the discarded file is still there: %v", err)
	}
}

func TestSPSSize(t *testing.T) {
	if w, h, err := spsSize(testSPS); err != nil || w != 444 || h != 972 {
		t.Errorf("%dx%d, %v; want 444x972", w, h, err)
	}
	for _, bad := range [][]byte{nil, {0x67}, {0x68, 1, 2, 3, 4, 5}, {0x67, 0x42, 0xc0, 0x28, 0xff}} {
		if _, _, err := spsSize(bad); err == nil {
			t.Errorf("%x: no error", bad)
		}
	}
}
