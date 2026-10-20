package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
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

var (
	errPCMAgain     = errors.New("pcm: not ready")
	errPCMXrun      = errors.New("pcm: underrun/overrun")
	errPCMSuspended = errors.New("pcm: suspended")
	errPCMGone      = errors.New("pcm: device gone")
)

// pcmParams is a stream's format and buffering.
type pcmParams struct {
	rate, channels        int
	periodFrames, periods int
}

// pcmStream is an open, non-blocking S16_LE interleaved PCM stream.
type pcmStream interface {
	Params() pcmParams
	// Wait returns true once a period can be transferred, false on timeout.
	Wait(timeout time.Duration) (bool, error)
	WriteFrames(b []byte) (int, error)
	ReadFrames(b []byte) (int, error)
	Recover(cause error) error
	Close() error
}

const (
	pcmPeriod     = 10 * time.Millisecond
	micPeriods    = 4 // 40 ms in ALSA's buffer
	headsetPeriod = 8 // 80 ms: room for a late reader
	pcmWaitMax    = 500 * time.Millisecond

	// The jitter buffer in front of the microphone. Audio from a browser
	// arrives in bursts; playback starts once micPrefill is queued and the
	// oldest audio is dropped beyond micMaxQueue, so latency stays bounded.
	micPrefill  = 40 * time.Millisecond
	micMaxQueue = 200 * time.Millisecond
	// Queued audio is thrown away once the phone has taken nothing for this
	// long: it is not recording, and the audio would be stale when it did.
	micStaleAfter = time.Second

	// The browser's microphone format (/api/hid/audio).
	micInputRate = 16000

	pcmRetryMax = 10 * time.Second
	// A stream that stayed up this long was healthy; its failure is new.
	pcmStableAfter = 5 * time.Second
)

// pcmRetryMin is the first reopen delay; a variable so tests can shorten it.
var pcmRetryMin = time.Second

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
			s, _ := readTrim(filepath.Join(dir, attr))
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
func NewGadgetAudio(cfg gadgetAudioConfig, ab *AudioBroadcaster) *GadgetAudio {
	return newGadgetAudio(cfg, ab, "/proc/asound", openPCM)
}

func newGadgetAudio(cfg gadgetAudioConfig, ab *AudioBroadcaster, procAsound string,
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

// ---- microphone ------------------------------------------------------------

// micStream takes the browser's 16 kHz mono audio (Write, from
// /api/hid/audio) and plays it into the gadget microphone (pump).
type micStream struct {
	rate, channels int

	mu        sync.Mutex
	up        *upsampler
	queue     []int16 // interleaved, at rate/channels
	playing   bool    // prefill reached; draining the queue
	lastTaken time.Time
	dropped   int // frames dropped for being too far behind, since last logged
	lastDrop  time.Time
}

func newMicStream(rate, channels int) *micStream {
	return &micStream{
		rate:     rate,
		channels: channels,
		up:       newUpsampler(micInputRate, rate, channels),
	}
}

func (m *micStream) frames(d time.Duration) int {
	return int(int64(m.rate) * int64(d) / int64(time.Second))
}

// Write queues browser audio. It never blocks and never fails for want of a
// listener: if the phone is not recording, the audio is dropped.
func (m *micStream) Write(pcm []byte) error {
	in := make([]int16, len(pcm)/2)
	for i := range in {
		in[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, m.up.process(in)...)
	if over := len(m.queue)/m.channels - m.frames(micMaxQueue); over > 0 {
		m.queue = m.queue[over*m.channels:]
		m.dropped += over
		if time.Since(m.lastDrop) > 10*time.Second {
			log.Printf("USB audio microphone: dropped %d frames arriving faster than "+
				"the phone takes them", m.dropped)
			m.dropped, m.lastDrop = 0, time.Now()
		}
	}
	return nil
}

// take fills out with queued audio, then silence. Playback holds off until
// micPrefill has built up, so a stream that has just started (or run dry)
// does not stutter on every late packet.
func (m *micStream) take(out []int16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastTaken = time.Now()
	queued := len(m.queue) / m.channels
	if !m.playing && queued >= m.frames(micPrefill) {
		m.playing = true
	}
	n := 0
	if m.playing {
		n = copy(out, m.queue)
		m.queue = m.queue[n:]
		if len(m.queue) == 0 {
			m.playing = false
			m.queue = nil
		}
	}
	clear(out[n:])
}

// idle is called while the phone takes nothing.
func (m *micStream) idle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queue) > 0 && time.Since(m.lastTaken) > micStaleAfter {
		m.queue, m.playing = nil, false
	}
}

// pump keeps the gadget microphone fed one period at a time, with queued
// audio or silence, so the phone hears silence rather than a replayed
// buffer when nobody is talking. It returns only when the device fails.
func (m *micStream) pump(ctx context.Context, s pcmStream) error {
	p := s.Params()
	if p.rate != m.rate || p.channels != m.channels {
		return fmt.Errorf("device is %d Hz x %d, expected %d Hz x %d", p.rate, p.channels, m.rate, m.channels)
	}
	samples := make([]int16, p.periodFrames*p.channels)
	buf := make([]byte, len(samples)*2)
	pending := 0 // bytes of buf not yet written
	xruns, lastXrunLog := 0, time.Time{}
	m.mu.Lock()
	m.lastTaken = time.Now()
	m.mu.Unlock()
	for ctx.Err() == nil {
		ready, err := s.Wait(pcmWaitMax)
		if err != nil {
			return err
		}
		if !ready {
			m.idle() // the phone is not recording
			continue
		}
		if pending == 0 {
			m.take(samples)
			for i, v := range samples {
				binary.LittleEndian.PutUint16(buf[2*i:], uint16(v))
			}
			pending = len(buf)
		}
		n, err := s.WriteFrames(buf[len(buf)-pending:])
		switch {
		case err == nil:
			pending -= n * 2 * p.channels
		case errors.Is(err, errPCMAgain):
		case errors.Is(err, errPCMXrun), errors.Is(err, errPCMSuspended):
			xruns++
			if time.Since(lastXrunLog) > 10*time.Second {
				log.Printf("USB audio microphone: %v (%d since last report); recovering", err, xruns)
				xruns, lastXrunLog = 0, time.Now()
			}
			if rerr := s.Recover(err); rerr != nil {
				return rerr
			}
		default:
			return err
		}
	}
	return nil
}

// ---- speaker ---------------------------------------------------------------

// headsetStream reads what the phone plays to its USB speaker and hands it
// to the AudioBroadcaster behind /audio, in the format the HDMI capture path
// has always produced: little-endian float32, interleaved.
type headsetStream struct {
	ab *AudioBroadcaster
}

func (h *headsetStream) pump(ctx context.Context, s pcmStream) error {
	p := s.Params()
	h.ab.SetParams(AudioParams{SampleRate: p.rate, Channels: p.channels, Format: "f32-interleaved"})
	defer h.ab.SetInactive()
	buf := make([]byte, p.periodFrames*p.channels*2)
	xruns, lastXrunLog := 0, time.Time{}
	for ctx.Err() == nil {
		ready, err := s.Wait(pcmWaitMax)
		if err != nil {
			return err
		}
		if !ready {
			continue // the phone is not playing anything
		}
		n, err := s.ReadFrames(buf)
		switch {
		case err == nil:
			if n > 0 && h.ab.Listeners() > 0 {
				h.ab.Broadcast(s16ToF32(buf[:n*p.channels*2]))
			}
		case errors.Is(err, errPCMAgain):
		case errors.Is(err, errPCMXrun), errors.Is(err, errPCMSuspended):
			xruns++
			if time.Since(lastXrunLog) > 10*time.Second {
				log.Printf("USB audio speaker: %v (%d since last report); recovering", err, xruns)
				xruns, lastXrunLog = 0, time.Now()
			}
			if rerr := s.Recover(err); rerr != nil {
				return rerr
			}
		default:
			return err
		}
	}
	return nil
}

func s16ToF32(b []byte) []byte {
	out := make([]byte, len(b)*2)
	for i := 0; i+1 < len(b); i += 2 {
		v := float32(int16(binary.LittleEndian.Uint16(b[i:]))) / 32768
		binary.LittleEndian.PutUint32(out[i*2:], math.Float32bits(v))
	}
	return out
}
