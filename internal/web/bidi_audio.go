package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/VibiumDev/roadie/audio/pcm"
	"github.com/VibiumDev/roadie/internal/audio"
)

// The roadie:audio BiDi extension: a raw audio pipe on the session socket.
// play sends a clip into the target's microphone, capture streams what the
// target plays, and clock reads the clock both are stamped with. Roadie
// knows nothing about speech; the client brings and takes PCM.
//
// All audio is 16 kHz mono 16-bit little-endian, the format the microphone
// path already takes. Times are milliseconds on audioNow's clock.

const (
	bidiAudioRate = 16000
	// bidiAudioMaxClip bounds one play command.
	bidiAudioMaxClip = 60 * time.Second
	// A clip goes to the microphone in pieces this long, at real-time pace:
	// the microphone's jitter buffer drops audio that arrives faster.
	bidiAudioPiece = 20 * time.Millisecond
	// The microphone starts playing once this much is queued (alsa's
	// micPrefill), so a clip's first sample leaves this long after the
	// first piece is written.
	bidiAudioPrefill = 40 * time.Millisecond
	// bidiReadLimit admits a 60 s clip as base64 in one message.
	bidiReadLimit = 4 << 20
)

var bidiAudioEpoch = time.Now()

// audioNow is Roadie's monotonic clock in milliseconds.
func audioNow() float64 { return float64(time.Since(bidiAudioEpoch)) / float64(time.Millisecond) }

func audioAt(t time.Time) float64 { return float64(t.Sub(bidiAudioEpoch)) / float64(time.Millisecond) }

// bidiAudio is one connection's audio state.
type bidiAudio struct {
	mu      sync.Mutex
	playing bool
	nextID  int
	capture string // id of the running capture, "" for none
	stop    context.CancelFunc
}

// close ends what the connection had running.
func (a *bidiAudio) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stop != nil {
		a.stop()
		a.stop, a.capture = nil, ""
	}
}

// audioCaps is what a session announces about audio.
func (s *Server) audioCaps() map[string]any {
	return map[string]any{
		"play":       s.HID != nil && s.HID.SendAudio(nil) == nil,
		"capture":    s.AudioBroadcast != nil && s.AudioBroadcast.IsActive(),
		"sampleRate": bidiAudioRate,
		"channels":   1,
		"format":     "s16le",
	}
}

func bidiFail(conn *websocket.Conn, ctx context.Context, id int, code, format string, args ...any) {
	writeJSON(conn, ctx, bidiErrorResp{Type: "error", ID: id, Error: code, Message: fmt.Sprintf(format, args...)})
}

func bidiHasSession() bool {
	bidiSessionMu.Lock()
	defer bidiSessionMu.Unlock()
	return bidiActive != nil
}

type bidiAudioFormat struct {
	Format     string `json:"format"`
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels"`
}

func (f bidiAudioFormat) check() error {
	if (f.Format != "" && f.Format != "s16le") || (f.SampleRate != 0 && f.SampleRate != bidiAudioRate) || (f.Channels != 0 && f.Channels != 1) {
		return fmt.Errorf("audio is s16le, %d Hz, 1 channel", bidiAudioRate)
	}
	return nil
}

func (s *Server) bidiAudioClock(conn *websocket.Conn, ctx context.Context, cmd bidiCommand) {
	writeJSON(conn, ctx, bidiResult{Type: "success", ID: cmd.ID, Result: map[string]any{"now": audioNow()}})
}

// roadie:audio.play sends a clip into the target's microphone at real-time
// pace and answers when the clip has played out.
func (s *Server) bidiAudioPlay(conn *websocket.Conn, ctx context.Context, cmd bidiCommand, a *bidiAudio) {
	var p struct {
		bidiAudioFormat
		Data string `json:"data"`
	}
	if err := json.Unmarshal(cmd.Params, &p); err != nil {
		bidiFail(conn, ctx, cmd.ID, "invalid argument", "invalid params: %v", err)
		return
	}
	if err := p.check(); err != nil {
		bidiFail(conn, ctx, cmd.ID, "invalid argument", "%v", err)
		return
	}
	clip, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil || len(clip) < 2 {
		bidiFail(conn, ctx, cmd.ID, "invalid argument", "data is base64 PCM, and must not be empty")
		return
	}
	clip = clip[:len(clip)-len(clip)%2]
	if max := int(bidiAudioMaxClip.Seconds()) * bidiAudioRate * 2; len(clip) > max {
		bidiFail(conn, ctx, cmd.ID, "invalid argument", "a clip is at most %s", bidiAudioMaxClip)
		return
	}
	if !bidiHasSession() {
		bidiFail(conn, ctx, cmd.ID, "no such session", "no active session")
		return
	}
	if s.HID == nil {
		bidiFail(conn, ctx, cmd.ID, "unsupported operation", "no input backend, so no microphone")
		return
	}
	if err := s.HID.SendAudio(nil); err != nil {
		bidiFail(conn, ctx, cmd.ID, "unsupported operation", "%v", err)
		return
	}
	a.mu.Lock()
	if a.playing {
		a.mu.Unlock()
		bidiFail(conn, ctx, cmd.ID, "unknown error", "a clip is already playing")
		return
	}
	a.playing = true
	a.mu.Unlock()

	// The clip plays on its own goroutine, so the socket still answers
	// clock and capture commands while it does.
	go func() {
		defer func() {
			a.mu.Lock()
			a.playing = false
			a.mu.Unlock()
		}()
		piece := int(bidiAudioPiece.Seconds()*bidiAudioRate) * 2
		begin := time.Now()
		for off, i := 0, 0; off < len(clip); off, i = off+piece, i+1 {
			if wait := time.Until(begin.Add(time.Duration(i) * bidiAudioPiece)); wait > 0 {
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return
				}
			}
			if err := s.HID.SendAudio(clip[off:min(off+piece, len(clip))]); err != nil {
				bidiFail(conn, ctx, cmd.ID, "unknown error", "%v", err)
				return
			}
		}
		samples := len(clip) / 2
		start := begin.Add(bidiAudioPrefill)
		end := start.Add(time.Duration(samples) * time.Second / bidiAudioRate)
		select {
		case <-time.After(time.Until(end)):
		case <-ctx.Done():
			return
		}
		writeJSON(conn, ctx, bidiResult{Type: "success", ID: cmd.ID, Result: map[string]any{
			"startTime": audioAt(start), "endTime": audioAt(end), "samples": samples,
		}})
	}()
}

// roadie:audio.capture.start streams what the target plays as
// roadie:audio.chunk events, each stamped with when its first sample was
// captured.
func (s *Server) bidiAudioCaptureStart(conn *websocket.Conn, ctx context.Context, cmd bidiCommand, a *bidiAudio) {
	var p struct {
		bidiAudioFormat
		ChunkMs int `json:"chunkMs"`
	}
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			bidiFail(conn, ctx, cmd.ID, "invalid argument", "invalid params: %v", err)
			return
		}
	}
	if err := p.check(); err != nil {
		bidiFail(conn, ctx, cmd.ID, "invalid argument", "%v", err)
		return
	}
	if p.ChunkMs == 0 {
		p.ChunkMs = 100
	}
	if p.ChunkMs < 20 || p.ChunkMs > 1000 {
		bidiFail(conn, ctx, cmd.ID, "invalid argument", "chunkMs is 20 to 1000")
		return
	}
	if !bidiHasSession() {
		bidiFail(conn, ctx, cmd.ID, "no such session", "no active session")
		return
	}
	if s.AudioBroadcast == nil || !s.AudioBroadcast.IsActive() {
		bidiFail(conn, ctx, cmd.ID, "unsupported operation", "no audio is coming from the target")
		return
	}
	a.mu.Lock()
	if a.stop != nil {
		a.stop()
	}
	a.nextID++
	id := fmt.Sprintf("c%d", a.nextID)
	cctx, cancel := context.WithCancel(ctx)
	a.capture, a.stop = id, cancel
	a.mu.Unlock()

	start := audioNow()
	writeJSON(conn, ctx, bidiResult{Type: "success", ID: cmd.ID, Result: map[string]any{
		"capture": id, "sampleRate": bidiAudioRate, "channels": 1, "format": "s16le", "startTime": start,
	}})
	go s.bidiAudioCapture(conn, cctx, id, p.ChunkMs)
}

func (s *Server) bidiAudioCapture(conn *websocket.Conn, ctx context.Context, id string, chunkMs int) {
	want := bidiAudioRate * chunkMs / 1000
	var pending []int16
	var scratch []int16
	var out []byte
	sent := 0
	for ctx.Err() == nil {
		params := s.AudioBroadcast.Params()
		ch, unsub := s.AudioBroadcast.Subscribe()
		down := newCaptureDownsampler(params)
		closed := false
		for !closed && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case chunk, ok := <-ch:
				if !ok {
					closed = true
					break
				}
				arrived := time.Now()
				scratch = down.Process(scratch[:0], pcm.Float32LE(chunk))
				pending = append(pending, scratch...)
				for len(pending) >= want {
					// The newest sample was captured about now; the piece's
					// first sample that much audio earlier.
					first := arrived.Add(-time.Duration(len(pending)) * time.Second / bidiAudioRate)
					out = pcm.Int16LE(out[:0], pending[:want])
					writeJSON(conn, ctx, map[string]any{"type": "event", "method": "roadie:audio.chunk", "params": map[string]any{
						"capture": id, "sample": sent, "time": audioAt(first), "data": base64.StdEncoding.EncodeToString(out),
					}})
					sent += want
					pending = pending[want:]
				}
			}
		}
		unsub()
		// The source changed format and dropped its listeners. Pick it up
		// again; the chunk times show the gap.
		pending = pending[:0]
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
		if !s.AudioBroadcast.IsActive() {
			return
		}
	}
}

func newCaptureDownsampler(p audio.AudioParams) *pcm.Downsampler {
	return pcm.NewDownsampler(p.SampleRate, bidiAudioRate, p.Channels)
}

func (s *Server) bidiAudioCaptureStop(conn *websocket.Conn, ctx context.Context, cmd bidiCommand, a *bidiAudio) {
	var p struct {
		Capture string `json:"capture"`
	}
	json.Unmarshal(cmd.Params, &p)
	a.mu.Lock()
	if a.stop == nil || (p.Capture != "" && p.Capture != a.capture) {
		a.mu.Unlock()
		bidiFail(conn, ctx, cmd.ID, "invalid argument", "no such capture")
		return
	}
	a.stop()
	a.stop, a.capture = nil, ""
	a.mu.Unlock()
	writeJSON(conn, ctx, bidiResult{Type: "success", ID: cmd.ID, Result: map[string]any{}})
}
