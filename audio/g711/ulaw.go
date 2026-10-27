// Package g711 is the µ-law half of ITU-T G.711 (PCMU): 8 kHz narrowband
// speech at 64 kbit/s, one byte per sample, the codec every WebRTC endpoint
// must support. It is the fallback when a peer offers no G.722.
package g711

const bias = 0x84

// EncodeULaw appends one µ-law byte per 16-bit sample.
func EncodeULaw(dst []byte, samples []int16) []byte {
	for _, s := range samples {
		dst = append(dst, encodeSample(s))
	}
	return dst
}

// DecodeULaw appends one 16-bit sample per µ-law byte.
func DecodeULaw(dst []int16, stream []byte) []int16 {
	for _, b := range stream {
		dst = append(dst, ulawTable[b])
	}
	return dst
}

func encodeSample(s int16) byte {
	v := int32(s)
	sign := byte(0)
	if v < 0 {
		v = -v
		sign = 0x80
	}
	if v > 32635 {
		v = 32635
	}
	v += bias
	exp := byte(7)
	for mask := int32(0x4000); mask != 0 && v&mask == 0; mask >>= 1 {
		exp--
	}
	mant := byte((v >> (uint(exp) + 3)) & 0x0f)
	return ^(sign | (exp << 4) | mant)
}

// ulawTable decodes every byte, built once from the inverse of the rule
// above.
var ulawTable = func() (t [256]int16) {
	for i := 0; i < 256; i++ {
		b := ^byte(i)
		exp := (b >> 4) & 7
		mant := b & 0x0f
		v := ((int32(mant) << 3) + bias) << uint(exp)
		v -= bias
		if b&0x80 != 0 {
			v = -v
		}
		t[i] = int16(v)
	}
	return
}()
