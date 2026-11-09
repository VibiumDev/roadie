package alsa

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/VibiumDev/roadie/internal/audio"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The ALSA PCM layer under the gadget's audio and the dongle's: stream
// parameters, the open stream, the microphone and headset pumps and the
// retry timing they share.

var (
	ErrPCMAgain     = errors.New("pcm: not ready")
	ErrPCMXrun      = errors.New("pcm: underrun/overrun")
	ErrPCMSuspended = errors.New("pcm: suspended")
	ErrPCMGone      = errors.New("pcm: device gone")
)

// pcmParams is a stream's format and buffering.
type PCMParams struct {
	Rate, Channels        int
	PeriodFrames, Periods int
}

// pcmStream is an open, non-blocking S16_LE interleaved PCM stream.
type PCMStream interface {
	Params() PCMParams
	// Wait returns true once a period can be transferred, false on timeout.
	Wait(timeout time.Duration) (bool, error)
	WriteFrames(b []byte) (int, error)
	ReadFrames(b []byte) (int, error)
	Recover(cause error) error
	Close() error
}

const (
	PcmPeriod     = 10 * time.Millisecond
	MicPeriods    = 4 // 40 ms in ALSA's buffer
	HeadsetPeriod = 8 // 80 ms: room for a late reader
	pcmWaitMax    = 500 * time.Millisecond

	// The jitter buffer in front of the microphone. Audio from a browser
	// arrives in bursts; playback starts once micPrefill is queued and the
	// oldest audio is dropped beyond micMaxQueue, so latency stays bounded.
	micPrefill  = 40 * time.Millisecond
	micMaxQueue = 200 * time.Millisecond
	// Queued audio is thrown away once the phone has taken nothing for this
	// long: it is not recording, and the audio would be stale when it did.
	micStaleAfter = time.Second

	// The browser's microphone format (/api/mic).
	micInputRate = 16000

	PcmRetryMax = 10 * time.Second
	// A stream that stayed up this long was healthy; its failure is new.
	PcmStableAfter = 5 * time.Second
)

// pcmRetryMin is the first reopen delay; a variable so tests can shorten it.
var PcmRetryMin = time.Second

// ---- microphone ------------------------------------------------------------

// micStream takes the browser's 16 kHz mono audio (Write, from
// /api/mic) and plays it into the gadget microphone (pump).
type MicStream struct {
	rate, channels int

	mu        sync.Mutex
	up        *upsampler
	Queue     []int16 // interleaved, at rate/channels
	playing   bool    // prefill reached; draining the queue
	lastTaken time.Time
	dropped   int // frames dropped for being too far behind, since last logged
	lastDrop  time.Time
}

func NewMicStream(rate, channels int) *MicStream {
	return &MicStream{
		rate:     rate,
		channels: channels,
		up:       newUpsampler(micInputRate, rate, channels),
	}
}

func (m *MicStream) frames(d time.Duration) int {
	return int(int64(m.rate) * int64(d) / int64(time.Second))
}

// Write queues browser audio. It never blocks and never fails for want of a
// listener: if the phone is not recording, the audio is dropped.
func (m *MicStream) Write(pcm []byte) error {
	in := make([]int16, len(pcm)/2)
	for i := range in {
		in[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Queue = append(m.Queue, m.up.process(in)...)
	if over := len(m.Queue)/m.channels - m.frames(micMaxQueue); over > 0 {
		m.Queue = m.Queue[over*m.channels:]
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
func (m *MicStream) take(out []int16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastTaken = time.Now()
	queued := len(m.Queue) / m.channels
	if !m.playing && queued >= m.frames(micPrefill) {
		m.playing = true
	}
	n := 0
	if m.playing {
		n = copy(out, m.Queue)
		m.Queue = m.Queue[n:]
		if len(m.Queue) == 0 {
			m.playing = false
			m.Queue = nil
		}
	}
	clear(out[n:])
}

// idle is called while the phone takes nothing.
func (m *MicStream) idle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Queue) > 0 && time.Since(m.lastTaken) > micStaleAfter {
		m.Queue, m.playing = nil, false
	}
}

// pump keeps the gadget microphone fed one period at a time, with queued
// audio or silence, so the phone hears silence rather than a replayed
// buffer when nobody is talking. It returns only when the device fails.
func (m *MicStream) Pump(ctx context.Context, s PCMStream) error {
	p := s.Params()
	if p.Rate != m.rate || p.Channels != m.channels {
		return fmt.Errorf("device is %d Hz x %d, expected %d Hz x %d", p.Rate, p.Channels, m.rate, m.channels)
	}
	samples := make([]int16, p.PeriodFrames*p.Channels)
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
			pending -= n * 2 * p.Channels
		case errors.Is(err, ErrPCMAgain):
		case errors.Is(err, ErrPCMXrun), errors.Is(err, ErrPCMSuspended):
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
type HeadsetStream struct {
	Ab *audio.AudioBroadcaster
}

func (h *HeadsetStream) Pump(ctx context.Context, s PCMStream) error {
	p := s.Params()
	h.Ab.SetParams(audio.AudioParams{SampleRate: p.Rate, Channels: p.Channels, Format: "f32-interleaved"})
	defer h.Ab.SetInactive()
	buf := make([]byte, p.PeriodFrames*p.Channels*2)
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
			if n > 0 && h.Ab.Listeners() > 0 {
				h.Ab.Broadcast(s16ToF32(buf[:n*p.Channels*2]))
			}
		case errors.Is(err, ErrPCMAgain):
		case errors.Is(err, ErrPCMXrun), errors.Is(err, ErrPCMSuspended):
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

// alsaCardIndex finds a card's number from its id. Card numbers depend on
// what else is plugged in; ids do not.
func CardIndex(procAsound, id string) (int, error) {
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
