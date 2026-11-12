package rtc

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/VibiumDev/roadie/audio/g711"
	"github.com/VibiumDev/roadie/audio/g722"
	"github.com/VibiumDev/roadie/audio/pcm"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// AudioSource is the phone's sound as the capture delivers it: chunks of
// interleaved little-endian float32 samples at AudioFormat's rate and
// channel count. The channel closes when the format changes or the source
// goes away; the reader subscribes again and reads the new format.
type AudioSource interface {
	Subscribe() (<-chan []byte, func())
	AudioFormat() (rate, channels int)
	AudioActive() bool
}

// The speaker track: 16 kHz mono as G.722, 20 ms a packet.
const (
	speakerRate    = 16000
	speakerFrame   = 320 // samples per packet
	speakerPacket  = 20 * time.Millisecond
	audioRetryWait = 500 * time.Millisecond
)

// SetInput gives the broadcaster where input from viewers goes: hid takes
// each message of the "hid" data channel, the JSON /api/hid/ws accepts,
// with a context that ends when the viewer's session does, and mic takes
// the viewer's microphone as 16 kHz mono 16-bit samples, the way /api/mic
// does. Either may be nil.
func (b *Broadcaster) SetInput(hid func(ctx context.Context, msg []byte), mic func(pcm []byte) error) {
	b.mu.Lock()
	b.hidHandler, b.micSink = hid, mic
	b.mu.Unlock()
}

func (b *Broadcaster) handleHID(ctx context.Context, msg []byte) {
	b.mu.Lock()
	h := b.hidHandler
	b.mu.Unlock()
	if h != nil {
		h(ctx, msg)
	}
}

// speakerLoop encodes the phone's sound for the speaker track while any
// session is connected, taking the source as it comes and going: with
// nothing to send it waits, and a format change restarts the converter.
func (b *Broadcaster) speakerLoop(done chan struct{}) {
	enc := g722.NewEncoder()
	var pending []int16
	for {
		src := b.opts.Audio
		if src == nil || !src.AudioActive() {
			select {
			case <-done:
				return
			case <-time.After(audioRetryWait):
			}
			continue
		}
		rate, channels := src.AudioFormat()
		down := pcm.NewDownsampler(rate, speakerRate, channels)
		chunks, unsub := src.Subscribe()
		pending = pending[:0]
	read:
		for {
			select {
			case <-done:
				unsub()
				return
			case chunk, ok := <-chunks:
				if !ok {
					break read
				}
				pending = down.Process(pending, pcm.Float32LE(chunk))
				for len(pending) >= speakerFrame {
					frame := enc.Encode(make([]byte, 0, speakerFrame/2), pending[:speakerFrame])
					n := copy(pending, pending[speakerFrame:])
					pending = pending[:n]
					if err := b.speaker.WriteSample(media.Sample{Data: frame, Duration: speakerPacket}); err != nil {
						b.opts.Logf("webrtc: speaker: %v", err)
					}
					b.statMu.Lock()
					b.speakerBytes += len(frame)
					b.statMu.Unlock()
				}
			}
		}
		unsub()
	}
}

// micReader decodes one viewer's microphone track into the phone. Only one
// viewer's mic is passed on at a time: the first to send keeps it until its
// track ends, and the rest are read and dropped.
type micReader struct {
	b     *Broadcaster
	s     *Session
	track *webrtc.TrackRemote
}

func (m *micReader) run() {
	mime := strings.ToUpper(m.track.Codec().MimeType)
	// A viewer that may only watch never has the microphone: its packets
	// are read and dropped, and the mic stays free for one who may speak.
	owner := m.s.input.Load() && m.b.claimMic(m.s, mime)
	defer m.b.releaseMic(m.s)
	dec := g722.NewDecoder()
	var up pcm.Upsample2
	var samples []int16
	var out []byte
	var logged sync.Once
	for {
		pkt, _, err := m.track.ReadRTP()
		if err != nil {
			return
		}
		if !owner || !m.s.input.Load() || len(pkt.Payload) == 0 {
			continue
		}
		samples = samples[:0]
		switch mime {
		case "AUDIO/G722":
			samples = dec.Decode(samples, pkt.Payload)
		case "AUDIO/PCMU":
			samples = up.Process(samples, g711.DecodeULaw(nil, pkt.Payload))
		default:
			continue
		}
		out = pcm.Int16LE(out[:0], samples)
		m.b.mu.Lock()
		sink := m.b.micSink
		m.b.mu.Unlock()
		if sink == nil {
			continue
		}
		if err := sink(out); err != nil {
			logged.Do(func() { m.b.opts.Logf("webrtc: mic: %v", err) })
			continue
		}
		m.b.statMu.Lock()
		m.b.micBytes += len(pkt.Payload)
		m.b.micAt = time.Now()
		m.b.statMu.Unlock()
	}
}

// claimMic makes s the session whose microphone reaches the phone, unless
// another already has it.
func (b *Broadcaster) claimMic(s *Session, mime string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.micOwner != nil && b.micOwner != s {
		return false
	}
	b.micOwner, b.micCodec = s, strings.TrimPrefix(mime, "AUDIO/")
	return true
}

func (b *Broadcaster) releaseMic(s *Session) {
	b.mu.Lock()
	if b.micOwner == s {
		b.micOwner, b.micCodec = nil, ""
	}
	b.mu.Unlock()
}
