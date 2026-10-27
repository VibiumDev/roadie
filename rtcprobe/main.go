// rtcprobe is a headless WebRTC viewer: it joins a Roadie's signaling
// endpoint, receives the H.264 and G.722 tracks and reports what arrives,
// can send one input command over the "hid" data channel and a tone as its
// microphone, so the remote path can be checked from a script. It is the
// seed of Vibium Station's station-probe.
//
//	rtcprobe -url http://roadie.local:8080 [-token T] [-t 10s] [-hid '{"cmd":"mouse_move","x":16383,"y":16383}'] [-mic]
//
// It exits non-zero when no video arrives.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/VibiumDev/roadie/audio/g711"
	"github.com/VibiumDev/roadie/audio/g722"
	"github.com/coder/websocket"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

type message struct {
	Type      string                   `json:"type"`
	SDP       string                   `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit `json:"candidate,omitempty"`
	Error     string                   `json:"error,omitempty"`
}

func main() {
	base := flag.String("url", "http://127.0.0.1:8080", "the Roadie to watch")
	token := flag.String("token", "", "its local admin token (--auth token)")
	dur := flag.Duration("t", 10*time.Second, "how long to watch")
	out := flag.String("out", "", "write the H.264 stream here (Annex-B), for a look with ffmpeg")
	capBps := flag.Int("cap", 0, "ask for this bitrate cap over the control channel, bits per second")
	hidMsg := flag.String("hid", "", "send this JSON command once on the hid data channel, as /api/hid/ws takes it")
	mic := flag.Bool("mic", false, "send a 300 Hz tone as the microphone, G.722, for the whole watch")
	flag.Parse()
	if err := run(*base, *token, *dur, *out, *capBps, *hidMsg, *mic); err != nil {
		log.Fatal(err)
	}
}

func run(base, token string, dur time.Duration, outPath string, capBps int, hidMsg string, mic bool) error {
	var outFile *os.File
	if outPath != "" {
		var err error
		if outFile, err = os.Create(outPath); err != nil {
			return err
		}
		defer outFile.Close()
	}
	wsURL := strings.Replace(strings.Replace(base, "https://", "wss://", 1), "http://", "ws://", 1) + "/api/webrtc/signal"
	ctx, cancel := context.WithTimeout(context.Background(), dur+20*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{HTTPHeader: http.Header{}}
	if token != "" {
		opts.HTTPHeader.Set("Authorization", "Bearer "+token)
	}
	conn, _, err := websocket.Dial(ctx, wsURL, opts)
	if err != nil {
		return fmt.Errorf("signaling: %w", err)
	}
	defer conn.CloseNow()
	var wmu sync.Mutex
	send := func(m message) error {
		b, _ := json.Marshal(m)
		wmu.Lock()
		defer wmu.Unlock()
		return conn.Write(ctx, websocket.MessageText, b)
	}

	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return err
	}
	// The usual interceptors, so the host gets the receiver reports and
	// transport feedback its congestion control needs, like a browser's.
	reg := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, reg); err != nil {
		return err
	}
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(reg), webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	defer pc.Close()

	// The microphone: a tone on a G.722 track, added before the answer so
	// the audio line is two-way, written once connected.
	var micTrack *webrtc.TrackLocalStaticSample
	if mic {
		micTrack, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeG722, ClockRate: 8000}, "mic", "rtcprobe")
		if err != nil {
			return err
		}
		if _, err := pc.AddTrack(micTrack); err != nil {
			return err
		}
	}

	var st struct {
		sync.Mutex
		packets, bytes, keyframes, frames int
		first, last                       time.Time
		lastTS                            uint32
		spsSeen                           bool
		audioPackets, audioBytes          int
		audioCodec                        string
		audioPeak                         int16
		audioFirst, audioLast             time.Time
		micPackets                        int
		hidSent                           bool
	}
	gotVideo := make(chan struct{})
	var once sync.Once
	pc.OnTrack(func(tr *webrtc.TrackRemote, recv *webrtc.RTPReceiver) {
		fmt.Printf("track: %s %s\n", tr.Kind(), tr.Codec().MimeType)
		if tr.Kind() == webrtc.RTPCodecTypeAudio {
			readAudio(tr, func(n int, samples []int16) {
				st.Lock()
				st.audioPackets++
				st.audioBytes += n
				st.audioCodec = tr.Codec().MimeType
				now := time.Now()
				if st.audioFirst.IsZero() {
					st.audioFirst = now
				}
				st.audioLast = now
				for _, v := range samples {
					if v < 0 {
						v = -v
					}
					if v > st.audioPeak {
						st.audioPeak = v
					}
				}
				st.Unlock()
			})
			return
		}
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for range t.C {
				if ctx.Err() != nil {
					return
				}
				pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(tr.SSRC())}})
			}
		}()
		var depacketizer codecs.H264Packet
		for {
			pkt, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			once.Do(func() { close(gotVideo) })
			st.Lock()
			st.packets++
			st.bytes += len(pkt.Payload)
			now := time.Now()
			if st.first.IsZero() {
				st.first = now
			}
			st.last = now
			if pkt.Timestamp != st.lastTS {
				st.frames++
				st.lastTS = pkt.Timestamp
			}
			if nal, err := depacketizer.Unmarshal(pkt.Payload); err == nil {
				if outFile != nil {
					outFile.Write(nal)
				}
				sps, _, idr := nalTypes(nal)
				if sps {
					st.spsSeen = true
				}
				if idr && pkt.Marker {
					st.keyframes++
				}
			}
			st.Unlock()
		}
	})
	var controlMsgs int
	var lastControl string
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		fmt.Printf("data channel: %s\n", dc.Label())
		switch dc.Label() {
		case "hid":
			dc.OnOpen(func() {
				if hidMsg != "" {
					if err := dc.SendText(hidMsg); err == nil {
						st.Lock()
						st.hidSent = true
						st.Unlock()
					}
				}
			})
		case "control":
			dc.OnOpen(func() {
				if capBps > 0 {
					dc.SendText(fmt.Sprintf(`{"type":"bitrate","bps":%d}`, capBps))
				}
			})
			dc.OnMessage(func(m webrtc.DataChannelMessage) {
				st.Lock()
				controlMsgs++
				lastControl = string(m.Data)
				st.Unlock()
			})
		}
	})
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			init := c.ToJSON()
			send(message{Type: "candidate", Candidate: &init})
		}
	})
	micStop := make(chan struct{})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		fmt.Printf("connection: %s\n", s)
		if s == webrtc.PeerConnectionStateConnected && micTrack != nil {
			go sendTone(micTrack, micStop, func() { st.Lock(); st.micPackets++; st.Unlock() })
		}
	})

	if err := send(message{Type: "hello"}); err != nil {
		return err
	}
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg message
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			switch msg.Type {
			case "offer":
				if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: msg.SDP}); err != nil {
					log.Printf("offer: %v", err)
					return
				}
				ans, err := pc.CreateAnswer(nil)
				if err != nil {
					log.Printf("answer: %v", err)
					return
				}
				pc.SetLocalDescription(ans)
				send(message{Type: "answer", SDP: ans.SDP})
			case "candidate":
				if msg.Candidate != nil {
					pc.AddICECandidate(*msg.Candidate)
				}
			case "error":
				log.Printf("roadie: %s", msg.Error)
			}
		}
	}()

	select {
	case <-gotVideo:
	case <-time.After(15 * time.Second):
		return fmt.Errorf("no video within 15 s")
	}
	time.Sleep(dur)
	close(micStop)
	send(message{Type: "bye"})
	st.Lock()
	defer st.Unlock()
	span := st.last.Sub(st.first).Seconds()
	if span <= 0 {
		span = 1
	}
	fmt.Printf("video: %d frames (%.1f fps), %d packets, %.2f Mbps, %d keyframes, SPS seen: %v, over %.1fs\n",
		st.frames, float64(st.frames)/span, st.packets, float64(st.bytes)*8/span/1e6, st.keyframes, st.spsSeen, span)
	fmt.Printf("control: %d messages, last: %s\n", controlMsgs, lastControl)
	if st.audioPackets > 0 {
		aspan := st.audioLast.Sub(st.audioFirst).Seconds()
		if aspan <= 0 {
			aspan = 1
		}
		fmt.Printf("audio: %s, %d packets, %.0f kbps, peak %.0f dBFS, over %.1fs\n", st.audioCodec, st.audioPackets,
			float64(st.audioBytes)*8/aspan/1e3, 20*math.Log10(float64(max(int(st.audioPeak), 1))/32768), aspan)
	} else {
		fmt.Println("audio: none")
	}
	if hidMsg != "" {
		fmt.Printf("hid: sent %v\n", st.hidSent)
	}
	if micTrack != nil {
		fmt.Printf("mic: %d packets of G.722 sent\n", st.micPackets)
	}
	if st.frames < 10 || !st.spsSeen {
		os.Exit(1)
	}
	return nil
}

// readAudio decodes the speaker track, G.722 or µ-law, reporting each
// packet's payload size and samples.
func readAudio(tr *webrtc.TrackRemote, got func(n int, samples []int16)) {
	dec := g722.NewDecoder()
	mime := strings.ToUpper(tr.Codec().MimeType)
	for {
		pkt, _, err := tr.ReadRTP()
		if err != nil {
			return
		}
		var samples []int16
		switch mime {
		case "AUDIO/G722":
			samples = dec.Decode(nil, pkt.Payload)
		case "AUDIO/PCMU":
			samples = g711.DecodeULaw(nil, pkt.Payload)
		}
		got(len(pkt.Payload), samples)
	}
}

// sendTone writes a 300 Hz tone, 20 ms a packet, until stopped.
func sendTone(track *webrtc.TrackLocalStaticSample, stop chan struct{}, sent func()) {
	enc := g722.NewEncoder()
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	var n int
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		samples := make([]int16, 320)
		for i := range samples {
			samples[i] = int16(8000 * math.Sin(2*math.Pi*300*float64(n)/16000))
			n++
		}
		if err := track.WriteSample(media.Sample{Data: enc.Encode(nil, samples), Duration: 20 * time.Millisecond}); err == nil {
			sent()
		}
	}
}

func nalTypes(b []byte) (sps, pps, idr bool) {
	for i := 0; i+3 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			switch b[i+3] & 0x1f {
			case 7:
				sps = true
			case 8:
				pps = true
			case 5:
				idr = true
			}
		}
	}
	return
}
