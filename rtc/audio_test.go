package rtc

import (
	"encoding/binary"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/VibiumDev/roadie/audio/g722"
	"github.com/VibiumDev/roadie/encoder"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// fakeAudio is a 48 kHz stereo tone, 20 ms a chunk, as the capture's
// audio broadcaster delivers it.
type fakeAudio struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func (a *fakeAudio) AudioFormat() (int, int) { return 48000, 2 }
func (a *fakeAudio) AudioActive() bool      { return true }
func (a *fakeAudio) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 16)
	a.mu.Lock()
	if a.subs == nil {
		a.subs = map[chan []byte]struct{}{}
	}
	a.subs[ch] = struct{}{}
	a.mu.Unlock()
	return ch, func() {
		a.mu.Lock()
		if _, ok := a.subs[ch]; ok {
			delete(a.subs, ch)
			close(ch)
		}
		a.mu.Unlock()
	}
}

// run sends chunks until done.
func (a *fakeAudio) run(done chan struct{}) {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	var n int
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		chunk := make([]byte, 960*2*4)
		for i := 0; i < 960; i++ {
			v := math.Float32bits(float32(0.3 * math.Sin(2*math.Pi*440*float64(n)/48000)))
			binary.LittleEndian.PutUint32(chunk[8*i:], v)
			binary.LittleEndian.PutUint32(chunk[8*i+4:], v)
			n++
		}
		a.mu.Lock()
		for ch := range a.subs {
			select {
			case ch <- chunk:
			default:
			}
		}
		a.mu.Unlock()
	}
}

// A session carries the speaker as G.722, takes the viewer's microphone
// back as 16 kHz samples, and passes "hid" messages to the input handler.
func TestSessionAudioAndInput(t *testing.T) {
	src := &fakeSource{added: make(chan struct{}), gone: make(chan struct{})}
	enc := &fakeEncoder{samples: make(chan encoder.Sample, 4), closed: make(chan struct{})}
	audio := &fakeAudio{}
	audioDone := make(chan struct{})
	defer close(audioDone)
	go audio.run(audioDone)
	b, err := New(src, Options{
		Loopback: true,
		Logf:     t.Logf,
		Audio:    audio,
		OpenEncoder: func(cfg encoder.Config) (encoder.Encoder, string, error) {
			return enc, "fake", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	hidGot := make(chan []byte, 4)
	micGot := make(chan []byte, 64)
	b.SetInput(func(m []byte) { hidGot <- m }, func(p []byte) error { micGot <- p; return nil })

	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	viewer, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	// The viewer's microphone: a G.722 track added before the answer, so
	// the audio line is two-way.
	mic, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeG722, ClockRate: 8000}, "mic", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := viewer.AddTrack(mic); err != nil {
		t.Fatal(err)
	}
	gotAudio := make(chan string, 1)
	var audioOnce sync.Once
	viewer.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if tr.Kind() != webrtc.RTPCodecTypeAudio {
			for {
				if _, _, err := tr.ReadRTP(); err != nil {
					return
				}
			}
		}
		for {
			pkt, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			if len(pkt.Payload) == 160 {
				audioOnce.Do(func() { gotAudio <- tr.Codec().MimeType })
			}
		}
	})
	hidOpen := make(chan struct{}, 1)
	viewer.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "hid" {
			return
		}
		dc.OnOpen(func() {
			dc.SendText(`{"cmd":"mouse_move","x":1,"y":2}`)
			hidOpen <- struct{}{}
		})
	})
	connected := make(chan struct{}, 1)
	viewer.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateConnected {
			connected <- struct{}{}
		}
	})

	msgs := make(chan Message, 64)
	s, err := b.newSession(func(m Message) error { msgs <- m; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	viewer.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			s.candidate(c.ToJSON())
		}
	})
	go func() {
		for m := range msgs {
			switch m.Type {
			case "offer":
				if err := viewer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP}); err != nil {
					t.Error(err)
					return
				}
				ans, err := viewer.CreateAnswer(nil)
				if err != nil {
					t.Error(err)
					return
				}
				if err := viewer.SetLocalDescription(ans); err != nil {
					t.Error(err)
					return
				}
				if err := s.answer(ans.SDP); err != nil {
					t.Error(err)
					return
				}
			case "candidate":
				viewer.AddICECandidate(*m.Candidate)
			case "error":
				t.Error(m.Error)
			}
		}
	}()

	deadline := time.After(20 * time.Second)
	select {
	case <-connected:
	case <-deadline:
		t.Fatal("not connected")
	}
	// The microphone: a tone, 20 ms a packet, until the Pi has some.
	micEnc := g722.NewEncoder()
	micDone := make(chan struct{})
	defer close(micDone)
	go func() {
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		var n int
		for {
			select {
			case <-micDone:
				return
			case <-tk.C:
			}
			samples := make([]int16, 320)
			for i := range samples {
				samples[i] = int16(8000 * math.Sin(2*math.Pi*300*float64(n)/16000))
				n++
			}
			mic.WriteSample(media.Sample{Data: micEnc.Encode(nil, samples), Duration: 20 * time.Millisecond})
		}
	}()

	var haveHID, haveAudio, haveMic bool
	for !(haveHID && haveAudio && haveMic) {
		select {
		case got := <-hidGot:
			if string(got) != `{"cmd":"mouse_move","x":1,"y":2}` {
				t.Errorf("hid message %q", got)
			}
			haveHID = true
		case mime := <-gotAudio:
			if mime != webrtc.MimeTypeG722 {
				t.Errorf("speaker codec %s", mime)
			}
			haveAudio = true
		case pcm := <-micGot:
			if len(pcm) != 640 {
				t.Errorf("mic chunk of %d bytes, want 640", len(pcm))
			}
			// Not silence: the tone comes through the decoder.
			var peak int16
			for i := 0; i+1 < len(pcm); i += 2 {
				if v := int16(binary.LittleEndian.Uint16(pcm[i:])); v > peak {
					peak = v
				}
			}
			if peak > 4000 {
				haveMic = true
			}
		case <-deadline:
			t.Fatalf("after 20 s: hid %v, audio %v, mic %v", haveHID, haveAudio, haveMic)
		}
	}
	st := b.Status()
	if st.Audio != "G722" || st.Mic != "G722" {
		t.Errorf("status audio %q mic %q", st.Audio, st.Mic)
	}
	select {
	case <-hidOpen:
	default:
	}
}
