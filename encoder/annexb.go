package encoder

import "bytes"

// NAL unit types.
const (
	nalSlice    = 1
	nalIDR      = 5
	nalSEI      = 6
	nalSPS      = 7
	nalPPS      = 8
	nalAUD      = 9
	nalEndSeq   = 10
	nalEndStrm  = 11
	nalFiller   = 12
	nalSPSExt   = 13
	nalPrefix   = 14
	nalSubsetSP = 15
)

var startCode = []byte{0, 0, 1}

// AUSplitter cuts an Annex-B byte stream into access units, one picture
// each, as it arrives in arbitrary chunks. Each unit is emitted with its
// leading non-picture NALs (SPS, PPS, SEI) attached.
type AUSplitter struct {
	Emit func(au []byte)

	buf    []byte // the pending unit, starting at a start code
	ends   []int  // where each complete NAL in buf ends
	hasVCL bool   // the pending unit holds a slice already
}

// Write feeds bytes. Complete units are passed to Emit as they are found;
// the last unit waits for the next one to begin, or for Flush.
func (s *AUSplitter) Write(p []byte) {
	s.buf = append(s.buf, p...)
	for {
		if len(s.ends) == 0 {
			i := bytes.Index(s.buf, startCode)
			if i < 0 {
				// Keep what could be the start of a start code.
				if len(s.buf) > 3 {
					s.buf = append(s.buf[:0], s.buf[len(s.buf)-3:]...)
				}
				return
			}
			if i > 0 && s.buf[i-1] == 0 {
				i-- // a four-byte start code
			}
			if i > 0 {
				s.buf = append(s.buf[:0], s.buf[i:]...)
			}
		}
		// The NAL after the last complete one runs to the next start code.
		start := 0
		if n := len(s.ends); n > 0 {
			start = s.ends[n-1]
		}
		next := indexStartCode(s.buf, start+3)
		if next < 0 {
			return
		}
		if s.hasVCL && s.beginsUnit(s.buf[start:next]) {
			unit := make([]byte, start)
			copy(unit, s.buf[:start])
			if s.Emit != nil {
				s.Emit(unit)
			}
			s.buf = append(s.buf[:0], s.buf[start:]...)
			next -= start
			start = 0
			s.ends = s.ends[:0]
			s.hasVCL = false
		}
		s.ends = append(s.ends, next)
		s.noteNAL(s.buf[start:next])
	}
}

// Flush emits what is pending, for the end of a stream: the last NAL is
// complete now, so it may begin a unit of its own.
func (s *AUSplitter) Flush() {
	start := 0
	if n := len(s.ends); n > 0 {
		start = s.ends[n-1]
	}
	if start > 0 && len(s.buf) > start+3 && s.hasVCL && s.beginsUnit(s.buf[start:]) {
		unit := make([]byte, start)
		copy(unit, s.buf[:start])
		if s.Emit != nil {
			s.Emit(unit)
		}
		s.buf = append(s.buf[:0], s.buf[start:]...)
	}
	if len(s.buf) > 3 && s.Emit != nil {
		unit := make([]byte, len(s.buf))
		copy(unit, s.buf)
		s.Emit(unit)
	}
	s.buf = s.buf[:0]
	s.ends = s.ends[:0]
	s.hasVCL = false
}

func (s *AUSplitter) noteNAL(nal []byte) {
	if t := nalType(nal); t >= nalSlice && t <= nalIDR {
		s.hasVCL = true
	}
}

// beginsUnit says whether a NAL starts a new access unit, given what the
// pending unit holds: after a picture's slices, any header NAL or a slice
// whose first_mb_in_slice is 0 belongs to the next picture (H.264 7.4.1.2.3).
func (s *AUSplitter) beginsUnit(nal []byte) bool {
	if !s.hasVCL {
		return false
	}
	switch t := nalType(nal); t {
	case nalAUD, nalSPS, nalPPS, nalSEI, nalPrefix, nalSubsetSP, 16, 17, 18:
		return true
	case nalSlice, nalIDR, 2, 3, 4:
		return firstMBInSlice(nal) == 0
	}
	return false
}

// indexStartCode finds the next 00 00 01 at or after from, pointing at the
// 00 of a 4-byte start code when there is one.
func indexStartCode(b []byte, from int) int {
	if from > len(b) {
		return -1
	}
	i := bytes.Index(b[from:], startCode)
	if i < 0 {
		return -1
	}
	i += from
	if i > 0 && b[i-1] == 0 && i-1 >= from {
		return i - 1
	}
	return i
}

// nalType is the type of a NAL that starts with its start code.
func nalType(nal []byte) int {
	h := headerIndex(nal)
	if h < 0 {
		return -1
	}
	return int(nal[h] & 0x1f)
}

func headerIndex(nal []byte) int {
	i := bytes.Index(nal, startCode)
	if i < 0 || i+3 >= len(nal) {
		return -1
	}
	return i + 3
}

// firstMBInSlice reads the slice header's first field, an unsigned
// Exp-Golomb code. A zero means the slice begins a picture.
func firstMBInSlice(nal []byte) int {
	h := headerIndex(nal)
	if h < 0 || h+1 >= len(nal) {
		return -1
	}
	r := bitReader{b: nal[h+1:]}
	return r.ue()
}

type bitReader struct {
	b   []byte
	pos int
}

func (r *bitReader) bit() int {
	if r.pos/8 >= len(r.b) {
		return 0
	}
	v := int(r.b[r.pos/8]>>(7-uint(r.pos%8))) & 1
	r.pos++
	return v
}

func (r *bitReader) ue() int {
	zeros := 0
	for r.bit() == 0 && zeros < 32 {
		zeros++
	}
	v := 1
	for i := 0; i < zeros; i++ {
		v = v<<1 | r.bit()
	}
	return v - 1
}

// NALTypes reports which NAL kinds an access unit holds.
func NALTypes(au []byte) (sps, pps, idr bool) {
	for i := 0; i+3 < len(au); i++ {
		if au[i] == 0 && au[i+1] == 0 && au[i+2] == 1 {
			switch au[i+3] & 0x1f {
			case nalSPS:
				sps = true
			case nalPPS:
				pps = true
			case nalIDR:
				idr = true
			}
			i += 3
		}
	}
	return
}

// ParameterSets returns the SPS and PPS NALs in an access unit, start codes
// included, or nil for each one missing.
func ParameterSets(au []byte) (sps, pps []byte) {
	i := 0
	for {
		start := indexStartCode(au, i)
		if start < 0 {
			return
		}
		end := indexStartCode(au, start+3)
		if end < 0 {
			end = len(au)
		}
		nal := au[start:end]
		switch nalType(nal) {
		case nalSPS:
			sps = nal
		case nalPPS:
			pps = nal
		}
		if end == len(au) {
			return
		}
		i = end
	}
}
