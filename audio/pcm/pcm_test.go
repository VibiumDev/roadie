package pcm

import (
	"math"
	"testing"
)

// A 1 kHz tone at 48 kHz stereo comes out at 16 kHz mono as the same tone
// at nearly full level, in the right number of samples.
func TestDownsample48to16(t *testing.T) {
	d := NewDownsampler(48000, 16000, 2)
	var out []int16
	for chunk := 0; chunk < 50; chunk++ { // 50 x 20 ms
		in := make([]float32, 960*2)
		for i := 0; i < 960; i++ {
			v := float32(0.5 * math.Sin(2*math.Pi*1000*float64(chunk*960+i)/48000))
			in[2*i], in[2*i+1] = v, v
		}
		out = d.Process(out, in)
	}
	if n := len(out); n < 320*50-2 || n > 320*50 {
		t.Fatalf("%d samples out, want about %d", n, 320*50)
	}
	// Level and frequency: count zero crossings over the last 0.5 s and the peak.
	tail := out[len(out)-8000:]
	var peak int16
	crossings := 0
	for i := 1; i < len(tail); i++ {
		if (tail[i-1] < 0) != (tail[i] < 0) {
			crossings++
		}
		if tail[i] > peak {
			peak = tail[i]
		}
	}
	if crossings < 990 || crossings > 1010 {
		t.Errorf("%d zero crossings in 0.5 s, want about 1000", crossings)
	}
	if peak < 15500 || peak > 16500 {
		t.Errorf("peak %d, want about 16384", peak)
	}
}

// A tone above the output's Nyquist is filtered out, not folded down.
func TestDownsampleRejectsHighTone(t *testing.T) {
	d := NewDownsampler(48000, 16000, 1)
	var out []int16
	for chunk := 0; chunk < 50; chunk++ {
		in := make([]float32, 960)
		for i := range in {
			in[i] = float32(0.5 * math.Sin(2*math.Pi*12000*float64(chunk*960+i)/48000))
		}
		out = d.Process(out, in)
	}
	var peak int16
	for _, s := range out[len(out)-8000:] {
		if s > peak {
			peak = s
		}
	}
	if peak > 500 {
		t.Errorf("a 12 kHz tone came through at %d", peak)
	}
}

func TestDownsample48to8AndUp(t *testing.T) {
	d := NewDownsampler(48000, 8000, 2)
	out := d.Process(nil, make([]float32, 960*2*10))
	if n := len(out); n < 160*10-2 || n > 160*10 {
		t.Errorf("%d samples at 8 kHz from 200 ms, want about 1600", n)
	}
	up := (&Upsample2{}).Process(nil, []int16{0, 100, 200})
	if len(up) != 6 || up[1] != 0 || up[2] != 50 || up[3] != 100 || up[4] != 150 {
		t.Errorf("upsampled to %v", up)
	}
}
