//go:build !linux

package alsa

import (
	"context"
	"errors"
	"github.com/VibiumDev/roadie/internal/audio"
	"github.com/VibiumDev/roadie/internal/devices"
)

var ErrNoDongleCard = errors.New("no ALSA on this platform")

// startDongleAudio is Linux-only; elsewhere the microphone driver is used.
func StartDongleAudio(ctx context.Context, dev devices.DeviceInfo, filter string, ab *audio.AudioBroadcaster) (<-chan struct{}, string, error) {
	return nil, "", ErrNoDongleCard
}
