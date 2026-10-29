package rtc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"github.com/VibiumDev/station/rig"
	"github.com/VibiumDev/station/token"
	"github.com/coder/websocket"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// Session is one viewer's peer connection.
type Session struct {
	b         *Broadcaster
	pc        *webrtc.PeerConnection
	control   *webrtc.DataChannel
	estimator cc.BandwidthEstimator
	send      func(Message) error

	// Through a station: the lease the viewer was admitted on, and the
	// check a refreshed grant goes through.
	lease string
	check func(ev nostr.Event) (token.Grant, error)

	mu         sync.Mutex
	haveRemote bool
	pending    []webrtc.ICECandidateInit // candidates that arrived before the answer

	closeOnce sync.Once
	done      chan struct{}
}

// ServeSignal is the signaling endpoint: a WebSocket whose first message is
// {"type":"hello"}; the Pi then sends its offer and ICE candidates, and the
// viewer answers with its own. The same messages travel through a station
// later, so nothing here depends on the WebSocket itself.
func (b *Broadcaster) ServeSignal(w http.ResponseWriter, r *http.Request) {
	if !b.Available() {
		http.Error(w, "no H.264 encoder on this host", http.StatusServiceUnavailable)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	var wmu sync.Mutex
	send := func(m Message) error {
		data, err := json.Marshal(m)
		if err != nil {
			return err
		}
		wmu.Lock()
		defer wmu.Unlock()
		wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return conn.Write(wctx, websocket.MessageText, data)
	}
	var hello Message
	if _, data, err := conn.Read(ctx); err != nil {
		return
	} else if err := json.Unmarshal(data, &hello); err != nil || hello.Type != "hello" {
		send(Message{Type: "error", Error: "expected a hello"})
		return
	}
	// A viewer that came through a station's tunnel must hold a lease: the
	// rig library's check is in the request context, and the grant in the
	// hello. On the LAN there is no check and no grant.
	var lease string
	var ice []webrtc.ICEServer
	check := rig.FirstMessageCheck(ctx)
	if check != nil {
		var ev nostr.Event
		if hello.Grant == nil || json.Unmarshal(hello.Grant, &ev) != nil {
			send(Message{Type: "error", Error: "a lease grant is required"})
			return
		}
		grant, err := check(ev)
		if err != nil {
			send(Message{Type: "error", Error: "lease: " + err.Error()})
			return
		}
		lease = grant.LeaseID
		// The grant names the station's STUN and TURN servers, with the
		// lease's credentials, so this side can be reached across NATs.
		for _, srv := range grant.ICE {
			ice = append(ice, webrtc.ICEServer{URLs: srv.URLs, Username: srv.Username, Credential: srv.Credential})
		}
	}
	s, err := b.newSession(send, ice)
	if err != nil {
		send(Message{Type: "error", Error: err.Error()})
		return
	}
	s.lease, s.check = lease, check
	defer s.Close()
	// A peer connection that fails ends the WebSocket too, so the viewer
	// notices and falls back or reconnects.
	go func() {
		select {
		case <-s.done:
			conn.Close(websocket.StatusNormalClosure, "closed")
		case <-ctx.Done():
		}
	}()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var m Message
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		switch m.Type {
		case "answer":
			if err := s.answer(m.SDP); err != nil {
				send(Message{Type: "error", Error: err.Error()})
				return
			}
		case "candidate":
			if m.Candidate != nil {
				s.candidate(*m.Candidate)
			}
		case "bye":
			return
		}
	}
}

// newSession makes the peer connection, offers, and registers the session.
func (b *Broadcaster) newSession(send func(Message) error, ice []webrtc.ICEServer) (*Session, error) {
	// The congestion-control interceptor reports its estimator for each new
	// peer connection through a callback; holding mu pairs it with ours.
	b.mu.Lock()
	b.newEstimator = nil
	pc, err := b.api.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	est := b.newEstimator
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s := &Session{b: b, pc: pc, estimator: est, send: send, done: make(chan struct{})}

	tr, err := pc.AddTransceiverFromTrack(b.track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		pc.Close()
		return nil, err
	}
	go s.readRTCP(tr.Sender())
	// The speaker, offered both ways so the viewer can send its microphone
	// back on the same line without a new offer.
	if _, err := pc.AddTransceiverFromTrack(b.speaker, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv}); err != nil {
		pc.Close()
		return nil, err
	}
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if tr.Kind() == webrtc.RTPCodecTypeAudio {
			(&micReader{b: b, s: s, track: tr}).run()
		}
	})

	ordered := true
	dc, err := pc.CreateDataChannel("control", &webrtc.DataChannelInit{Ordered: &ordered})
	if err != nil {
		pc.Close()
		return nil, err
	}
	s.control = dc
	dc.OnMessage(func(m webrtc.DataChannelMessage) { s.onControl(m.Data) })
	dc.OnOpen(func() { go s.sendStats() })
	// Input: the JSON /api/hid/ws takes, one message per command.
	hid, err := pc.CreateDataChannel("hid", &webrtc.DataChannelInit{Ordered: &ordered})
	if err != nil {
		pc.Close()
		return nil, err
	}
	hid.OnMessage(func(m webrtc.DataChannelMessage) { b.handleHID(m.Data) })

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		send(Message{Type: "candidate", Candidate: &init})
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		switch st {
		case webrtc.PeerConnectionStateConnected:
			// A viewer can only start on a keyframe.
			b.requestKeyframe()
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			s.Close()
		}
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		pc.Close()
		return nil, err
	}
	// Sent before SetLocalDescription starts gathering, so the offer always
	// reaches the viewer ahead of the candidates.
	if err := send(Message{Type: "offer", SDP: offer.SDP}); err != nil {
		pc.Close()
		return nil, err
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		pc.Close()
		return nil, err
	}
	b.addSession(s)
	return s, nil
}

func (s *Session) answer(sdp string) error {
	if err := s.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		return fmt.Errorf("answer: %w", err)
	}
	s.mu.Lock()
	s.haveRemote = true
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	for _, c := range pending {
		s.pc.AddICECandidate(c)
	}
	return nil
}

func (s *Session) candidate(c webrtc.ICECandidateInit) {
	s.mu.Lock()
	if !s.haveRemote {
		s.pending = append(s.pending, c)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.pc.AddICECandidate(c)
}

// readRTCP turns the viewer's picture-loss and full-intra requests into
// keyframes.
func (s *Session) readRTCP(sender *webrtc.RTPSender) {
	for {
		pkts, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, p := range pkts {
			switch p.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				s.b.requestKeyframe()
			}
		}
	}
}

// onControl handles the viewer's requests on the control channel.
func (s *Session) onControl(data []byte) {
	var m controlMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	switch m.Type {
	case "keyframe":
		s.b.requestKeyframe()
	case "bitrate":
		s.b.mu.Lock()
		s.b.caps[s] = m.BPS
		s.b.mu.Unlock()
	case "grant":
		// A refreshed grant from a viewer through a station: it must still
		// pass, and on the same lease, or the session ends.
		if s.check == nil || m.Grant == nil {
			return
		}
		var ev nostr.Event
		if err := json.Unmarshal(m.Grant, &ev); err != nil {
			return
		}
		grant, err := s.check(ev)
		if err != nil || grant.LeaseID != s.lease {
			s.send(Message{Type: "error", Error: "lease: refresh refused"})
			s.Close()
		}
	}
}

// sendStats posts the encoder's state on the control channel every second.
func (s *Session) sendStats() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			// Text, so a browser gets a string rather than a Blob.
			if err := s.control.SendText(string(s.b.statsMessage())); err != nil {
				return
			}
		}
	}
}

// Close ends the session; the encoder stops with the last one.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.pc.Close()
		s.b.removeSession(s)
	})
}
