package g711

import "testing"

func TestULawRoundTrip(t *testing.T) {
	for _, v := range []int16{0, 1, -1, 100, -100, 1000, -1000, 8000, -8000, 32767, -32768} {
		got := DecodeULaw(nil, EncodeULaw(nil, []int16{v}))[0]
		// µ-law keeps about 1 part in 32 of the magnitude.
		limit := int32(v) / 16
		if limit < 0 {
			limit = -limit
		}
		if limit < 8 {
			limit = 8
		}
		if d := int32(got) - int32(v); d > limit || d < -limit {
			t.Errorf("%d came back as %d", v, got)
		}
	}
}

func TestULawKnownValues(t *testing.T) {
	// Silence is 0xFF, full scale positive 0x80 and negative 0x00.
	for _, c := range []struct {
		in   int16
		want byte
	}{{0, 0xff}, {32767, 0x80}, {-32768, 0x00}, {-1, 0x7f}} {
		if got := EncodeULaw(nil, []int16{c.in})[0]; got != c.want {
			t.Errorf("%d encodes to %#x, want %#x", c.in, got, c.want)
		}
	}
}
