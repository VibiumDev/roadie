package rtc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/VibiumDev/roadie/audio/g722"
	"github.com/VibiumDev/roadie/encoder"
	"github.com/VibiumDev/station"
	"github.com/VibiumDev/station/rig"
	"github.com/VibiumDev/station/token"
	"github.com/coder/websocket"
	"github.com/pion/ice/v4"
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
func (a *fakeAudio) AudioActive() bool       { return true }
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
	s, err := b.newSession(func(m Message) error { msgs <- m; return nil }, nil, true)
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

// noFrames is a capture with nothing to show, for sessions that come and go.
type noFrames struct{}

func (noFrames) AddSink(FrameSink)    {}
func (noFrames) RemoveSink(FrameSink) {}

// Through a station, a grant that carries the view scope alone gets the
// picture and the sound. What it sends on the "hid" channel and its
// microphone are dropped; with the input scope both reach the target.
func TestViewGrantSessionTakesNoInput(t *testing.T) {
	onlyLoopback := func(name string) bool { return name == "lo" }
	b, err := New(noFrames{}, Options{
		Loopback: true, Interfaces: onlyLoopback, Logf: t.Logf,
		OpenEncoder: func(encoder.Config) (encoder.Encoder, string, error) { return nil, "", encoder.ErrUnavailable },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	hidGot := make(chan string, 16)
	micGot := make(chan struct{}, 1024)
	heard := make(chan struct{}, 16)
	b.SetInput(func(m []byte) { hidGot <- string(m) }, func([]byte) error {
		select {
		case micGot <- struct{}{}:
		default:
		}
		return nil
	})
	b.OnViewerRecording(func() { heard <- struct{}{} })

	// The rig's gate in front of the signaling socket, as a station's
	// tunnel reaches it: the grant is in the hello and needs view.
	stationKey, rigKey := nostr.Generate(), nostr.Generate()
	gate := &rig.Gate{Key: rigKey.Public(), Devices: func() []string { return []string{"rig_1/phone"} }, Logf: t.Logf,
		Policy: &rig.Policy{Stations: []*rig.StationEntry{{Name: "st", Npub: rig.Npub(stationKey.Public()), RigID: "rig_1", Scopes: []string{"view", "input"}}}}}
	ts := httptest.NewServer(gate.Middleware(rig.Allowlist{{Path: "/api/webrtc/signal", Scope: station.ScopeView, FirstMessage: true}}, http.HandlerFunc(b.ServeSignal)))
	defer ts.Close()

	// connect opens a session with a grant of these scopes, sends one
	// "hid" message and a tone on the microphone, and returns once the
	// host has taken a control message sent after them.
	connect := func(scopes ...station.Scope) {
		t.Helper()
		grant, err := token.SignGrant(stationKey, token.Grant{Rig: rigKey.Public(), RigID: "rig_1", DeviceID: "rig_1/phone",
			Subject: "someone", HoldID: "hld_1", Seq: 1, Scopes: scopes})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/webrtc/signal", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		ws.SetReadLimit(1 << 20)
		var wmu sync.Mutex
		send := func(v any) {
			data, _ := json.Marshal(v)
			wmu.Lock()
			defer wmu.Unlock()
			ws.Write(ctx, websocket.MessageText, data)
		}
		send(map[string]any{"type": "hello", "grant": grant})

		m := &webrtc.MediaEngine{}
		if err := m.RegisterDefaultCodecs(); err != nil {
			t.Fatal(err)
		}
		se := webrtc.SettingEngine{}
		se.SetIncludeLoopbackCandidate(true)
		se.SetInterfaceFilter(onlyLoopback)
		se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
		viewer, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			t.Fatal(err)
		}
		defer viewer.Close()
		mic, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeG722, ClockRate: 8000}, "mic", "viewer")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := viewer.AddTrack(mic); err != nil {
			t.Fatal(err)
		}
		channels := make(chan *webrtc.DataChannel, 2)
		viewer.OnDataChannel(func(dc *webrtc.DataChannel) { dc.OnOpen(func() { channels <- dc }) })
		viewer.OnICECandidate(func(c *webrtc.ICECandidate) {
			if c != nil {
				send(map[string]any{"type": "candidate", "candidate": c.ToJSON()})
			}
		})
		go func() {
			for {
				_, data, err := ws.Read(ctx)
				if err != nil {
					return
				}
				var msg Message
				if json.Unmarshal(data, &msg) != nil {
					continue
				}
				switch msg.Type {
				case "offer":
					if err := viewer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: msg.SDP}); err != nil {
						t.Error(err)
						return
					}
					ans, err := viewer.CreateAnswer(nil)
					if err != nil {
						t.Error(err)
						return
					}
					viewer.SetLocalDescription(ans)
					send(map[string]any{"type": "answer", "sdp": ans.SDP})
				case "candidate":
					if msg.Candidate != nil {
						viewer.AddICECandidate(*msg.Candidate)
					}
				case "error":
					t.Errorf("signal: %s", msg.Error)
				}
			}
		}()
		open := map[string]*webrtc.DataChannel{}
		for len(open) < 2 {
			select {
			case dc := <-channels:
				open[dc.Label()] = dc
			case <-ctx.Done():
				t.Fatal("the data channels did not open")
			}
		}
		// The microphone: a tone, 20 ms a packet, for half a second.
		enc := g722.NewEncoder()
		for n, i := 0, 0; i < 25; i++ {
			samples := make([]int16, 320)
			for j := range samples {
				samples[j] = int16(8000 * math.Sin(2*math.Pi*300*float64(n)/16000))
				n++
			}
			mic.WriteSample(media.Sample{Data: enc.Encode(nil, samples), Duration: 20 * time.Millisecond})
			time.Sleep(20 * time.Millisecond)
		}
		open["hid"].SendText(`{"cmd":"type","text":"hello"}`)
		open["control"].SendText(`{"type":"recording"}`)
		select {
		case <-heard:
		case <-ctx.Done():
			t.Fatal("the host did not take the control message")
		}
		time.Sleep(200 * time.Millisecond) // the two channels are not ordered with each other
	}

	connect(station.ScopeView)
	select {
	case got := <-hidGot:
		t.Errorf("a grant to watch sent input: %s", got)
	default:
	}
	select {
	case <-micGot:
		t.Error("a grant to watch spoke into the microphone")
	default:
	}

	connect(station.ScopeView, station.ScopeInput)
	select {
	case got := <-hidGot:
		if got != `{"cmd":"type","text":"hello"}` {
			t.Errorf("hid message %q", got)
		}
	default:
		t.Error("a grant with input sent none")
	}
	select {
	case <-micGot:
	default:
		t.Error("a grant with input was not heard")
	}
}
