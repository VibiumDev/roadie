//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The capture dongle's audio, read straight from its ALSA card.
//
// The microphone driver Roadie used for this joined its blocks badly --
// measured on a UGREEN dongle, about 30 clicks a second where the same card
// read directly had none -- and picked its device by name, which could land
// on ALSA's null device. This reads the card that belongs to the same USB
// device as the video, with the same client as the gadget's audio.

// errNoDongleCard means no ALSA card was found for the dongle; the caller
// falls back to the microphone driver.
var errNoDongleCard = errors.New("no ALSA sound card found for the capture device")

// The formats tried, best first. USB capture dongles almost all do the first.
var dongleFormats = []struct{ rate, channels int }{
	{48000, 2}, {44100, 2}, {48000, 1}, {44100, 1}, {96000, 2}, {32000, 2}, {16000, 2}, {16000, 1},
}

// usbParent is the USB device a sysfs class device hangs off: its device
// link points at a USB interface (2-1:1.2) whose parent is the device (2-1).
func usbParent(classDev string) (string, error) {
	iface, err := filepath.EvalSymlinks(filepath.Join(classDev, "device"))
	if err != nil {
		return "", err
	}
	return filepath.Dir(iface), nil
}

// dongleSoundCard finds the dongle's sound card. With a filter (--audio) it
// matches the card's id or its USB product name; without one, the card on
// the same USB device as the video node.
func dongleSoundCard(sys, videoNode, filter string) (int, string, error) {
	var want string
	if filter == "" {
		if videoNode == "" {
			return 0, "", errNoDongleCard
		}
		p, err := usbParent(filepath.Join(sys, "class/video4linux", videoNode))
		if err != nil {
			return 0, "", errNoDongleCard
		}
		want = p
	}
	cards, _ := filepath.Glob(filepath.Join(sys, "class/sound/card[0-9]*"))
	for _, c := range cards {
		n, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(c), "card"))
		if err != nil {
			continue
		}
		id, _ := readTrim(filepath.Join(c, "id"))
		parent, err := usbParent(c)
		if err != nil {
			continue
		}
		product, _ := readTrim(filepath.Join(parent, "product"))
		name := id
		if product != "" {
			name = product
		}
		if filter != "" {
			f := strings.ToLower(filter)
			if strings.Contains(strings.ToLower(id), f) || strings.Contains(strings.ToLower(product), f) {
				return n, name, nil
			}
			continue
		}
		if parent == want {
			return n, name, nil
		}
	}
	return 0, "", errNoDongleCard
}

func openDongleCapture(card int) (pcmStream, error) {
	var last error
	for _, f := range dongleFormats {
		s, err := openPCM(card, false, pcmParams{
			rate: f.rate, channels: f.channels, periodFrames: f.rate / 100, periods: 8,
		})
		if err == nil {
			return s, nil
		}
		last = err
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			break // no point trying other formats
		}
	}
	return nil, last
}

// startDongleAudio streams the dongle's audio into ab until ctx ends,
// reopening it after an error. The returned channel closes on exit.
func startDongleAudio(ctx context.Context, dev deviceInfo, filter string, ab *AudioBroadcaster) (<-chan struct{}, string, error) {
	card, name, err := dongleSoundCard("/sys", dev.VideoNode(), filter)
	if err != nil {
		return nil, "", err
	}
	s, err := openDongleCapture(card)
	if err != nil {
		return nil, "", fmt.Errorf("%s (card %d): %w", name, card, err)
	}
	desc := fmt.Sprintf("%s (ALSA card %d, %d Hz, %d ch)", name, card, s.Params().rate, s.Params().channels)
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader := &headsetStream{ab: ab}
		for {
			err := reader.pump(ctx, s)
			s.Close()
			if ctx.Err() != nil {
				return
			}
			log.Printf("capture audio %s: %v; reopening", name, err)
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
				c, _, ferr := dongleSoundCard("/sys", dev.VideoNode(), filter)
				if ferr != nil {
					continue
				}
				if s, err = openDongleCapture(c); err == nil {
					break
				}
			}
		}
	}()
	return done, desc, nil
}
