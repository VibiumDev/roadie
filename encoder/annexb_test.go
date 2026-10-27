package encoder

import (
	"bytes"
	"testing"
)

func nal(t byte, body ...byte) []byte {
	return append([]byte{0, 0, 0, 1, t}, body...)
}

// A slice whose first_mb_in_slice is 0 starts with a 1 bit; one with a
// first_mb of 2 starts with 011.
var (
	sps    = nal(0x67, 0x42, 0xc0, 0x28, 0xaa)
	pps    = nal(0x68, 0xce, 0x38, 0x80)
	idr    = nal(0x65, 0x88, 0x84, 0x00)
	slice0 = nal(0x41, 0x9a, 0x02)
	slice2 = nal(0x41, 0x7a, 0x02) // 011 11010...: first_mb_in_slice = 2
	sei    = nal(0x06, 0x05, 0x01)
)

func TestAUSplitter(t *testing.T) {
	stream := bytes.Join([][]byte{sps, pps, idr, slice0, slice0, slice2, sei, slice0}, nil)
	var got [][]byte
	s := &AUSplitter{Emit: func(au []byte) { got = append(got, au) }}
	// Feed it a byte at a time, the hardest case.
	for i := range stream {
		s.Write(stream[i : i+1])
	}
	s.Flush()
	want := [][]byte{
		bytes.Join([][]byte{sps, pps, idr}, nil),
		slice0,
		bytes.Join([][]byte{slice0, slice2}, nil),
		bytes.Join([][]byte{sei, slice0}, nil),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d units, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("unit %d = % x, want % x", i, got[i], want[i])
		}
	}
	if s0, p0, i0 := NALTypes(got[0]); !s0 || !p0 || !i0 {
		t.Errorf("first unit: sps %v pps %v idr %v", s0, p0, i0)
	}
	if s1, p1, i1 := NALTypes(got[1]); s1 || p1 || i1 {
		t.Errorf("second unit: sps %v pps %v idr %v", s1, p1, i1)
	}
	gotSPS, gotPPS := ParameterSets(got[0])
	if !bytes.Equal(gotSPS, sps) || !bytes.Equal(gotPPS, pps) {
		t.Errorf("ParameterSets = % x / % x", gotSPS, gotPPS)
	}
}

func TestAUSplitterThreeByteStartCodes(t *testing.T) {
	three := func(n []byte) []byte { return n[1:] }
	stream := bytes.Join([][]byte{three(idr), three(slice0), three(slice0)}, nil)
	var got int
	s := &AUSplitter{Emit: func([]byte) { got++ }}
	s.Write(stream)
	s.Flush()
	if got != 3 {
		t.Errorf("got %d units, want 3", got)
	}
}

func TestFirstMBInSlice(t *testing.T) {
	if v := firstMBInSlice(slice0); v != 0 {
		t.Errorf("slice0: %d", v)
	}
	if v := firstMBInSlice(slice2); v != 2 {
		t.Errorf("slice2: %d", v)
	}
}
