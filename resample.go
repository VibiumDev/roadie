package main

import "math"

// upsampler turns the browser's 16 kHz mono microphone audio into the gadget
// microphone's rate and channel count. An integer ratio (16 kHz to 48 kHz is
// 3) uses a polyphase windowed-sinc filter, which keeps speech clean instead
// of folding images back into it; other ratios fall back to linear
// interpolation.
type upsampler struct {
	inRate, outRate, channels int

	// polyphase, integer ratio
	factor int
	phases [][]float32 // phases[p][k] = h[p + k*factor]
	hist   []float32   // newest input first

	// linear, any ratio
	step float64 // input samples per output sample
	pos  float64 // position of the next output between prev and the next input
	prev float32
}

const upsamplerTapsPerPhase = 16

func newUpsampler(inRate, outRate, channels int) *upsampler {
	u := &upsampler{inRate: inRate, outRate: outRate, channels: max(channels, 1)}
	if outRate >= inRate && outRate%inRate == 0 {
		u.factor = outRate / inRate
		if u.factor > 1 {
			u.phases = designPolyphase(u.factor, upsamplerTapsPerPhase)
			u.hist = make([]float32, upsamplerTapsPerPhase)
		}
		return u
	}
	u.step = float64(inRate) / float64(outRate)
	return u
}

// designPolyphase builds a Blackman-windowed sinc low-pass for an L-fold
// upsampler, cut off just below the input's Nyquist frequency, with a gain
// of L to make up for the zeros stuffed between input samples.
func designPolyphase(l, k int) [][]float32 {
	n := l * k
	fc := 0.45 / float64(l) // cycles per output sample
	h := make([]float64, n)
	center := float64(n-1) / 2
	sum := 0.0
	for i := range h {
		t := float64(i) - center
		s := 2 * fc
		if t != 0 {
			s = math.Sin(2*math.Pi*fc*t) / (math.Pi * t)
		}
		w := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1)) +
			0.08*math.Cos(4*math.Pi*float64(i)/float64(n-1))
		h[i] = s * w
		sum += h[i]
	}
	phases := make([][]float32, l)
	for p := range phases {
		phases[p] = make([]float32, k)
		for j := 0; j < k; j++ {
			phases[p][j] = float32(h[p+j*l] * float64(l) / sum)
		}
	}
	return phases
}

func clamp16(v float32) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(math.Round(float64(v)))
}

// process converts a block of input samples and returns interleaved output.
// State carries over, so consecutive blocks join without a click.
func (u *upsampler) process(in []int16) []int16 {
	var mono []int16
	switch {
	case u.factor == 1:
		mono = in
	case u.factor > 1:
		mono = make([]int16, 0, len(in)*u.factor)
		for _, s := range in {
			copy(u.hist[1:], u.hist[:len(u.hist)-1])
			u.hist[0] = float32(s)
			for _, taps := range u.phases {
				var acc float32
				for j, c := range taps {
					acc += c * u.hist[j]
				}
				mono = append(mono, clamp16(acc))
			}
		}
	default:
		mono = make([]int16, 0, int(float64(len(in))/u.step)+2)
		for _, s := range in {
			cur := float32(s)
			for u.pos < 1 {
				mono = append(mono, clamp16(u.prev+(cur-u.prev)*float32(u.pos)))
				u.pos += u.step
			}
			u.pos--
			u.prev = cur
		}
	}
	if u.channels == 1 {
		return mono
	}
	out := make([]int16, len(mono)*u.channels)
	for i, s := range mono {
		for c := 0; c < u.channels; c++ {
			out[i*u.channels+c] = s
		}
	}
	return out
}
