package web

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/VibiumDev/roadie/internal/audio"
)

// micHID records what is played into the microphone, and when.
type micHID struct {
	keyHID
	mu     sync.Mutex
	pieces [][]byte
	at     []time.Time
	err    error
}

func (m *micHID) SendAudio(pcm []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if len(pcm) > 0 {
		m.pieces = append(m.pieces, append([]byte(nil), pcm...))
		m.at = append(m.at, time.Now())
	}
	return nil
}

func (m *micHID) total() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []byte
	for _, p := range m.pieces {
		all = append(all, p...)
	}
	return all
}

func audioServer(t *testing.T) (*Server, *micHID, *websocket.Conn) {
	t.Helper()
	resetBidiState()
	s, mux := newTestServer()
	mic := &micHID{}
	s.HID = mic
	s.AudioBroadcast = audio.NewAudioBroadcaster()
	s.AudioBroadcast.SetParams(audio.AudioParams{SampleRate: 48000, Channels: 2, Format: "f32-interleaved"})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	conn := bidiDial(t, ts)
	conn.SetReadLimit(8 << 20)
	return s, mic, conn
}

// bidiCall sends a command and returns its answer, setting aside events
// that arrive first.
func bidiCall(t *testing.T, conn *websocket.Conn, id int, method string, params any) (bidiResp, []json.RawMessage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
	var events []json.RawMessage
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("%s: read: %v", method, err)
		}
		var r bidiResp
		json.Unmarshal(raw, &r)
		if r.Type == "event" {
			events = append(events, raw)
			continue
		}
		if r.ID == id {
			return r, events
		}
	}
}

func clip(d time.Duration) string {
	n := int(d.Seconds() * 16000)
	b := make([]byte, n*2)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(8000*math.Sin(2*math.Pi*300*float64(i)/16000))))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestAudioCapabilityAndClock(t *testing.T) {
	_, _, conn := audioServer(t)
	resp, _ := bidiCall(t, conn, 1, "session.new", map[string]any{"capabilities": map[string]any{}})
	var result struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	json.Unmarshal(resp.Result, &result)
	var caps struct {
		Play, Capture bool
		SampleRate    int
		Format        string
	}
	json.Unmarshal(result.Capabilities["roadie:audio"], &caps)
	if !caps.Play || !caps.Capture || caps.SampleRate != 16000 || caps.Format != "s16le" {
		t.Fatalf("roadie:audio capability: %s", result.Capabilities["roadie:audio"])
	}

	var a, b struct{ Now float64 }
	first, _ := bidiCall(t, conn, 2, "roadie:audio.clock", nil)
	json.Unmarshal(first.Result, &a)
	time.Sleep(30 * time.Millisecond)
	second, _ := bidiCall(t, conn, 3, "roadie:audio.clock", nil)
	json.Unmarshal(second.Result, &b)
	if d := b.Now - a.Now; d < 25 || d > 200 {
		t.Fatalf("the clock moved %.1f ms across a 30 ms wait", d)
	}
}

func TestAudioPlayPacesTheClipIntoTheMicrophone(t *testing.T) {
	_, mic, conn := audioServer(t)
	bidiCall(t, conn, 1, "session.new", map[string]any{})
	data := clip(400 * time.Millisecond)
	raw, _ := base64.StdEncoding.DecodeString(data)

	var before, after struct{ Now float64 }
	c, _ := bidiCall(t, conn, 2, "roadie:audio.clock", nil)
	json.Unmarshal(c.Result, &before)
	began := time.Now()
	resp, _ := bidiCall(t, conn, 3, "roadie:audio.play", map[string]any{"format": "s16le", "sampleRate": 16000, "channels": 1, "data": data})
	took := time.Since(began)
	c, _ = bidiCall(t, conn, 4, "roadie:audio.clock", nil)
	json.Unmarshal(c.Result, &after)
	if resp.Type != "success" {
		t.Fatalf("play: %s %s", resp.Error, resp.Message)
	}
	var r struct {
		StartTime, EndTime float64
		Samples            int
	}
	json.Unmarshal(resp.Result, &r)
	if r.Samples != 6400 || math.Abs(r.EndTime-r.StartTime-400) > 0.5 {
		t.Fatalf("result %+v: want 6400 samples spanning 400 ms", r)
	}
	if r.StartTime < before.Now || r.EndTime > after.Now+5 {
		t.Fatalf("the clip's times %.1f..%.1f are not between the clock readings around it, %.1f..%.1f", r.StartTime, r.EndTime, before.Now, after.Now)
	}
	if took < 400*time.Millisecond || took > 900*time.Millisecond {
		t.Fatalf("play answered after %v; it should answer when the 400 ms clip has played out", took)
	}
	if got := mic.total(); string(got) != string(raw) {
		t.Fatalf("the microphone got %d bytes, want the clip's %d unchanged", len(got), len(raw))
	}
	mic.mu.Lock()
	defer mic.mu.Unlock()
	if len(mic.pieces) != 20 || len(mic.pieces[0]) != 640 {
		t.Fatalf("%d pieces, first of %d bytes: want twenty 20 ms pieces", len(mic.pieces), len(mic.pieces[0]))
	}
	if span := mic.at[19].Sub(mic.at[0]); span < 350*time.Millisecond || span > 600*time.Millisecond {
		t.Fatalf("the pieces went out over %v; a jitter buffer needs them at real-time pace", span)
	}
}

func TestAudioPlayRefusals(t *testing.T) {
	_, mic, conn := audioServer(t)
	short := clip(40 * time.Millisecond)
	if r, _ := bidiCall(t, conn, 1, "roadie:audio.play", map[string]any{"data": short}); r.Error != "no such session" {
		t.Fatalf("without a session: %+v", r)
	}
	bidiCall(t, conn, 2, "session.new", map[string]any{})
	for name, params := range map[string]map[string]any{
		"another rate":   {"sampleRate": 48000, "data": short},
		"two channels":   {"channels": 2, "data": short},
		"another format": {"format": "f32le", "data": short},
		"no data":        {},
		"not base64":     {"data": "!!!"},
		"too long":       {"data": base64.StdEncoding.EncodeToString(make([]byte, 61*16000*2))},
	} {
		if r, _ := bidiCall(t, conn, 3, "roadie:audio.play", params); r.Error != "invalid argument" {
			t.Errorf("%s: got %q %q, want invalid argument", name, r.Error, r.Message)
		}
	}
	mic.mu.Lock()
	mic.err = errors.New("the USB gadget has no microphone")
	mic.mu.Unlock()
	if r, _ := bidiCall(t, conn, 4, "roadie:audio.play", map[string]any{"data": short}); r.Error != "unsupported operation" {
		t.Fatalf("no microphone: %+v", r)
	}
}

func TestAudioOneClipAtATimeAndTheSocketStaysResponsive(t *testing.T) {
	_, _, conn := audioServer(t)
	bidiCall(t, conn, 1, "session.new", map[string]any{})
	ctx := context.Background()
	long, _ := json.Marshal(map[string]any{"id": 10, "method": "roadie:audio.play", "params": map[string]any{"data": clip(600 * time.Millisecond)}})
	conn.Write(ctx, websocket.MessageText, long)

	began := time.Now()
	if r, _ := bidiCall(t, conn, 11, "roadie:audio.clock", nil); r.Type != "success" || time.Since(began) > 300*time.Millisecond {
		t.Fatalf("the clock did not answer while a clip played: %+v after %v", r, time.Since(began))
	}
	if r, _ := bidiCall(t, conn, 12, "roadie:audio.play", map[string]any{"data": clip(40 * time.Millisecond)}); r.Type != "error" {
		t.Fatalf("a second clip was accepted over the first: %+v", r)
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, raw, err := conn.Read(rctx)
	var done bidiResp
	json.Unmarshal(raw, &done)
	if err != nil || done.ID != 10 || done.Type != "success" {
		t.Fatalf("the first clip's answer: %s %v", raw, err)
	}
}

// broadcast plays d of a tone out of the target at real-time pace, as the
// capture path delivers it: 48 kHz stereo float32 in 20 ms chunks.
func broadcast(ab *audio.AudioBroadcaster, d time.Duration) {
	const frames = 960
	begin := time.Now()
	for i := 0; i < int(d/(20*time.Millisecond)); i++ {
		b := make([]byte, frames*2*4)
		for f := 0; f < frames; f++ {
			v := math.Float32bits(float32(0.25 * math.Sin(2*math.Pi*300*float64(i*frames+f)/48000)))
			binary.LittleEndian.PutUint32(b[8*f:], v)
			binary.LittleEndian.PutUint32(b[8*f+4:], v)
		}
		time.Sleep(time.Until(begin.Add(time.Duration(i+1) * 20 * time.Millisecond)))
		ab.Broadcast(b)
	}
}

func TestAudioCaptureStreamsStampedChunks(t *testing.T) {
	s, _, conn := audioServer(t)
	if r, _ := bidiCall(t, conn, 1, "roadie:audio.capture.start", nil); r.Error != "no such session" {
		t.Fatalf("without a session: %+v", r)
	}
	bidiCall(t, conn, 2, "session.new", map[string]any{})
	resp, _ := bidiCall(t, conn, 3, "roadie:audio.capture.start", map[string]any{"chunkMs": 100})
	var started struct {
		Capture    string
		SampleRate int
		StartTime  float64
	}
	json.Unmarshal(resp.Result, &started)
	if resp.Type != "success" || started.Capture == "" || started.SampleRate != 16000 {
		t.Fatalf("capture.start: %+v %s", resp, resp.Result)
	}

	go broadcast(s.AudioBroadcast, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type chunk struct {
		Capture string
		Sample  int
		Time    float64
		Data    string
	}
	var chunks []chunk
	var energy float64
	for len(chunks) < 9 {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("after %d chunks: %v", len(chunks), err)
		}
		var ev struct {
			Type, Method string
			Params       chunk
		}
		json.Unmarshal(raw, &ev)
		if ev.Type != "event" || ev.Method != "roadie:audio.chunk" {
			t.Fatalf("unexpected message %s", raw)
		}
		pcm, _ := base64.StdEncoding.DecodeString(ev.Params.Data)
		if len(pcm) != 3200 {
			t.Fatalf("a chunk of %d bytes, want 100 ms of 16 kHz mono", len(pcm))
		}
		for i := 0; i+1 < len(pcm); i += 2 {
			v := float64(int16(binary.LittleEndian.Uint16(pcm[i:]))) / 32768
			energy += v * v
		}
		chunks = append(chunks, ev.Params)
	}
	for i, c := range chunks {
		if c.Capture != started.Capture || c.Sample != i*1600 {
			t.Fatalf("chunk %d: %+v", i, c)
		}
		if i > 0 {
			if step := c.Time - chunks[i-1].Time; step < 60 || step > 140 {
				t.Fatalf("chunk %d is stamped %.1f ms after the one before; chunks are 100 ms", i, step)
			}
		}
	}
	if chunks[0].Time < started.StartTime-150 || chunks[0].Time > started.StartTime+400 {
		t.Fatalf("the first chunk is stamped %.1f, the capture started at %.1f", chunks[0].Time, started.StartTime)
	}
	if rms := math.Sqrt(energy / float64(9*1600)); rms < 0.12 || rms > 0.24 {
		t.Fatalf("level %.3f: a 0.25 tone downmixed to mono should be near 0.18", rms)
	}

	if r, _ := bidiCall(t, conn, 4, "roadie:audio.capture.stop", map[string]any{"capture": "nope"}); r.Error != "invalid argument" {
		t.Fatalf("stopping a capture that is not running: %+v", r)
	}
	if r, _ := bidiCall(t, conn, 5, "roadie:audio.capture.stop", map[string]any{"capture": started.Capture}); r.Type != "success" {
		t.Fatalf("capture.stop: %+v", r)
	}
	go broadcast(s.AudioBroadcast, 300*time.Millisecond)
	if _, events := bidiCall(t, conn, 6, "roadie:audio.clock", nil); len(events) != 0 {
		t.Fatalf("%d chunks arrived after the capture was stopped", len(events))
	}
	if n := s.AudioBroadcast.Listeners(); n != 0 {
		t.Fatalf("%d listeners left on the audio source", n)
	}
}

func TestAudioCaptureNeedsAudio(t *testing.T) {
	s, _, conn := audioServer(t)
	s.AudioBroadcast.SetInactive()
	resp, _ := bidiCall(t, conn, 1, "session.new", map[string]any{})
	var result struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	json.Unmarshal(resp.Result, &result)
	var caps struct{ Capture bool }
	json.Unmarshal(result.Capabilities["roadie:audio"], &caps)
	if caps.Capture {
		t.Fatal("capture was announced with no audio coming from the target")
	}
	if r, _ := bidiCall(t, conn, 2, "roadie:audio.capture.start", nil); r.Error != "unsupported operation" {
		t.Fatalf("%+v", r)
	}
}

func TestAudioCaptureEndsWithTheConnection(t *testing.T) {
	s, _, conn := audioServer(t)
	bidiCall(t, conn, 1, "session.new", map[string]any{})
	bidiCall(t, conn, 2, "roadie:audio.capture.start", nil)
	deadline := time.Now().Add(2 * time.Second)
	for s.AudioBroadcast.Listeners() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	conn.Close(websocket.StatusNormalClosure, "")
	for s.AudioBroadcast.Listeners() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := s.AudioBroadcast.Listeners(); n != 0 {
		t.Fatalf("%d listeners left after the client went away", n)
	}
}
