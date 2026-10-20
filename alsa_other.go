//go:build !linux

package main

import "errors"

func openPCM(card int, playback bool, want pcmParams) (pcmStream, error) {
	return nil, errors.New("USB gadget audio needs Linux")
}
