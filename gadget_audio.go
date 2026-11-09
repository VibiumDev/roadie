package main

import (
	"context"
	"fmt"
	"github.com/VibiumDev/roadie/internal/audio"
	"github.com/VibiumDev/roadie/internal/devices"
	"log"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// USB audio for the linux-gadget backend. The gadget's UAC2 (or UAC1)
// function gives this machine an ALSA card. Its playback side is what the
// phone hears as a USB microphone; its capture side is what the phone plays
// to its USB speaker. Neither stream needs the gadget rebuilt to restart.

// gadgetAudioConfig is what the gadget's audio function offers.
type gadgetAudioConfig struct {
	function             string // uac2.audio or uac1.audio
	cardID               string // ALSA card id: UAC2Gadget or UAC1Gadget
	micRate, micChannels int    // gadget playback = phone's microphone
	spkRate, spkChannels int    // gadget capture = phone's speaker
}

func (c gadgetAudioConfig) hasMic() bool     { return c.micChannels > 0 }
func (c gadgetAudioConfig) hasSpeaker() bool { return c.spkChannels > 0 }

// readGadgetAudioConfig reads the audio function's settings from configfs.
// They are authoritative: the ALSA card only accepts exactly these.
func readGadgetAudioConfig(paths gadgetPaths) (gadgetAudioConfig, error) {
	for _, fn := range []struct{ name, card string }{
		{"uac2.audio", "UAC2Gadget"}, {"uac1.audio", "UAC1Gadget"},
	} {
		dir := paths.function(fn.name)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		num := func(attr string) int {
			s, _ := devices.ReadTrim(filepath.Join(dir, attr))
			n, _ := strconv.Atoi(s)
			return n
		}
		return gadgetAudioConfig{
			function:    fn.name,
			cardID:      fn.card,
			micRate:     num("p_srate"),
			micChannels: bits.OnesCount(uint(num("p_chmask"))),
			spkRate:     num("c_srate"),
			spkChannels: bits.OnesCount(uint(num("c_chmask"))),
		}, nil
	}
	return gadgetAudioConfig{}, fmt.Errorf("USB gadget %q has no audio function", gadgetName)
}

// alsaCardIndex finds a card's number from its id. Card numbers depend on
// what else is plugged in; ids do not.
func alsaCardIndex(procAsound, id string) (int, error) {
	target, err := os.Readlink(filepath.Join(procAsound, id))
	if err != nil {
		return 0, fmt.Errorf("no ALSA card %q: %w", id, err)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(target), "card"))
	if err != nil {
		return 0, fmt.Errorf("ALSA card %q links to %q", id, target)
	}
	return n, nil
}

// GadgetAudio runs the gadget microphone and speaker streams.
type GadgetAudio struct {
	cfg        gadgetAudioConfig
	procAsound string
	open       func(card int, playback bool, want pcmParams) (pcmStream, error)

	Mic     *micStream     // nil without a microphone
	headset *headsetStream // nil without a speaker

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewGadgetAudio prepares the streams the gadget's audio function has. ab
// receives the phone's speaker output; it may be nil to leave it unused.
func NewGadgetAudio(cfg gadgetAudioConfig, ab *audio.AudioBroadcaster) *GadgetAudio {
	return newGadgetAudio(cfg, ab, "/proc/asound", openPCM)
}

func newGadgetAudio(cfg gadgetAudioConfig, ab *audio.AudioBroadcaster, procAsound string,
	open func(int, bool, pcmParams) (pcmStream, error)) *GadgetAudio {
	ctx, cancel := context.WithCancel(context.Background())
	a := &GadgetAudio{cfg: cfg, procAsound: procAsound, open: open, ctx: ctx, cancel: cancel}
	if cfg.hasMic() {
		a.Mic = newMicStream(cfg.micRate, cfg.micChannels)
	}
	if cfg.hasSpeaker() && ab != nil {
		a.headset = &headsetStream{ab: ab}
	}
	return a
}

// Start runs the streams until Shutdown.
func (a *GadgetAudio) Start() {
	if a.Mic != nil {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.keepOpen("microphone", true, a.cfg.micRate, a.cfg.micChannels, micPeriods, a.Mic.pump)
		}()
	}
	if a.headset != nil {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.keepOpen("speaker", false, a.cfg.spkRate, a.cfg.spkChannels, headsetPeriod, a.headset.pump)
		}()
	}
}

func (a *GadgetAudio) Shutdown() {
	a.cancel()
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		log.Printf("USB audio: streams did not stop in time")
	}
}

// keepOpen opens a stream and runs pump on it, reopening with backoff
// whenever the device goes away -- the gadget rebuilt, the card renumbered
// -- until shutdown. Each distinct failure is logged once.
func (a *GadgetAudio) keepOpen(what string, playback bool, rate, channels, periods int,
	pump func(context.Context, pcmStream) error) {
	delay := pcmRetryMin
	lastErr := ""
	for a.ctx.Err() == nil {
		card, err := alsaCardIndex(a.procAsound, a.cfg.cardID)
		var s pcmStream
		if err == nil {
			s, err = a.open(card, playback, pcmParams{
				rate:         rate,
				channels:     channels,
				periodFrames: int(int64(rate) * int64(pcmPeriod) / int64(time.Second)),
				periods:      periods,
			})
		}
		if err != nil {
			if msg := err.Error(); msg != lastErr {
				log.Printf("USB audio %s: %v (retrying)", what, err)
				lastErr = msg
			}
			select {
			case <-a.ctx.Done():
				return
			case <-time.After(delay):
			}
			delay = min(delay*2, pcmRetryMax)
			continue
		}
		p := s.Params()
		log.Printf("USB audio %s: open on card %s, %d Hz, %d channel(s), %d x %d frames",
			what, a.cfg.cardID, p.rate, p.channels, p.periods, p.periodFrames)
		lastErr = ""
		opened := time.Now()
		err = pump(a.ctx, s)
		s.Close()
		if a.ctx.Err() != nil {
			return
		}
		log.Printf("USB audio %s: %v; reopening", what, err)
		// A stream that ran a while gets an immediate retry; one that keeps
		// failing as soon as it opens backs off instead of spinning.
		if time.Since(opened) > pcmStableAfter {
			delay = pcmRetryMin
			continue
		}
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, pcmRetryMax)
	}
}
