// rtcprobe is a headless WebRTC viewer: it joins a Roadie's signaling
// endpoint, receives the H.264 track and reports what arrives, so the
// remote video path can be checked from a script. It is the seed of
// Vibium Station's station-probe.
//
//	rtcprobe -url http://roadie.local:8080 [-token T] [-t 10s]
//
// It exits non-zero when no video arrives.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
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
	flag.Parse()
	if err := run(*base, *token, *dur); err != nil {
		log.Fatal(err)
	}
}

func run(base, token string, dur time.Duration) error {
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
	se := webrtc.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	defer pc.Close()

	var st struct {
		sync.Mutex
		packets, bytes, keyframes, frames int
		first, last                       time.Time
		lastTS                            uint32
		spsSeen                           bool
	}
	gotVideo := make(chan struct{})
	var once sync.Once
	pc.OnTrack(func(tr *webrtc.TrackRemote, recv *webrtc.RTPReceiver) {
		fmt.Printf("track: %s %s\n", tr.Kind(), tr.Codec().MimeType)
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
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			init := c.ToJSON()
			send(message{Type: "candidate", Candidate: &init})
		}
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) { fmt.Printf("connection: %s\n", s) })

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
	send(message{Type: "bye"})
	st.Lock()
	defer st.Unlock()
	span := st.last.Sub(st.first).Seconds()
	if span <= 0 {
		span = 1
	}
	fmt.Printf("video: %d frames (%.1f fps), %d packets, %.2f Mbps, %d keyframes, SPS seen: %v, over %.1fs\n",
		st.frames, float64(st.frames)/span, st.packets, float64(st.bytes)*8/span/1e6, st.keyframes, st.spsSeen, span)
	if st.frames < 10 || !st.spsSeen {
		os.Exit(1)
	}
	return nil
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
