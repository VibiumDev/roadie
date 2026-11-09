//go:build !linux

package alsa

import "errors"

func OpenPCM(card int, playback bool, want PCMParams) (PCMStream, error) {
	return nil, errors.New("USB gadget audio needs Linux")
}
