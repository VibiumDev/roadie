package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/pion/mediadevices/pkg/wave"
)

func TestAudioBroadcasterSubscribeUnsubscribe(t *testing.T) {
	ab := NewAudioBroadcaster()

	ch, unsub := ab.Subscribe()

	chunk := []byte{1, 2, 3, 4}
	ab.Broadcast(chunk)

	got := <-ch
	if len(got) != 4 || got[0] != 1 {
		t.Errorf("expected chunk {1,2,3,4}, got %v", got)
	}

	unsub()

	// Channel should be closed after unsubscribe.
	if _, ok := <-ch; ok {
		t.Error("expected channel to be closed after unsubscribe")
	}
}

func TestAudioBroadcasterSlowConsumer(t *testing.T) {
	ab := NewAudioBroadcaster()
	_, unsub := ab.Subscribe() // subscribe but never read
	defer unsub()

	// Broadcast 20 chunks — must not block.
	for i := 0; i < 20; i++ {
		ab.Broadcast([]byte{byte(i)})
	}
}

func TestAudioBroadcasterConcurrent(t *testing.T) {
	ab := NewAudioBroadcaster()

	var wg sync.WaitGroup
	wg.Add(3)

	// Concurrent broadcaster.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			ab.Broadcast([]byte{byte(i)})
		}
	}()

	// Concurrent subscribe/unsubscribe.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, unsub := ab.Subscribe()
			unsub()
		}
	}()

	// Concurrent reader.
	go func() {
		defer wg.Done()
		ch, unsub := ab.Subscribe()
		defer unsub()
		for i := 0; i < 50; i++ {
			select {
			case <-ch:
			default:
			}
		}
	}()

	wg.Wait()
}

func TestAudioBroadcasterParams(t *testing.T) {
	ab := NewAudioBroadcaster()

	if ab.IsActive() {
		t.Error("expected inactive initially")
	}

	ab.SetParams(AudioParams{SampleRate: 48000, Channels: 2, Format: "f32-interleaved"})
	if !ab.IsActive() {
		t.Error("expected active after SetParams")
	}

	p := ab.Params()
	if p.SampleRate != 48000 || p.Channels != 2 {
		t.Errorf("unexpected params: %+v", p)
	}

	ab.SetInactive()
	if ab.IsActive() {
		t.Error("expected inactive after SetInactive")
	}
}

func TestAudioHandlerNoAudio(t *testing.T) {
	buf := &FrameBuffer{}
	buf.SetFPS(30)
	s := &Server{
		Source:         &fakeSource{status: StatusConnected},
		Buf:            buf,
		AudioBroadcast: nil, // no audio
	}
	w := httptest.NewRecorder()
	NewMux(s).ServeHTTP(w, httptest.NewRequest("GET", "/audio", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when no audio broadcaster, got %d", w.Code)
	}
}

func TestAudioHandlerInactive(t *testing.T) {
	buf := &FrameBuffer{}
	buf.SetFPS(30)
	ab := NewAudioBroadcaster()
	s := &Server{
		Source:         &fakeSource{status: StatusConnected},
		Buf:            buf,
		AudioBroadcast: ab, // present but inactive
	}
	w := httptest.NewRecorder()
	NewMux(s).ServeHTTP(w, httptest.NewRequest("GET", "/audio", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when audio inactive, got %d", w.Code)
	}
}

// readerFunc adapts a function to the audio reader interface the capture
// loop consumes.
type readerFunc func() (wave.Audio, func(), error)

func (f readerFunc) Read() (wave.Audio, func(), error) { return f() }

// TestRunAudioCaptureSurvivesDriverPanic covers the case that killed the
// whole server: the microphone driver panics inside Read instead of
// returning an error, which an ALSA null device does on a machine with no
// real audio hardware. Audio is secondary to video and input, so this has
// to come back rather than take the process down.
func TestRunAudioCaptureSurvivesDriverPanic(t *testing.T) {
	ab := NewAudioBroadcaster()
	reader := readerFunc(func() (wave.Audio, func(), error) {
		panic("unsupported format")
	})

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		runAudioCapture(context.Background(), "null device", reader, ab)
	}()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("runAudioCapture did not return after the driver panicked")
	}
}

func TestRunAudioCaptureStopsAfterRepeatedErrors(t *testing.T) {
	ab := NewAudioBroadcaster()
	var reads int
	reader := readerFunc(func() (wave.Audio, func(), error) {
		reads++
		return nil, nil, errors.New("device gone")
	})

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		runAudioCapture(context.Background(), "flaky device", reader, ab)
	}()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("runAudioCapture kept reading a device that only returns errors")
	}
	if reads < maxConsecutiveErrors {
		t.Errorf("gave up after %d reads, want at least %d", reads, maxConsecutiveErrors)
	}
}

func TestRunAudioCaptureStopsOnContextCancel(t *testing.T) {
	ab := NewAudioBroadcaster()
	ctx, cancel := context.WithCancel(context.Background())
	reader := readerFunc(func() (wave.Audio, func(), error) {
		// A working device: silence, and nothing to release.
		return wave.NewFloat32Interleaved(wave.ChunkInfo{
			Len: 16, Channels: 1, SamplingRate: 16000,
		}), func() {}, nil
	})

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		runAudioCapture(ctx, "good device", reader, ab)
	}()

	cancel()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("runAudioCapture ignored a cancelled context")
	}
}
