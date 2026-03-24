package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
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

	ab.SetParams(AudioParams{SampleRate: 48000, Channels: 2, Format: "f32-planar"})
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
	s := &Server{
		Source:         &fakeSource{status: StatusConnected},
		AudioBroadcast: nil, // no audio
		FPS:            30,
	}
	w := httptest.NewRecorder()
	NewMux(s).ServeHTTP(w, httptest.NewRequest("GET", "/audio", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when no audio broadcaster, got %d", w.Code)
	}
}

func TestAudioHandlerInactive(t *testing.T) {
	ab := NewAudioBroadcaster()
	s := &Server{
		Source:         &fakeSource{status: StatusConnected},
		AudioBroadcast: ab, // present but inactive
		FPS:            30,
	}
	w := httptest.NewRecorder()
	NewMux(s).ServeHTTP(w, httptest.NewRequest("GET", "/audio", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when audio inactive, got %d", w.Code)
	}
}
