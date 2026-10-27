// Package g722 is an ITU-T G.722 codec in pure Go: 16 kHz wideband speech
// at 64 kbit/s, the codec every browser offers for WebRTC. A frame of 20 ms
// is 320 samples in and 160 bytes out; the RTP clock rate is 8000 despite
// the 16 kHz sampling, as RFC 3551 has it.
//
// The algorithm follows the recommendation: a quadrature-mirror filter
// splits the signal into two sub-bands, each coded by adaptive differential
// PCM with a 6-bit (low band) or 2-bit (high band) quantizer, an adaptive
// scale factor and a pole-zero predictor. The arithmetic is the standard's
// 16-bit fixed point, so a stream decodes bit-exactly to what any other
// conforming decoder produces.
package g722

// band is the ADPCM state of one sub-band.
type band struct {
	s, sp, sz int32
	r         [3]int32
	a, ap     [3]int32
	p         [3]int32
	d         [7]int32
	b, bp     [7]int32
	sg        [7]int32
	nb        int32
	det       int32
}

var qmfCoeffs = [12]int32{3, -11, 12, 32, -210, 951, 3876, -805, 362, -156, 53, -11}

var (
	q6   = [32]int32{0, 35, 72, 110, 150, 190, 233, 276, 323, 370, 422, 473, 530, 587, 650, 714, 786, 858, 940, 1023, 1121, 1219, 1339, 1458, 1612, 1765, 1980, 2195, 2557, 2919, 0, 0}
	iln  = [32]int32{0, 63, 62, 31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 0}
	ilp  = [32]int32{0, 61, 60, 59, 58, 57, 56, 55, 54, 53, 52, 51, 50, 49, 48, 47, 46, 45, 44, 43, 42, 41, 40, 39, 38, 37, 36, 35, 34, 33, 32, 0}
	wl   = [8]int32{-60, -30, 58, 172, 334, 538, 1198, 3042}
	rl42 = [16]int32{0, 7, 6, 5, 4, 3, 2, 1, 7, 6, 5, 4, 3, 2, 1, 0}
	ilb  = [32]int32{2048, 2093, 2139, 2186, 2234, 2283, 2333, 2385, 2437, 2491, 2546, 2602, 2659, 2718, 2778, 2839, 2902, 2966, 3031, 3098, 3166, 3236, 3308, 3381, 3456, 3532, 3610, 3690, 3771, 3854, 3939, 4026}
	qm4  = [16]int32{0, -20456, -12896, -8968, -6288, -4240, -2584, -1200, 20456, 12896, 8968, 6288, 4240, 2584, 1200, 0}
	qm6  = [64]int32{-136, -136, -136, -136, -24808, -21904, -19008, -16704, -14984, -13512, -12280, -11192, -10232, -9360, -8576, -7856, -7192, -6576, -6000, -5456, -4944, -4464, -4008, -3576, -3168, -2776, -2400, -2032, -1688, -1360, -1040, -728, 24808, 21904, 19008, 16704, 14984, 13512, 12280, 11192, 10232, 9360, 8576, 7856, 7192, 6576, 6000, 5456, 4944, 4464, 4008, 3576, 3168, 2776, 2400, 2032, 1688, 1360, 1040, 728, 432, 136, -432, -136}
	ihn  = [3]int32{0, 1, 0}
	ihp  = [3]int32{0, 3, 2}
	wh   = [3]int32{0, -214, 798}
	rh2  = [4]int32{2, 1, 2, 1}
	qm2  = [4]int32{-7408, -1616, 7408, 1616}
)

func saturate(v int32) int32 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return v
}

// block4 is the predictor update shared by both bands and both directions
// (the recommendation's block 4): it reconstructs the signal, adapts the
// pole and zero coefficients, and forms the next prediction.
func (b *band) block4(d int32) {
	b.d[0] = d
	b.r[0] = saturate(b.s + d)
	b.p[0] = saturate(b.sz + d)

	// Second pole coefficient.
	for i := 0; i < 3; i++ {
		b.sg[i] = b.p[i] >> 15
	}
	wd1 := saturate(b.a[1] << 2)
	wd2 := wd1
	if b.sg[0] == b.sg[1] {
		wd2 = -wd1
	}
	if wd2 > 32767 {
		wd2 = 32767
	}
	wd3 := int32(-128)
	if b.sg[0] == b.sg[2] {
		wd3 = 128
	}
	wd3 += wd2 >> 7
	wd3 += (b.a[2] * 32512) >> 15
	if wd3 > 12288 {
		wd3 = 12288
	} else if wd3 < -12288 {
		wd3 = -12288
	}
	b.ap[2] = wd3

	// First pole coefficient.
	b.sg[0] = b.p[0] >> 15
	b.sg[1] = b.p[1] >> 15
	wd1 = -192
	if b.sg[0] == b.sg[1] {
		wd1 = 192
	}
	wd2 = (b.a[1] * 32640) >> 15
	b.ap[1] = saturate(wd1 + wd2)
	wd3 = saturate(15360 - b.ap[2])
	if b.ap[1] > wd3 {
		b.ap[1] = wd3
	} else if b.ap[1] < -wd3 {
		b.ap[1] = -wd3
	}

	// Zero coefficients.
	wd1 = 128
	if d == 0 {
		wd1 = 0
	}
	b.sg[0] = d >> 15
	for i := 1; i < 7; i++ {
		b.sg[i] = b.d[i] >> 15
		wd2 = -wd1
		if b.sg[i] == b.sg[0] {
			wd2 = wd1
		}
		wd3 = (b.b[i] * 32640) >> 15
		b.bp[i] = saturate(wd2 + wd3)
	}

	// Delays.
	for i := 6; i > 0; i-- {
		b.d[i] = b.d[i-1]
		b.b[i] = b.bp[i]
	}
	for i := 2; i > 0; i-- {
		b.r[i] = b.r[i-1]
		b.p[i] = b.p[i-1]
		b.a[i] = b.ap[i]
	}

	// Pole and zero predictions, then their sum.
	wd1 = saturate(b.r[1] + b.r[1])
	wd1 = (b.a[1] * wd1) >> 15
	wd2 = saturate(b.r[2] + b.r[2])
	wd2 = (b.a[2] * wd2) >> 15
	b.sp = saturate(wd1 + wd2)
	b.sz = 0
	for i := 6; i > 0; i-- {
		wd1 = saturate(b.d[i] + b.d[i])
		b.sz += (b.b[i] * wd1) >> 15
	}
	b.sz = saturate(b.sz)
	b.s = saturate(b.sp + b.sz)
}

// scale adapts the band's logarithmic scale factor and quantizer step.
func (b *band) scale(w, limit, shift int32) {
	wd := (b.nb * 127) >> 7
	b.nb = wd + w
	if b.nb < 0 {
		b.nb = 0
	} else if b.nb > limit {
		b.nb = limit
	}
	wd1 := (b.nb >> 6) & 31
	wd2 := shift - (b.nb >> 11)
	var wd3 int32
	if wd2 < 0 {
		wd3 = ilb[wd1] << -wd2
	} else {
		wd3 = ilb[wd1] >> wd2
	}
	b.det = wd3 << 2
}

// Encoder turns 16 kHz samples into G.722 at 64 kbit/s.
type Encoder struct {
	low, high band
	x         [24]int32
}

// NewEncoder returns an encoder in its initial state.
func NewEncoder() *Encoder {
	e := &Encoder{}
	e.low.det = 32
	e.high.det = 8
	return e
}

// Encode codes samples, two per output byte, appending to dst. An odd last
// sample is dropped.
func (e *Encoder) Encode(dst []byte, samples []int16) []byte {
	for j := 0; j+1 < len(samples); j += 2 {
		// Transmit QMF: split the pair into a low and a high band sample.
		copy(e.x[:22], e.x[2:])
		e.x[22] = int32(samples[j])
		e.x[23] = int32(samples[j+1])
		var sumEven, sumOdd int32
		for i := 0; i < 12; i++ {
			sumOdd += e.x[2*i] * qmfCoeffs[i]
			sumEven += e.x[2*i+1] * qmfCoeffs[11-i]
		}
		xlow := (sumEven + sumOdd) >> 14
		xhigh := (sumEven - sumOdd) >> 14

		// Low band: 6-bit quantizer.
		el := saturate(xlow - e.low.s)
		wd := el
		if el < 0 {
			wd = -(el + 1)
		}
		i := 1
		for ; i < 30; i++ {
			if wd < (q6[i]*e.low.det)>>12 {
				break
			}
		}
		var ilow int32
		if el < 0 {
			ilow = iln[i]
		} else {
			ilow = ilp[i]
		}
		ril := ilow >> 2
		dlow := (e.low.det * qm4[ril]) >> 15
		e.low.scale(wl[rl42[ril]], 18432, 8)
		e.low.block4(dlow)

		// High band: 2-bit quantizer.
		eh := saturate(xhigh - e.high.s)
		wd = eh
		if eh < 0 {
			wd = -(eh + 1)
		}
		mih := int32(1)
		if wd >= (564*e.high.det)>>12 {
			mih = 2
		}
		var ihigh int32
		if eh < 0 {
			ihigh = ihn[mih]
		} else {
			ihigh = ihp[mih]
		}
		dhigh := (e.high.det * qm2[ihigh]) >> 15
		e.high.scale(wh[rh2[ihigh]], 22528, 10)
		e.high.block4(dhigh)

		dst = append(dst, byte((ihigh<<6)|ilow))
	}
	return dst
}

// Decoder turns G.722 at 64 kbit/s back into 16 kHz samples.
type Decoder struct {
	low, high band
	x         [24]int32
}

// NewDecoder returns a decoder in its initial state.
func NewDecoder() *Decoder {
	d := &Decoder{}
	d.low.det = 32
	d.high.det = 8
	return d
}

// Decode decodes a stream, two samples per byte, appending to dst.
func (d *Decoder) Decode(dst []int16, stream []byte) []int16 {
	for _, code := range stream {
		ilow := int32(code & 0x3f)
		ihigh := int32(code>>6) & 3

		// Low band.
		wd2 := (d.low.det * qm6[ilow]) >> 15
		rlow := saturate(d.low.s + wd2)
		if rlow > 16383 {
			rlow = 16383
		} else if rlow < -16384 {
			rlow = -16384
		}
		ril := ilow >> 2
		dlow := (d.low.det * qm4[ril]) >> 15
		d.low.scale(wl[rl42[ril]], 18432, 8)
		d.low.block4(dlow)

		// High band.
		dhigh := (d.high.det * qm2[ihigh]) >> 15
		rhigh := saturate(d.high.s + dhigh)
		if rhigh > 16383 {
			rhigh = 16383
		} else if rhigh < -16384 {
			rhigh = -16384
		}
		d.high.scale(wh[rh2[ihigh]], 22528, 10)
		d.high.block4(dhigh)

		// Receive QMF: the two bands back into a pair of samples.
		copy(d.x[:22], d.x[2:])
		d.x[22] = rlow + rhigh
		d.x[23] = rlow - rhigh
		var xout1, xout2 int32
		for i := 0; i < 12; i++ {
			xout2 += d.x[2*i] * qmfCoeffs[i]
			xout1 += d.x[2*i+1] * qmfCoeffs[11-i]
		}
		dst = append(dst, int16(saturate(xout1>>11)), int16(saturate(xout2>>11)))
	}
	return dst
}
