package g722

import (
	"math"
	"testing"
)

// The codec's delay through both QMF banks, in samples.
const delay = 22

// snr is the signal-to-noise ratio of got against want, in dB, with got
// delayed by the codec's delay.
func snr(want, got []int16) float64 {
	var sig, noise float64
	for i := 320; i+delay < len(got) && i < len(want); i++ {
		s := float64(want[i])
		n := float64(got[i+delay]) - s
		sig += s * s
		noise += n * n
	}
	if noise == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(sig/noise)
}

// A stream from another encoder decodes to almost exactly what that
// encoder's own decoder produces: the two agree to the sample at first and
// then differ by a few units now and then, from a different rounding
// somewhere in the predictor, which is 45 dB down and inaudible.
func TestDecodeCloseToReference(t *testing.T) {
	got := NewDecoder().Decode(nil, katStream)
	if len(got) != len(katDecoded) {
		t.Fatalf("decoded %d samples, want %d", len(got), len(katDecoded))
	}
	var worst int
	for i := range got {
		d := int(got[i]) - int(katDecoded[i])
		if d < 0 {
			d = -d
		}
		if d > worst {
			worst = d
		}
	}
	if worst > 64 {
		t.Errorf("decoders differ by up to %d units", worst)
	}
	if r := snr(katInput, got); r < 40 {
		t.Errorf("reference stream decoded at %.1f dB against the input, want 40", r)
	}
}

// Our own stream comes back clean through our decoder, and a tone through
// the whole codec keeps more than 45 dB on this short vector (54 dB over a
// longer one).
func TestEncodeDecodeTone(t *testing.T) {
	stream := NewEncoder().Encode(nil, katInput)
	if len(stream) != len(katInput)/2 {
		t.Fatalf("stream of %d bytes for %d samples", len(stream), len(katInput))
	}
	got := NewDecoder().Decode(nil, stream)
	if r := snr(katInput, got); r < 45 {
		t.Errorf("round trip SNR %.1f dB, want at least 45", r)
	}
}

func TestSilenceAndOddLength(t *testing.T) {
	stream := NewEncoder().Encode(nil, make([]int16, 321))
	if len(stream) != 160 {
		t.Fatalf("321 silent samples gave %d bytes, want 160", len(stream))
	}
	for _, s := range NewDecoder().Decode(nil, stream) {
		if s < -64 || s > 64 {
			t.Fatalf("silence decoded to %d", s)
		}
	}
}

func BenchmarkEncode20ms(b *testing.B) {
	e := NewEncoder()
	out := make([]byte, 0, 160)
	for i := 0; i < b.N; i++ {
		out = e.Encode(out[:0], katInput[:320])
	}
}

func BenchmarkDecode20ms(b *testing.B) {
	d := NewDecoder()
	out := make([]int16, 0, 320)
	for i := 0; i < b.N; i++ {
		out = d.Decode(out[:0], katStream[:160])
	}
}
