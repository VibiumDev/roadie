package rtc

import (
	"image"
	"sync"
	"testing"
	"time"

	"github.com/VibiumDev/roadie/encoder"
	"github.com/pion/webrtc/v4"
)

// fakeEncoder emits a made-up access unit per frame: a keyframe with SPS
// and PPS first, then slices. The packetizer does not look inside.
type fakeEncoder struct {
	samples chan encoder.Sample
	n       int
	closed  chan struct{}
}

var (
	fakeSPS   = []byte{0, 0, 0, 1, 0x67, 0x42, 0xc0, 0x28, 0xaa}
	fakePPS   = []byte{0, 0, 0, 1, 0x68, 0xce, 0x38, 0x80}
	fakeIDR   = []byte{0, 0, 0, 1, 0x65, 0x88, 0x84, 0x80}
	fakeSlice = []byte{0, 0, 0, 1, 0x41, 0x9a, 0x02}
)

func (f *fakeEncoder) Encode(fr encoder.Frame) error {
	f.n++
	var au []byte
	key := f.n%30 == 1
	if key {
		au = append(append(append([]byte{}, fakeSPS...), fakePPS...), fakeIDR...)
	} else {
		au = append([]byte{}, fakeSlice...)
	}
	select {
	case f.samples <- encoder.Sample{Data: au, Keyframe: key, Timestamp: fr.Timestamp}:
	default:
	}
	return nil
}
func (f *fakeEncoder) Samples() <-chan encoder.Sample { return f.samples }
func (f *fakeEncoder) SetBitrate(int) error           { return nil }
func (f *fakeEncoder) ForceKeyframe() error           { f.n = 0; return nil }
func (f *fakeEncoder) Dropped() int                   { return 0 }
func (f *fakeEncoder) Close() error {
	close(f.samples)
	close(f.closed)
	return nil
}

// fakeSource plays frames to its sink at 30 fps while one is attached.
type fakeSource struct {
	mu    sync.Mutex
	stop  chan struct{}
	added chan struct{}
	gone  chan struct{}
}

func (s *fakeSource) AddSink(k FrameSink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stop := make(chan struct{})
	s.stop = stop
	img := image.NewYCbCr(image.Rect(0, 0, 64, 48), image.YCbCrSubsampleRatio422)
	go func() {
		t := time.NewTicker(33 * time.Millisecond)
		defer t.Stop()
		start := time.Now()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				k.Frame(img, img.Bounds(), time.Since(start))
			}
		}
	}()
	close(s.added)
}

func (s *fakeSource) RemoveSink(FrameSink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
	close(s.gone)
}

func TestSessionLoopback(t *testing.T) {
	src := &fakeSource{added: make(chan struct{}), gone: make(chan struct{})}
	enc := &fakeEncoder{samples: make(chan encoder.Sample, 4), closed: make(chan struct{})}
	b, err := New(src, Options{
		Loopback: true,
		Logf:     t.Logf,
		OpenEncoder: func(cfg encoder.Config) (encoder.Encoder, string, error) {
			if cfg.Width != 64 || cfg.Height != 48 {
				t.Errorf("encoder opened at %dx%d", cfg.Width, cfg.Height)
			}
			return enc, "fake", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// The viewer: a plain peer connection, like a browser's.
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
	gotRTP := make(chan struct{})
	var once sync.Once
	viewer.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if tr.Codec().MimeType != webrtc.MimeTypeH264 {
			t.Errorf("track codec %s", tr.Codec().MimeType)
		}
		if _, _, err := tr.ReadRTP(); err == nil {
			once.Do(func() { close(gotRTP) })
		}
	})

	msgs := make(chan Message, 64)
	s, err := b.newSession(func(m Message) error { msgs <- m; return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	viewer.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			s.candidate(c.ToJSON())
		}
	})
	deadline := time.After(15 * time.Second)
	for done := false; !done; {
		select {
		case m := <-msgs:
			switch m.Type {
			case "offer":
				if err := viewer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: m.SDP}); err != nil {
					t.Fatal(err)
				}
				ans, err := viewer.CreateAnswer(nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := viewer.SetLocalDescription(ans); err != nil {
					t.Fatal(err)
				}
				if err := s.answer(ans.SDP); err != nil {
					t.Fatal(err)
				}
			case "candidate":
				if err := viewer.AddICECandidate(*m.Candidate); err != nil {
					t.Fatal(err)
				}
			case "error":
				t.Fatal(m.Error)
			}
		case <-gotRTP:
			done = true
		case <-deadline:
			t.Fatal("no RTP reached the viewer")
		}
	}
	select {
	case <-src.added:
	default:
		t.Error("the first session did not tap the source")
	}
	if st := b.Status(); st.Sessions != 1 || st.Encoder != "fake" || st.Width != 64 {
		t.Errorf("status %+v", st)
	}
	s.Close()
	select {
	case <-src.gone:
	case <-time.After(5 * time.Second):
		t.Error("the last session leaving did not release the source")
	}
	select {
	case <-enc.closed:
	case <-time.After(5 * time.Second):
		t.Error("the encoder was not closed")
	}
}
