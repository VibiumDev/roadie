//go:build !linux

package main

import (
	"context"
	"errors"
	"github.com/VibiumDev/roadie/internal/devices"
)

var errNoDongleCard = errors.New("no ALSA on this platform")

// startDongleAudio is Linux-only; elsewhere the microphone driver is used.
func startDongleAudio(ctx context.Context, dev devices.DeviceInfo, filter string, ab *AudioBroadcaster) (<-chan struct{}, string, error) {
	return nil, "", errNoDongleCard
}
