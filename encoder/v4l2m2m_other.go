//go:build !linux || !(arm64 || amd64)

package encoder

func newV4L2M2M(Config) (Encoder, error) { return nil, ErrUnavailable }

func probeV4L2M2M(string) error { return ErrUnavailable }
