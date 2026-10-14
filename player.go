package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Playback pacing mirrors the browser's voice-input cadence: the HID board
// buffers only ~100 ms of audio ahead, so chunks must arrive at real-time
// pace or the ring buffer overwrites unplayed samples.
const (
	playRate       = 16000
	playChunkMS    = 20
	playChunkBytes = playRate * 2 * playChunkMS / 1000 // 320 samples
)

// AudioPlayer paces synthetic PCM (TTS, sound files) into the HID board's
// USB microphone. At most one playback runs at a time; starting a new one
// preempts the current one. It does not coordinate with live /audio-in mic
// sessions — speaking while a viewer holds the mic button interleaves both
// streams on the wire.
type AudioPlayer struct {
	hid *HIDController

	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	label     string
	sentBytes int64
	totalMS   int64
}

func NewAudioPlayer(hid *HIDController) *AudioPlayer {
	return &AudioPlayer{hid: hid}
}

// PlaybackStatus reports what the player is doing, for /audio-in/status.
// DurationMS is 0 when unknown (TTS streams as it synthesizes).
type PlaybackStatus struct {
	Playing    bool   `json:"playing"`
	Source     string `json:"source,omitempty"`
	PositionMS int64  `json:"position_ms"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// Status returns the current playback state.
func (p *AudioPlayer) Status() PlaybackStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel == nil {
		return PlaybackStatus{}
	}
	return PlaybackStatus{
		Playing:    true,
		Source:     p.label,
		PositionMS: p.sentBytes * 1000 / (playRate * 2),
		DurationMS: p.totalMS,
	}
}

// Play opens a PCM source bound to the run's context and sends it to the HID
// mic in real-time 20 ms chunks. open must return 16 kHz mono S16LE PCM and
// spawn any subprocesses with the given context, so that preemption kills
// them and unblocks reads. label and durationMS (0 when unknown) feed
// Status. Blocks until the stream ends, fails, or is preempted by another
// Play or Stop.
func (p *AudioPlayer) Play(label string, durationMS int64, open func(ctx context.Context) (io.ReadCloser, func() error, error)) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})

	p.mu.Lock()
	for p.cancel != nil {
		p.cancel()
		prev := p.done
		p.mu.Unlock()
		<-prev
		p.mu.Lock()
	}
	p.cancel, p.done = cancel, done
	p.label, p.sentBytes, p.totalMS = label, 0, durationMS
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		if p.done == done {
			p.cancel, p.done = nil, nil
			p.label, p.sentBytes, p.totalMS = "", 0, 0
		}
		p.mu.Unlock()
		close(done)
	}()

	r, cleanup, err := open(ctx)
	if err != nil {
		return err
	}
	defer func() {
		r.Close()
		if cleanup != nil {
			cleanup()
		}
	}()

	buf := make([]byte, playChunkBytes)
	ticker := time.NewTicker(playChunkMS * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			if serr := p.hid.SendAudio(buf[:n]); serr != nil {
				return serr
			}
			p.mu.Lock()
			if p.done == done {
				p.sentBytes += int64(n)
			}
			p.mu.Unlock()
		}
		switch err {
		case nil:
		case io.EOF, io.ErrUnexpectedEOF:
			return nil
		default:
			return err
		}
	}
}

// Stop cancels any in-flight playback. No-op when idle.
func (p *AudioPlayer) Stop() {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
}

// ffmpegToPCM decodes an ffmpeg input (a file path, or "pipe:0" fed by
// stdin) to 16 kHz mono S16LE PCM on stdout. The returned cleanup reaps the
// process and surfaces its stderr on failure.
func ffmpegToPCM(ctx context.Context, stdin io.Reader, input string) (io.ReadCloser, func() error, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-i", input,
		"-f", "s16le", "-ar", "16000", "-ac", "1", "pipe:1")
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	cleanup := func() error {
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
	return out, cleanup, nil
}

// fileToPCM decodes any ffmpeg-readable audio file to 16 kHz mono S16LE PCM.
func fileToPCM(ctx context.Context, path string) (io.ReadCloser, func() error, error) {
	return ffmpegToPCM(ctx, nil, path)
}

// fileDurationMS probes a media file's duration with ffprobe. Returns 0 when
// the probe fails — the UI then shows progress without a total.
func fileDurationMS(path string) int64 {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return int64(secs * 1000)
}
