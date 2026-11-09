package main

import (
	"github.com/VibiumDev/roadie/internal/audio"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The /audio handler without a broadcaster and with an idle one; the
// broadcaster's own tests live with it in internal/audio.

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
	ab := audio.NewAudioBroadcaster()
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
