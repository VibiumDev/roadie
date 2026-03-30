package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pion/mediadevices/pkg/driver"
	"github.com/pion/mediadevices/pkg/prop"
	"github.com/pion/mediadevices/pkg/wave"

	// Register microphone drivers via init().
	_ "github.com/pion/mediadevices/pkg/driver/microphone"
)

// AudioParams describes the audio stream format. Sent as the first WebSocket message.
type AudioParams struct {
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels"`
	Format     string `json:"format"` // "f32-planar"
}

// AudioBroadcaster fans out raw PCM audio chunks to WebSocket subscribers.
type AudioBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[uint64]chan []byte
	nextID      uint64
	params      AudioParams
	active      atomic.Bool
}

// NewAudioBroadcaster creates an AudioBroadcaster ready for use.
func NewAudioBroadcaster() *AudioBroadcaster {
	return &AudioBroadcaster{
		subscribers: make(map[uint64]chan []byte),
	}
}

// Subscribe returns a channel that receives audio chunks and an unsubscribe function.
// The channel is buffered (cap 16 ≈ 320ms at 20ms/chunk).
func (ab *AudioBroadcaster) Subscribe() (<-chan []byte, func()) {
	ab.mu.Lock()
	id := ab.nextID
	ab.nextID++
	ch := make(chan []byte, 16)
	ab.subscribers[id] = ch
	ab.mu.Unlock()

	return ch, func() {
		ab.mu.Lock()
		delete(ab.subscribers, id)
		close(ch)
		ab.mu.Unlock()
	}
}

// Listeners returns the number of active audio subscribers.
func (ab *AudioBroadcaster) Listeners() int {
	ab.mu.RLock()
	n := len(ab.subscribers)
	ab.mu.RUnlock()
	return n
}

// Broadcast sends a chunk to all subscribers. Non-blocking: drops the oldest
// chunk for slow consumers.
func (ab *AudioBroadcaster) Broadcast(chunk []byte) {
	ab.mu.RLock()
	defer ab.mu.RUnlock()

	for _, ch := range ab.subscribers {
		select {
		case ch <- chunk:
		default:
			// Slow consumer — drop oldest, then send new.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- chunk:
			default:
			}
		}
	}
}

// SetParams updates the audio parameters and marks the broadcaster as active.
func (ab *AudioBroadcaster) SetParams(p AudioParams) {
	ab.mu.Lock()
	ab.params = p
	ab.mu.Unlock()
	ab.active.Store(true)
}

// SetInactive marks the broadcaster as inactive (device disconnected).
func (ab *AudioBroadcaster) SetInactive() {
	ab.active.Store(false)
}

// Params returns the current audio parameters.
func (ab *AudioBroadcaster) Params() AudioParams {
	ab.mu.RLock()
	defer ab.mu.RUnlock()
	return ab.params
}

// IsActive returns true if audio is currently being captured.
func (ab *AudioBroadcaster) IsActive() bool {
	return ab.active.Load()
}

// DetectAudioDevice queries microphone drivers and returns the best candidate.
// Uses the same skip/prefer heuristics as video detection.
// Returns (info, false) if no suitable device is found (audio is optional).
func DetectAudioDevice(filter string) (deviceInfo, bool) {
	drivers := driver.GetManager().Query(func(d driver.Driver) bool {
		return d.Info().DeviceType == driver.Microphone
	})

	if len(drivers) == 0 {
		return deviceInfo{}, false
	}

	skipKeywords := []string{"facetime", "iphone", "macbook", "imac", "integrated"}
	preferKeywords := []string{"usb", "hdmi", "capture", "video"}

	type candidate struct {
		info deviceInfo
	}
	var candidates []candidate
	for _, d := range drivers {
		info := d.Info()
		name := info.Name
		if name == "" {
			name = info.Label
		}
		candidates = append(candidates, candidate{
			info: deviceInfo{Name: name, Label: info.Label},
		})
	}

	// If a filter is provided, find the first match.
	if filter != "" {
		for _, c := range candidates {
			if strings.Contains(strings.ToLower(c.info.Name), strings.ToLower(filter)) {
				return c.info, true
			}
		}
		return deviceInfo{}, false
	}

	// Auto-detect: skip built-in, prefer external capture devices.
	for _, c := range candidates {
		lower := strings.ToLower(c.info.Name)
		skip := false
		for _, kw := range skipKeywords {
			if strings.Contains(lower, kw) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		for _, kw := range preferKeywords {
			if strings.Contains(lower, kw) {
				return c.info, true
			}
		}
	}

	// Fallback: return first non-skipped device.
	for _, c := range candidates {
		lower := strings.ToLower(c.info.Name)
		skip := false
		for _, kw := range skipKeywords {
			if strings.Contains(lower, kw) {
				skip = true
				break
			}
		}
		if !skip {
			return c.info, true
		}
	}

	return deviceInfo{}, false
}

// ListAudioDevices returns a list of all audio device names for diagnostic output.
func ListAudioDevices() []string {
	drivers := driver.GetManager().Query(func(d driver.Driver) bool {
		return d.Info().DeviceType == driver.Microphone
	})
	var names []string
	for _, d := range drivers {
		name := d.Info().Name
		if name == "" {
			name = d.Info().Label
		}
		names = append(names, name)
	}
	return names
}

// StartAudioCapture opens the microphone device and streams PCM audio to the broadcaster.
// Returns a channel that closes when capture stops (device error or context cancellation).
func StartAudioCapture(ctx context.Context, dev deviceInfo, ab *AudioBroadcaster) (<-chan struct{}, error) {
	drivers := driver.GetManager().Query(func(d driver.Driver) bool {
		return d.Info().DeviceType == driver.Microphone && d.Info().Label == dev.Label
	})
	if len(drivers) == 0 {
		return nil, fmt.Errorf("audio device %q not found", dev.Name)
	}

	d := drivers[0]
	if err := d.Open(); err != nil {
		return nil, fmt.Errorf("failed to open audio device %q: %w", dev.Name, err)
	}

	// Query properties and pick best format (prefer Float32).
	props := d.Properties()
	mediaProp := selectAudioProp(props)

	recorder, ok := d.(driver.AudioRecorder)
	if !ok {
		d.Close()
		return nil, fmt.Errorf("audio device %q does not support recording", dev.Name)
	}

	reader, err := recorder.AudioRecord(mediaProp)
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("failed to start audio recording on %q: %w", dev.Name, err)
	}

	ab.SetParams(AudioParams{
		SampleRate: mediaProp.SampleRate,
		Channels:   mediaProp.ChannelCount,
		Format:     "f32-planar",
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer d.Close()
		defer ab.SetInactive()

		var consecutiveErrors int
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			audio, release, err := reader.Read()
			if err != nil {
				consecutiveErrors++
				if consecutiveErrors >= maxConsecutiveErrors {
					log.Printf("audio device %q: %d consecutive errors, stopping", dev.Name, consecutiveErrors)
					return
				}
				continue
			}
			consecutiveErrors = 0

			if ab.Listeners() == 0 {
				release()
				continue
			}

			chunk := audioToBytes(audio)
			if chunk != nil {
				ab.Broadcast(chunk)
			}
			release()
		}
	}()

	return done, nil
}

// audioToBytes converts wave.Audio to little-endian float32 bytes.
func audioToBytes(audio wave.Audio) []byte {
	switch a := audio.(type) {
	case *wave.Float32Interleaved:
		buf := make([]byte, len(a.Data)*4)
		for i, v := range a.Data {
			binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
		}
		return buf
	case *wave.Int16Interleaved:
		buf := make([]byte, len(a.Data)*4)
		for i, v := range a.Data {
			f := float32(v) / 32768.0
			binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
		}
		return buf
	default:
		return nil
	}
}

// selectAudioProp picks a prop.Media suitable for audio capture.
// Prefers 48kHz, 2-channel, Float32.
func selectAudioProp(props []prop.Media) prop.Media {
	if len(props) == 0 {
		return prop.Media{}
	}

	best := props[0]
	bestScore := audioScore(best)
	for _, p := range props[1:] {
		s := audioScore(p)
		if s > bestScore {
			best = p
			bestScore = s
		}
	}
	return best
}

// audioScore returns a higher score for preferred audio properties.
func audioScore(p prop.Media) int {
	score := 0
	if p.SampleRate == 48000 {
		score += 10
	} else if p.SampleRate == 44100 {
		score += 5
	}
	if p.ChannelCount == 2 {
		score += 5
	}
	if p.IsFloat {
		score += 3
	}
	return score
}
