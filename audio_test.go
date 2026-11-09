package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
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

func TestSendAudioMirrorsToMonitor(t *testing.T) {
	mp := &mockPort{}
	hc := newTestHID(mp)
	defer hc.Shutdown()

	ch, unsub := hc.Monitor.Subscribe()
	defer unsub()

	pcm := make([]byte, 700) // spans two 512-byte board frames
	for i := range pcm {
		pcm[i] = byte(i)
	}
	if err := hc.SendAudio(pcm); err != nil {
		t.Fatal(err)
	}

	got := <-ch
	if len(got) != len(pcm) {
		t.Fatalf("monitor got %d bytes, want %d", len(got), len(pcm))
	}
	for i := range got {
		if got[i] != pcm[i] {
			t.Fatalf("monitor byte %d = %d, want %d", i, got[i], pcm[i])
		}
	}
}

func TestSendAudioDisconnectedMirrorsNothing(t *testing.T) {
	hc := NewHIDController("")
	defer hc.Shutdown()

	ch, unsub := hc.Monitor.Subscribe()
	defer unsub()

	if err := hc.SendAudio([]byte{1, 2, 3, 4}); err == nil {
		t.Fatal("expected error while disconnected")
	}
	select {
	case got := <-ch:
		t.Fatalf("monitor got %d bytes, want none", len(got))
	default:
	}
}

func TestAudioInMonitorNoHID(t *testing.T) {
	buf := &FrameBuffer{}
	buf.SetFPS(30)
	s := &Server{
		Source: &fakeSource{status: StatusConnected},
		Buf:    buf,
	}
	w := httptest.NewRecorder()
	NewMux(s).ServeHTTP(w, httptest.NewRequest("GET", "/audio-in/monitor", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 without HID, got %d", w.Code)
	}
}

func TestAudioInMonitorStream(t *testing.T) {
	mp := &mockPort{}
	hc := newTestHID(mp)
	defer hc.Shutdown()

	buf := &FrameBuffer{}
	buf.SetFPS(30)
	s := &Server{
		Source: &fakeSource{status: StatusConnected},
		Buf:    buf,
		HID:    hc,
	}
	ts := httptest.NewServer(NewMux(s))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, ts.URL+"/audio-in/monitor", nil)
	if err != nil {
		t.Fatalf("dial /audio-in/monitor: %v", err)
	}
	defer conn.CloseNow()

	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("first message type = %v, want text params", typ)
	}
	var params AudioParams
	if err := json.Unmarshal(data, &params); err != nil {
		t.Fatal(err)
	}
	if params.SampleRate != 16000 || params.Channels != 1 || params.Format != "s16le" {
		t.Fatalf("unexpected params: %+v", params)
	}

	// The subscription races the dial: wait for it before injecting audio.
	deadline := time.Now().Add(2 * time.Second)
	for hc.Monitor.Listeners() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	pcm := []byte{1, 2, 3, 4, 5, 6}
	if err := hc.SendAudio(pcm); err != nil {
		t.Fatal(err)
	}

	typ, data, err = conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageBinary || len(data) != len(pcm) || data[0] != 1 {
		t.Fatalf("binary chunk = type %v, %v", typ, data)
	}
}
