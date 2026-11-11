package web

import (
	"archive/zip"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VibiumDev/roadie/internal/audio"
)

// fakeSound plays a device: 48 kHz stereo in 20 ms chunks, a tone or
// silence, starting after wait.
type fakeSound struct {
	tone bool
	wait time.Duration

	mu   sync.Mutex
	subs int
}

func (f *fakeSound) Params() audio.AudioParams {
	return audio.AudioParams{SampleRate: 48000, Channels: 2, Format: "f32-interleaved"}
}
func (f *fakeSound) IsActive() bool { return true }

func (f *fakeSound) Subscribe() (<-chan []byte, func()) {
	f.mu.Lock()
	f.subs++
	f.mu.Unlock()
	ch := make(chan []byte, 16)
	quit := make(chan struct{})
	go func() {
		select {
		case <-quit:
			return
		case <-time.After(f.wait):
		}
		const frames = 960
		n := 0
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			chunk := make([]byte, frames*2*4)
			for i := 0; i < frames; i++ {
				var v float32
				if f.tone {
					v = float32(0.3 * math.Sin(float64(n)*2*math.Pi*440/48000))
				}
				binary.LittleEndian.PutUint32(chunk[i*8:], math.Float32bits(v))
				binary.LittleEndian.PutUint32(chunk[i*8+4:], math.Float32bits(v))
				n++
			}
			select {
			case ch <- chunk:
			default:
			}
			select {
			case <-quit:
				return
			case <-tick.C:
			}
		}
	}()
	var once sync.Once
	return ch, func() { once.Do(func() { close(quit) }) }
}

// wavOf reads audio/in-session.wav and its entry in the index.
func wavOf(t *testing.T, zr *zip.Reader) (samples []int16, durationMs float64) {
	t.Helper()
	f, err := zr.Open("audio/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Version int `json:"version"`
		Tracks  []struct {
			ID         string  `json:"id"`
			File       string  `json:"file"`
			SampleRate int     `json:"sampleRate"`
			Channels   int     `json:"channels"`
			StartMs    float64 `json:"startMs"`
			DurationMs float64 `json:"durationMs"`
		} `json:"tracks"`
	}
	err = json.NewDecoder(f).Decode(&index)
	f.Close()
	if err != nil || index.Version != 1 || len(index.Tracks) != 1 {
		t.Fatalf("audio/index.json: %+v, %v", index, err)
	}
	tr := index.Tracks[0]
	if tr.ID != "in-session" || tr.File != "audio/in-session.wav" || tr.SampleRate != 16000 || tr.Channels != 1 || tr.StartMs != 0 {
		t.Errorf("track %+v", tr)
	}
	w, err := zr.Open(tr.File)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(w)
	w.Close()
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:16]) != "WAVEfmt " || binary.LittleEndian.Uint32(data[24:]) != 16000 || int(binary.LittleEndian.Uint32(data[40:])) != len(data)-44 {
		t.Fatalf("%s is not a 16 kHz WAV (%d bytes)", tr.File, len(data))
	}
	for i := 44; i+1 < len(data); i += 2 {
		samples = append(samples, int16(binary.LittleEndian.Uint16(data[i:])))
	}
	return samples, tr.DurationMs
}

func loudest(samples []int16) int {
	peak := 0
	for _, v := range samples {
		peak = max(peak, int(max(v, -v)))
	}
	return peak
}

func TestRecorderKeepsWhatTheDevicePlayed(t *testing.T) {
	// The device says nothing for 200 ms, then plays a tone.
	sound := &fakeSound{tone: true, wait: 200 * time.Millisecond}
	tap := &fakeTap{sps: tallSPS}
	zr, _ := recordAHoldWith(t, tap, sound, 8<<20, func(*recorder) { time.Sleep(600 * time.Millisecond) })

	samples, durationMs := wavOf(t, zr)
	if durationMs < 550 || durationMs > 900 || math.Abs(float64(len(samples))/16-durationMs) > 1 {
		t.Errorf("the sound runs %.0f ms in %d samples; the hold ran about 600", durationMs, len(samples))
	}
	// Silence where the device was silent, the tone after.
	if peak := loudest(samples[:16*150]); peak > 8 {
		t.Errorf("the first 150 ms peak at %d; the device was silent", peak)
	}
	if peak := loudest(samples[16*350:]); peak < 5000 {
		t.Errorf("the tone peaks at %d", peak)
	}
	videos := readVideoIndex(t, zr)
	if len(videos) != 1 || !videos[0].Audio {
		t.Errorf("videos %+v; want one with a sound track", videos)
	}
	f, _ := zr.Open(videos[0].File)
	data, _ := io.ReadAll(f)
	f.Close()
	if !strings.Contains(string(data), "mp4a") || !strings.Contains(string(data), "soun") {
		t.Errorf("%s has no sound track", videos[0].File)
	}
}

func TestRecorderLeavesOutSilence(t *testing.T) {
	zr, _ := recordAHoldWith(t, &fakeTap{sps: tallSPS}, &fakeSound{}, 8<<20, func(*recorder) { time.Sleep(200 * time.Millisecond) })
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "audio/") {
			t.Errorf("%s in a recording where nothing was heard", f.Name)
		}
	}
	if videos := readVideoIndex(t, zr); len(videos) != 1 || videos[0].Audio {
		t.Errorf("videos %+v; want one without a sound track", videos)
	}
}

func TestRecorderKeepsSoundWithoutAnEncoder(t *testing.T) {
	zr, dir := recordAHoldWith(t, nil, &fakeSound{tone: true}, 8<<20, func(*recorder) { time.Sleep(200 * time.Millisecond) })
	if samples, _ := wavOf(t, zr); loudest(samples) < 5000 {
		t.Error("no tone in the sound")
	}
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "video/") {
			t.Errorf("%s from a host with no encoder", f.Name)
		}
	}
	if left := leftIn(dir); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}
