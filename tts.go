package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// PiperDaemon keeps a piper TTS process warm so each utterance pays only
// inference time, not the ~1.3s model load. Piper runs in --output_dir mode:
// one line of text in, one "<path>.wav" line out, which frames utterances
// cleanly. Synth calls are serialized; a cancelled synth kills the process
// (the pending output line would otherwise desync the stream) and the next
// call restarts it.
type PiperDaemon struct {
	bin, model string

	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Reader
	dir   string
}

func NewPiperDaemon(bin, model string) *PiperDaemon {
	return &PiperDaemon{bin: bin, model: model}
}

// Warm starts the daemon in the background so the first say is fast too.
func (p *PiperDaemon) Warm() {
	go func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.ensureLocked()
	}()
}

func (p *PiperDaemon) ensureLocked() error {
	if p.cmd != nil && (p.cmd.ProcessState == nil || !p.cmd.ProcessState.Exited()) {
		return nil
	}
	dir := filepath.Join(os.TempDir(), "roadie-piper")
	os.RemoveAll(dir) // drop stale WAVs from a killed daemon
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cmd := exec.Command(p.bin, "--model", p.model, "--output_dir", dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap whenever it exits
	p.cmd, p.stdin, p.out, p.dir = cmd, stdin, bufio.NewReader(stdout), dir
	return nil
}

// Shutdown kills the daemon process. Piper does not exit on stdin EOF, so
// an explicit kill is needed or it outlives the server.
func (p *PiperDaemon) Shutdown() {
	p.mu.Lock()
	p.killLocked()
	p.mu.Unlock()
}

func (p *PiperDaemon) killLocked() {
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	p.cmd, p.stdin, p.out = nil, nil, nil
}

// sentenceEnd matches sentence boundaries so long text can stream: piper
// synthesizes one line at a time, and splitting lets the first sentence
// play while the rest are still being synthesized.
var sentenceEnd = regexp.MustCompile(`[.!?]+(\s+|$)`)

// splitSentences splits text at line breaks and sentence punctuation.
// Both are boundaries the writer chose, so seams — where piper's prosody
// resets and any buffering pause appears — land only where a pause
// belongs. Line breaks matter as much as periods: song lyrics and poems
// often carry no sentence punctuation at all, and without them a whole
// song becomes one giant utterance to synthesize before anything plays.
// The cost model stays honest and predictable: audio starts after the
// first chunk finishes synthesizing (~0.75x its spoken length on the Pi).
func splitSentences(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		last := 0
		for _, m := range sentenceEnd.FindAllStringIndex(line, -1) {
			if s := strings.TrimSpace(line[last:m[1]]); s != "" {
				out = append(out, s)
			}
			last = m[1]
		}
		if s := strings.TrimSpace(line[last:]); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []string{strings.TrimSpace(text)}
	}
	return out
}

// readPathLocked reads one "<path>.wav" line from the daemon. The caller
// holds p.mu. On cancellation, timeout, or read error the daemon is killed —
// a pending output line would desync every later synthesis.
func (p *PiperDaemon) readPathLocked(ctx context.Context) (string, error) {
	type result struct {
		path string
		err  error
	}
	ch := make(chan result, 1)
	go func(out *bufio.Reader) {
		path, err := out.ReadString('\n')
		ch <- result{strings.TrimSpace(path), err}
	}(p.out)
	select {
	case r := <-ch:
		if r.err != nil {
			p.killLocked()
			return "", fmt.Errorf("piper read: %w", r.err)
		}
		return r.path, nil
	case <-ctx.Done():
		p.killLocked()
		return "", ctx.Err()
	case <-time.After(120 * time.Second):
		p.killLocked()
		return "", fmt.Errorf("piper synth timed out")
	}
}

// SynthStream queues the text sentence-by-sentence and returns a stream of
// 16 kHz mono S16LE PCM that begins as soon as the first sentence is
// synthesized. p.mu stays held until every queued sentence is consumed, so
// concurrent says cannot interleave their output lines.
func (p *PiperDaemon) SynthStream(ctx context.Context, text string) (io.ReadCloser, func() error, error) {
	sentences := splitSentences(text)
	p.mu.Lock()
	if err := p.ensureLocked(); err != nil {
		p.mu.Unlock()
		return nil, nil, err
	}
	for _, s := range sentences {
		if _, err := io.WriteString(p.stdin, s+"\n"); err != nil {
			p.killLocked()
			p.mu.Unlock()
			return nil, nil, fmt.Errorf("piper write: %w", err)
		}
	}
	pr, pw := io.Pipe()
	go func() {
		defer p.mu.Unlock()
		for range sentences {
			wav, err := p.readPathLocked(ctx)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			rc, cleanup, err := fileToPCM(ctx, wav)
			if err != nil {
				os.Remove(wav)
				p.killLocked()
				pw.CloseWithError(err)
				return
			}
			_, cerr := io.Copy(pw, rc)
			rc.Close()
			cleanup()
			os.Remove(wav)
			if cerr != nil || ctx.Err() != nil {
				// Playback stopped. Queued sentences would leave unread
				// output lines behind, so restart rather than drain.
				p.killLocked()
				pw.CloseWithError(cerr)
				return
			}
		}
		pw.Close()
	}()
	return pr, func() error { pr.Close(); return nil }, nil
}

// ttsArgv expands the TTS command template into an argv. {{text}} and
// {{voice}} are replaced as whole arguments — the text is never passed
// through a shell, so it cannot inject flags or commands. When the template
// has no {{text}} token the engine reads the text from stdin instead (piper
// style); textOnStdin reports which mode applies.
func ttsArgv(tmpl, text, voice string) (argv []string, textOnStdin bool, err error) {
	if voice == "" {
		voice = "en"
	}
	fields := strings.Fields(tmpl)
	if len(fields) == 0 {
		return nil, false, fmt.Errorf("empty tts command")
	}
	argv = make([]string, 0, len(fields))
	sawText := false
	for _, f := range fields {
		switch f {
		case "{{text}}":
			argv = append(argv, text)
			sawText = true
		case "{{voice}}":
			argv = append(argv, voice)
		default:
			argv = append(argv, f)
		}
	}
	return argv, !sawText, nil
}

// synthToPCM turns text into 16 kHz mono S16LE PCM. With a warm Piper
// daemon it synthesizes there and decodes the resulting WAV; otherwise it
// runs the --tts-cmd template (espeak-ng by default, emitting WAV on
// stdout) piped through ffmpeg. Processes are bound to ctx so cancellation
// kills them.
func (s *Server) synthToPCM(ctx context.Context, text, voice string) (io.ReadCloser, func() error, error) {
	if s.Piper != nil {
		return s.Piper.SynthStream(ctx, text)
	}
	argv, textOnStdin, err := ttsArgv(s.TTSCmd, text, voice)
	if err != nil {
		return nil, nil, err
	}
	tts := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if textOnStdin {
		tts.Stdin = strings.NewReader(text + "\n")
	}
	var ttsErr bytes.Buffer
	tts.Stderr = &ttsErr
	wav, err := tts.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := tts.Start(); err != nil {
		return nil, nil, err
	}
	out, ffCleanup, err := ffmpegToPCM(ctx, wav, "pipe:0")
	if err != nil {
		tts.Process.Kill()
		tts.Wait()
		return nil, nil, err
	}
	cleanup := func() error {
		ffErr := ffCleanup()
		if err := tts.Wait(); err != nil && ctx.Err() == nil {
			return fmt.Errorf("tts: %w: %s", err, strings.TrimSpace(ttsErr.String()))
		}
		return ffErr
	}
	return out, cleanup, nil
}
