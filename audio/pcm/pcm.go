// Package pcm converts between the capture's audio and what the speech
// codecs take: 48 kHz stereo float samples down to 16 kHz or 8 kHz mono
// 16-bit, and 8 kHz back up to 16 kHz.
package pcm

import (
	"encoding/binary"
	"math"
)

// Downsampler takes interleaved float32 samples at one rate and returns
// mono int16 samples at a lower rate. A windowed-sinc low-pass at the
// output's Nyquist frequency runs over the mono mix, and the output is read
// off it by stepping through the filtered signal, with linear interpolation
// when the ratio is not a whole number.
type Downsampler struct {
	inRate, outRate, channels int
	taps                      []float32
	hist                      []float32 // the last len(taps) mono input samples, newest last
	step, pos                 float64   // input samples per output sample; where the next output falls
	filtered                  []float32
}

const downsamplerTaps = 48

// NewDownsampler makes a converter from inRate Hz with channels interleaved
// channels to outRate Hz mono.
func NewDownsampler(inRate, outRate, channels int) *Downsampler {
	if channels < 1 {
		channels = 1
	}
	d := &Downsampler{inRate: inRate, outRate: outRate, channels: channels, step: float64(inRate) / float64(outRate)}
	if outRate < inRate {
		d.taps = lowPass(downsamplerTaps, float64(outRate)/2/float64(inRate))
		d.hist = make([]float32, len(d.taps))
	}
	return d
}

// lowPass is a Blackman-windowed sinc with the cutoff as a fraction of the
// sample rate, normalised to unit gain.
func lowPass(n int, cutoff float64) []float32 {
	h := make([]float32, n)
	var sum float64
	mid := float64(n-1) / 2
	for i := range h {
		x := float64(i) - mid
		var v float64
		if x == 0 {
			v = 2 * cutoff
		} else {
			v = math.Sin(2*math.Pi*cutoff*x) / (math.Pi * x)
		}
		w := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1)) + 0.08*math.Cos(4*math.Pi*float64(i)/float64(n-1))
		v *= w
		h[i] = float32(v)
		sum += v
	}
	for i := range h {
		h[i] = float32(float64(h[i]) / sum)
	}
	return h
}

// Process converts interleaved float32 samples, appending the mono int16
// output to dst. Whole input frames are consumed; a trailing partial frame
// is dropped.
func (d *Downsampler) Process(dst []int16, in []float32) []int16 {
	frames := len(in) / d.channels
	if frames == 0 {
		return dst
	}
	// Mono mix.
	mono := make([]float32, frames)
	if d.channels == 1 {
		copy(mono, in[:frames])
	} else {
		for i := 0; i < frames; i++ {
			var s float32
			for c := 0; c < d.channels; c++ {
				s += in[i*d.channels+c]
			}
			mono[i] = s / float32(d.channels)
		}
	}
	// Low-pass, or none when the rate does not drop. The run starts with
	// the last filtered sample of the previous call, kept in d.filtered.
	if d.taps == nil {
		d.filtered = append(d.filtered, mono...)
	} else {
		for _, x := range mono {
			copy(d.hist, d.hist[1:])
			d.hist[len(d.hist)-1] = x
			var acc float32
			for k, t := range d.taps {
				acc += t * d.hist[len(d.hist)-1-k]
			}
			d.filtered = append(d.filtered, acc)
		}
	}
	// Pick outputs between filtered samples. pos is relative to filtered[0]
	// of this call; what is left over carries into the next call.
	n := len(d.filtered)
	for d.pos < float64(n-1) {
		i := int(d.pos)
		frac := float32(d.pos - float64(i))
		v := d.filtered[i]*(1-frac) + d.filtered[i+1]*frac
		dst = append(dst, toInt16(v))
		d.pos += d.step
	}
	d.pos -= float64(n - 1)
	// Keep the last filtered sample as the start of the next call's run.
	last := d.filtered[n-1]
	d.filtered = append(d.filtered[:0], last)
	return dst
}

func toInt16(v float32) int16 {
	if v >= 1 {
		return 32767
	}
	if v <= -1 {
		return -32768
	}
	return int16(v * 32767)
}

// Upsample2 doubles the rate of mono int16 samples, for 8 kHz speech into
// a 16 kHz path, interpolating between neighbours.
type Upsample2 struct{ prev int16 }

// Process appends two output samples per input sample.
func (u *Upsample2) Process(dst []int16, in []int16) []int16 {
	for _, s := range in {
		dst = append(dst, int16((int32(u.prev)+int32(s))/2), s)
		u.prev = s
	}
	return dst
}

// Int16LE encodes samples as little-endian bytes, appending to dst.
func Int16LE(dst []byte, samples []int16) []byte {
	for _, s := range samples {
		dst = binary.LittleEndian.AppendUint16(dst, uint16(s))
	}
	return dst
}

// Float32LE decodes little-endian float32 samples.
func Float32LE(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}
