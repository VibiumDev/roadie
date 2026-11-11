// Package rtc sends the captured picture to browsers over WebRTC.
//
// A Broadcaster owns one H.264 track and one encoder, which runs only while
// a viewer is connected or a tap is taking the encoded picture: the first of
// them taps the capture loop's raw frames, the last one leaving stops the
// encoder again. It also owns one
// speaker track, the phone's sound as G.722, encoded the same way, only
// while someone is connected. Every session is a peer connection with the
// two tracks, a "control" data channel for keyframe and bitrate requests
// and stats, a "hid" data channel carrying the viewer's input as the JSON
// /api/hid/ws takes, the viewer's microphone back as an audio track, and
// signaling over a WebSocket.
package rtc

import (
	"encoding/json"
	"fmt"
	"image"
	"log"
	"net"
	"sync"
	"time"

	"github.com/VibiumDev/roadie/encoder"
	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// Options configure a Broadcaster.
type Options struct {
	// UDPPort is the one UDP port every peer connection uses (ICE on a
	// mux), so a firewall needs one rule. 0 picks an ephemeral port per
	// connection.
	UDPPort int
	// Encoder is the spec for encoder.Open: auto, v4l2m2m, ffmpeg[:codec].
	Encoder string
	FPS     int
	// Bitrate limits in bits per second; congestion control moves the
	// target between them. Zero means the defaults: 300 kbps to 4 Mbps,
	// starting at 1.5 Mbps.
	MinBitrate, MaxBitrate, StartBitrate int
	// Interfaces says which network interfaces to offer candidates on.
	// nil means all but the phone's own link (usb0), docker and loopback.
	Interfaces func(name string) bool
	Logf       func(format string, args ...any)
	// OpenEncoder replaces encoder.Open, for tests.
	OpenEncoder func(cfg encoder.Config) (encoder.Encoder, string, error)
	// Loopback offers loopback candidates too, for tests on one machine.
	Loopback bool
	// Audio is the phone's sound for the speaker track; nil sends none.
	Audio AudioSource
}

// FrameSource is where frames come from: the capture loop.
type FrameSource interface {
	AddSink(FrameSink)
	RemoveSink(FrameSink)
}

// FrameSink receives every captured frame with its crop. It must return
// quickly: it runs in the capture loop.
type FrameSink interface {
	Frame(img image.Image, crop image.Rectangle, ts time.Duration)
}

// Status is what /health reports.
type Status struct {
	Encoder  string `json:"encoder"`           // what Open would use, or did
	Sessions int    `json:"sessions"`          // connected viewers
	Bitrate  int    `json:"bitrate,omitempty"` // current target, bits per second
	Width    int    `json:"width,omitempty"`   // of the picture being encoded
	Height   int    `json:"height,omitempty"`
	FPS      int    `json:"fps,omitempty"`     // encoded in the last second
	Dropped  int    `json:"dropped,omitempty"` // frames skipped by the encoder
	// Audio is the speaker track's codec while sound is being sent, and
	// AudioKbps its rate over the last second; Mic is the codec of the
	// viewer's microphone being passed to the phone, if one is.
	Audio     string `json:"audio,omitempty"`
	AudioKbps int    `json:"audio_kbps,omitempty"`
	Mic       string `json:"mic,omitempty"`
	MicKbps   int    `json:"mic_kbps,omitempty"`
}

// Broadcaster encodes once and sends to every connected session.
type Broadcaster struct {
	opts    Options
	src     FrameSource
	api     *webrtc.API
	track   *webrtc.TrackLocalStaticSample
	speaker *webrtc.TrackLocalStaticSample
	udp     net.PacketConn
	probe   string // the encoder Probe found, for Status before any session

	mu       sync.Mutex
	sessions map[*Session]struct{}
	// The estimator the congestion-control interceptor made for the peer
	// connection being created; see newPeerConnection.
	newEstimator cc.BandwidthEstimator
	caps         map[*Session]int // bitrate caps asked for over control
	hidHandler   func([]byte)
	micSink      func([]byte) error
	micOwner     *Session
	micCodec     string
	speakerDone  chan struct{} // the speaker loop runs while sessions exist
	taps         map[int]SampleSink
	tapN         int

	encMu     sync.Mutex
	enc       encoder.Encoder
	encName   string
	encSize   image.Point
	encDone   chan struct{}
	sinking   bool
	bitrate   int
	lastTS    time.Duration
	sps, pps  []byte
	keyAt     time.Time
	wantKey   bool
	frameSkip bool // under heavy congestion, every other frame
	frameN    int

	statMu       sync.Mutex
	frames       int
	bytes        int
	fps          int
	kbps         int
	speakerBytes int
	speakerKbps  int
	micBytes     int
	micKbps      int
	micAt        time.Time // when the viewer's mic last delivered a packet
	statAt       time.Time
	stopStats    chan struct{}
}

const (
	defaultMinBitrate   = 300_000
	defaultMaxBitrate   = 4_000_000
	defaultStartBitrate = 1_500_000
	lowBitrate          = 600_000 // below this, halve the frame rate
)

var fmtp = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"

// New makes a Broadcaster. It opens the UDP port now; the encoder waits
// for the first viewer.
func New(src FrameSource, opts Options) (*Broadcaster, error) {
	if opts.FPS <= 0 {
		opts.FPS = 30
	}
	if opts.MinBitrate <= 0 {
		opts.MinBitrate = defaultMinBitrate
	}
	if opts.MaxBitrate <= 0 {
		opts.MaxBitrate = defaultMaxBitrate
	}
	if opts.StartBitrate <= 0 {
		opts.StartBitrate = defaultStartBitrate
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	if opts.Interfaces == nil {
		opts.Interfaces = defaultInterfaceFilter
	}
	b := &Broadcaster{
		opts:      opts,
		src:       src,
		sessions:  map[*Session]struct{}{},
		caps:      map[*Session]int{},
		taps:      map[int]SampleSink{},
		bitrate:   opts.StartBitrate,
		stopStats: make(chan struct{}),
	}
	if opts.OpenEncoder == nil {
		opts.OpenEncoder = func(cfg encoder.Config) (encoder.Encoder, string, error) {
			return encoder.Open(opts.Encoder, cfg)
		}
		if name, err := encoder.Probe(opts.Encoder); err == nil {
			b.probe = name
		} else {
			opts.Logf("webrtc: no H.264 encoder: %v", err)
		}
	} else {
		b.probe = "test"
	}
	b.opts = opts

	m := &webrtc.MediaEngine{}
	feedback := []webrtc.RTCPFeedback{{Type: "goog-remb"}, {Type: "ccm", Parameter: "fir"}, {Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "transport-cc"}}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: fmtp, RTCPFeedback: feedback},
		PayloadType:        102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	// Speech codecs every browser has, in order of preference: G.722, then
	// µ-law. Only these, so a browser does not pick Opus, which Roadie
	// has no pure-Go encoder for.
	for _, c := range []webrtc.RTPCodecParameters{
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeG722, ClockRate: 8000}, PayloadType: 9},
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000}, PayloadType: 0},
	} {
		if err := m.RegisterCodec(c, webrtc.RTPCodecTypeAudio); err != nil {
			return nil, err
		}
	}
	reg := &interceptor.Registry{}
	ccI, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		return gcc.NewSendSideBWE(
			gcc.SendSideBWEInitialBitrate(opts.StartBitrate),
			gcc.SendSideBWEMinBitrate(opts.MinBitrate),
			gcc.SendSideBWEMaxBitrate(opts.MaxBitrate))
	})
	if err != nil {
		return nil, err
	}
	ccI.OnNewPeerConnection(func(_ string, est cc.BandwidthEstimator) {
		b.newEstimator = est
	})
	reg.Add(ccI)
	if err := webrtc.ConfigureTWCCHeaderExtensionSender(m, reg); err != nil {
		return nil, err
	}
	if err := webrtc.RegisterDefaultInterceptors(m, reg); err != nil {
		return nil, err
	}

	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetInterfaceFilter(opts.Interfaces)
	if opts.Loopback {
		se.SetIncludeLoopbackCandidate(true)
	}
	if opts.UDPPort > 0 {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: opts.UDPPort})
		if err != nil {
			return nil, fmt.Errorf("webrtc: UDP port %d: %w", opts.UDPPort, err)
		}
		b.udp = conn
		se.SetICEUDPMux(webrtc.NewICEUDPMux(nil, conn))
	}
	b.api = webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(reg), webrtc.WithSettingEngine(se))

	b.track, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: fmtp}, "video", "roadie")
	if err != nil {
		return nil, err
	}
	b.speaker, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeG722, ClockRate: 8000}, "audio", "roadie")
	if err != nil {
		return nil, err
	}
	go b.statsLoop()
	return b, nil
}

// defaultInterfaceFilter keeps real interfaces: not the phone's own link
// through the USB gadget, not containers, not loopback.
func defaultInterfaceFilter(name string) bool {
	switch {
	case name == "lo", name == "usb0", name == "docker0":
		return false
	}
	for _, p := range []string{"veth", "br-", "virbr"} {
		if len(name) >= len(p) && name[:len(p)] == p {
			return false
		}
	}
	return true
}

// Available says whether an encoder was found, so WebRTC can be offered.
func (b *Broadcaster) Available() bool { return b.probe != "" }

// Status is the current state for /health and the control channel.
func (b *Broadcaster) Status() Status {
	b.mu.Lock()
	n := len(b.sessions)
	b.mu.Unlock()
	b.encMu.Lock()
	st := Status{Encoder: b.probe, Sessions: n, Bitrate: b.bitrate}
	if b.enc != nil {
		st.Encoder = b.encName
		st.Width, st.Height = b.encSize.X, b.encSize.Y
		st.Dropped = b.enc.Dropped()
	}
	b.encMu.Unlock()
	b.mu.Lock()
	speaking := b.speakerDone != nil && b.opts.Audio != nil && b.opts.Audio.AudioActive()
	st.Mic = b.micCodec
	b.mu.Unlock()
	b.statMu.Lock()
	st.FPS = b.fps
	if speaking {
		st.Audio, st.AudioKbps = "G722", b.speakerKbps
	}
	// A mic that has gone quiet, as when the viewer turns it off without
	// ending the track, is not reported.
	if st.Mic != "" && time.Since(b.micAt) < 2*time.Second {
		st.MicKbps = b.micKbps
	} else {
		st.Mic = ""
	}
	b.statMu.Unlock()
	return st
}

// Close ends every session and the encoder.
func (b *Broadcaster) Close() {
	b.mu.Lock()
	sessions := make([]*Session, 0, len(b.sessions))
	for s := range b.sessions {
		sessions = append(sessions, s)
	}
	b.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
	close(b.stopStats)
	if b.udp != nil {
		b.udp.Close()
	}
}

// addSession registers a session and starts encoding for the first one.
func (b *Broadcaster) addSession(s *Session) {
	b.mu.Lock()
	b.sessions[s] = struct{}{}
	if b.speakerDone == nil {
		b.speakerDone = make(chan struct{})
		go b.speakerLoop(b.speakerDone)
	}
	b.mu.Unlock()
	b.feed()
}

// removeSession forgets a session and stops encoding after the last one.
func (b *Broadcaster) removeSession(s *Session) {
	b.mu.Lock()
	delete(b.sessions, s)
	delete(b.caps, s)
	if len(b.sessions) == 0 && b.speakerDone != nil {
		close(b.speakerDone)
		b.speakerDone = nil
	}
	b.mu.Unlock()
	b.feed()
}

// SampleSink takes every encoded access unit: Annex-B, with its parameter
// sets on keyframes, and the time its picture was captured. It runs in the
// encoder's pump, so it must return at once, and it must not keep data.
type SampleSink func(data []byte, keyframe bool, ts time.Duration)

// Tap sends the encoded picture to sink until stop is called. A tap keeps
// the encoder running as a viewer does, and its first unit is a keyframe
// as soon as the encoder can make one.
func (b *Broadcaster) Tap(sink SampleSink) (stop func()) {
	b.mu.Lock()
	b.tapN++
	id := b.tapN
	b.taps[id] = sink
	b.mu.Unlock()
	b.feed()
	b.encMu.Lock()
	b.wantKey = true
	b.encMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.taps, id)
			b.mu.Unlock()
			b.feed()
		})
	}
}

// feed takes the capture loop's frames while a viewer or a tap wants the
// picture, and stops the encoder when none does.
func (b *Broadcaster) feed() {
	b.encMu.Lock()
	defer b.encMu.Unlock()
	b.mu.Lock()
	want := len(b.sessions) > 0 || len(b.taps) > 0
	b.mu.Unlock()
	switch {
	case want && !b.sinking:
		b.sinking = true
		b.src.AddSink(b)
	case !want && b.sinking:
		b.sinking = false
		b.src.RemoveSink(b)
		b.closeEncoderLocked()
	}
}

// Frame is the capture loop's hand-off: convert and queue this frame,
// opening the encoder at this crop's size when it is not running.
func (b *Broadcaster) Frame(img image.Image, crop image.Rectangle, ts time.Duration) {
	if crop.Empty() {
		crop = img.Bounds()
	}
	r := encoder.EvenRect(crop)
	if r.Empty() {
		return
	}
	b.encMu.Lock()
	defer b.encMu.Unlock()
	if !b.sinking {
		return
	}
	size := r.Size()
	if b.enc != nil && size != b.encSize {
		b.opts.Logf("webrtc: picture is now %dx%d, restarting the encoder", size.X, size.Y)
		b.closeEncoderLocked()
	}
	if b.enc == nil {
		enc, name, err := b.opts.OpenEncoder(encoder.Config{Width: size.X, Height: size.Y, FPS: b.opts.FPS, Bitrate: b.bitrate})
		if err != nil {
			if b.encName != "failed" {
				b.opts.Logf("webrtc: encoder: %v", err)
				b.encName = "failed"
			}
			return
		}
		b.enc, b.encName, b.encSize = enc, name, size
		b.encDone = make(chan struct{})
		b.lastTS = 0
		b.sps, b.pps = nil, nil
		b.opts.Logf("webrtc: encoding %dx%d with %s", size.X, size.Y, name)
		go b.pump(enc, b.encDone)
	}
	b.frameN++
	if b.frameSkip && b.frameN&1 == 1 {
		return
	}
	if b.wantKey {
		b.wantKey = false
		b.enc.ForceKeyframe()
	}
	if err := b.enc.Encode(encoder.Frame{Img: img, Rect: r, Timestamp: ts}); err != nil {
		b.opts.Logf("webrtc: encode: %v", err)
	}
}

// closeEncoderLocked stops the encoder; the caller holds encMu.
func (b *Broadcaster) closeEncoderLocked() {
	if b.enc == nil {
		return
	}
	enc, done := b.enc, b.encDone
	b.enc = nil
	b.encMu.Unlock()
	enc.Close()
	<-done
	b.encMu.Lock()
}

// pump writes encoded samples to the track until the encoder closes.
func (b *Broadcaster) pump(enc encoder.Encoder, done chan struct{}) {
	defer close(done)
	frame := time.Second / time.Duration(b.opts.FPS)
	for s := range enc.Samples() {
		data := s.Data
		if s.Keyframe {
			sps, pps := encoder.ParameterSets(data)
			switch {
			case sps != nil && pps != nil:
				b.sps, b.pps = sps, pps
			case b.sps != nil && b.pps != nil:
				// A keyframe without its headers would leave a viewer
				// who joined mid-stream unable to start.
				joined := make([]byte, 0, len(b.sps)+len(b.pps)+len(data))
				joined = append(joined, b.sps...)
				joined = append(joined, b.pps...)
				data = append(joined, data...)
			}
		}
		dur := frame
		if b.lastTS != 0 && s.Timestamp > b.lastTS {
			if d := s.Timestamp - b.lastTS; d < 200*time.Millisecond {
				dur = d
			}
		}
		b.lastTS = s.Timestamp
		if err := b.track.WriteSample(media.Sample{Data: data, Duration: dur}); err != nil {
			b.opts.Logf("webrtc: write sample: %v", err)
		}
		b.mu.Lock()
		taps := make([]SampleSink, 0, len(b.taps))
		for _, t := range b.taps {
			taps = append(taps, t)
		}
		b.mu.Unlock()
		for _, t := range taps {
			t(data, s.Keyframe, s.Timestamp)
		}
		b.statMu.Lock()
		b.frames++
		b.bytes += len(data)
		b.statMu.Unlock()
	}
}

// requestKeyframe asks the encoder for an IDR, at most a few times a
// second however many viewers ask.
func (b *Broadcaster) requestKeyframe() {
	b.encMu.Lock()
	defer b.encMu.Unlock()
	if time.Since(b.keyAt) < 300*time.Millisecond {
		return
	}
	b.keyAt = time.Now()
	b.wantKey = true
}

// setBitrate moves the encoder's target, clamped to the options' range.
func (b *Broadcaster) setBitrate(bps int) {
	if bps < b.opts.MinBitrate {
		bps = b.opts.MinBitrate
	}
	if bps > b.opts.MaxBitrate {
		bps = b.opts.MaxBitrate
	}
	b.encMu.Lock()
	defer b.encMu.Unlock()
	diff := bps - b.bitrate
	if diff < 0 {
		diff = -diff
	}
	if diff*20 < b.bitrate { // under 5%: not worth a change
		return
	}
	b.bitrate = bps
	b.frameSkip = bps < lowBitrate
	if b.enc != nil {
		if err := b.enc.SetBitrate(bps); err != nil {
			b.opts.Logf("webrtc: set bitrate: %v", err)
		}
	}
}

// statsLoop follows congestion control and viewer caps every half second,
// and keeps the per-second frame and bit rates.
func (b *Broadcaster) statsLoop() {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	b.statAt = time.Now()
	for {
		select {
		case <-b.stopStats:
			return
		case <-t.C:
		}
		b.mu.Lock()
		target := 0
		for s := range b.sessions {
			if s.estimator != nil {
				if est := s.estimator.GetTargetBitrate(); est > 0 && (target == 0 || est < target) {
					target = est
				}
			}
		}
		for _, c := range b.caps {
			if c > 0 && (target == 0 || c < target) {
				target = c
			}
		}
		n, taps := len(b.sessions), len(b.taps)
		b.mu.Unlock()
		switch {
		case n > 0 && target > 0:
			b.setBitrate(target)
		case n == 0 && taps > 0:
			// No viewer's network to follow: a tap gets the starting rate.
			b.setBitrate(b.opts.StartBitrate)
		}
		b.statMu.Lock()
		if since := time.Since(b.statAt); since >= time.Second {
			b.fps = int(float64(b.frames)/since.Seconds() + 0.5)
			b.kbps = int(float64(b.bytes) * 8 / since.Seconds() / 1000)
			b.speakerKbps = int(float64(b.speakerBytes) * 8 / since.Seconds() / 1000)
			b.micKbps = int(float64(b.micBytes) * 8 / since.Seconds() / 1000)
			b.frames, b.bytes, b.speakerBytes, b.micBytes = 0, 0, 0, 0
			b.statAt = time.Now()
		}
		b.statMu.Unlock()
	}
}

// Message is one signaling message, either way. Type is one of hello (the
// viewer's first message), offer, answer, candidate, bye and error.
type Message struct {
	Type      string                   `json:"type"`
	SDP       string                   `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit `json:"candidate,omitempty"`
	Error     string                   `json:"error,omitempty"`
	// Grant is the hold grant a viewer coming through a station puts in
	// its hello: the rig library checks it.
	Grant json.RawMessage `json:"grant,omitempty"`
}

// controlMessage is what travels on the control data channel.
type controlMessage struct {
	Type   string          `json:"type"` // keyframe, bitrate, grant (from the viewer); stats (to it)
	BPS    int             `json:"bps,omitempty"`
	Status *Status         `json:"status,omitempty"`
	Kbps   int             `json:"kbps,omitempty"`
	Grant  json.RawMessage `json:"grant,omitempty"` // a refreshed hold grant
}

// EndHold closes every session admitted on a hold: it was revoked or a
// newer hold took the device.
func (b *Broadcaster) EndHold(holdID string) {
	b.mu.Lock()
	var ended []*Session
	for s := range b.sessions {
		if s.hold == holdID {
			ended = append(ended, s)
		}
	}
	b.mu.Unlock()
	for _, s := range ended {
		s.send(Message{Type: "error", Error: "hold ended"})
		s.Close()
	}
}

func (b *Broadcaster) statsMessage() []byte {
	st := b.Status()
	b.statMu.Lock()
	kbps := b.kbps
	b.statMu.Unlock()
	data, _ := json.Marshal(controlMessage{Type: "stats", Status: &st, Kbps: kbps})
	return data
}
