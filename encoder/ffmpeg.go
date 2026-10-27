package encoder

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// FFmpeg encodes through an ffmpeg subprocess: raw NV12 frames go in on
// its stdin and Annex-B H.264 comes out of its stdout. It is the encoder
// for hosts other than the Pi 4, and the Pi's fallback. ffmpeg cannot
// change bitrate or force a keyframe while running, so both restart the
// process, which costs a moment of video; they are rate-limited for that.
type FFmpeg struct {
	cfg   Config
	codec string

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	bitrate  int
	stamps   []time.Duration // of frames written and not yet encoded
	restart  time.Time       // when the process last (re)started
	frame    []byte
	closed   bool
	dropped  int
	samples  chan Sample
	quit     chan struct{} // closed by Close, so a blocked reader lets go
	readDone chan struct{}
}

// Codecs ffmpeg can be asked for, in the order PickFFmpegCodec prefers
// them on each host.
var ffmpegCodecs = map[string][]string{
	"darwin": {"h264_videotoolbox", "libx264"},
	"linux":  {"h264_v4l2m2m", "h264_vaapi", "h264_nvenc", "libx264"},
}

// PickFFmpegCodec chooses the best H.264 encoder this host's ffmpeg has,
// checking for the device each hardware one needs.
func PickFFmpegCodec() (string, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", fmt.Errorf("ffmpeg not found: %w", ErrUnavailable)
	}
	for _, c := range ffmpegCodecs[runtime.GOOS] {
		if !ffmpegHas(c) {
			continue
		}
		switch c {
		case "h264_v4l2m2m":
			if _, err := os.Stat("/dev/video11"); err != nil {
				continue
			}
		case "h264_vaapi":
			if _, err := os.Stat("/dev/dri/renderD128"); err != nil {
				continue
			}
		case "h264_nvenc":
			if _, err := os.Stat("/dev/nvidia0"); err != nil {
				continue
			}
		}
		return c, nil
	}
	if ffmpegHas("libx264") {
		return "libx264", nil
	}
	return "", fmt.Errorf("ffmpeg has no H.264 encoder: %w", ErrUnavailable)
}

var (
	ffmpegEncodersOnce sync.Once
	ffmpegEncoders     map[string]bool
)

// ffmpegHas says whether ffmpeg lists an encoder.
func ffmpegHas(codec string) bool {
	ffmpegEncodersOnce.Do(func() {
		ffmpegEncoders = map[string]bool{}
		out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && strings.HasPrefix(f[0], "V") {
				ffmpegEncoders[f[1]] = true
			}
		}
	})
	return ffmpegEncoders[codec]
}

// NewFFmpeg starts ffmpeg with the given codec.
func NewFFmpeg(cfg Config, codec string) (*FFmpeg, error) {
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("ffmpeg encoder: bad size %dx%d", cfg.Width, cfg.Height)
	}
	if cfg.FPS <= 0 {
		cfg.FPS = 30
	}
	if cfg.Bitrate <= 0 {
		cfg.Bitrate = 2_000_000
	}
	if !ffmpegHas(codec) {
		return nil, fmt.Errorf("ffmpeg has no encoder %q: %w", codec, ErrUnavailable)
	}
	e := &FFmpeg{
		cfg:     cfg,
		codec:   codec,
		bitrate: cfg.Bitrate,
		frame:   make([]byte, NV12Size(cfg.Width, cfg.Height)),
		samples: make(chan Sample, 16),
		quit:    make(chan struct{}),
	}
	if err := e.start(); err != nil {
		return nil, err
	}
	return e, nil
}

// args builds the command line for the codec. Every codec is asked for
// Baseline, no B-frames and headers on each keyframe where it can be.
func (e *FFmpeg) args() []string {
	w, h, fps := e.cfg.Width, e.cfg.Height, e.cfg.FPS
	gop := fmt.Sprint(e.cfg.GOPFrames())
	br := fmt.Sprint(e.bitrate)
	a := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if e.codec == "h264_vaapi" {
		a = append(a, "-vaapi_device", "/dev/dri/renderD128")
	}
	a = append(a,
		"-f", "rawvideo", "-pix_fmt", "nv12", "-s", fmt.Sprintf("%dx%d", w, h), "-r", fmt.Sprint(fps),
		"-i", "pipe:0", "-an", "-fps_mode", "passthrough")
	switch e.codec {
	case "libx264":
		a = append(a, "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
			"-profile:v", "baseline", "-level", "4.0", "-b:v", br, "-maxrate", br, "-bufsize", fmt.Sprint(e.bitrate/2),
			"-x264-params", "repeat-headers=1:keyint="+gop+":min-keyint="+gop+":scenecut=0:bframes=0")
	case "h264_videotoolbox":
		a = append(a, "-c:v", "h264_videotoolbox", "-realtime", "1", "-allow_sw", "1",
			"-profile:v", "baseline", "-b:v", br, "-g", gop, "-bf", "0",
			"-bsf:v", "dump_extra=freq=keyframe")
	case "h264_vaapi":
		a = append(a, "-vf", "format=nv12,hwupload", "-c:v", "h264_vaapi",
			"-profile:v", "constrained_baseline", "-b:v", br, "-g", gop, "-bf", "0",
			"-bsf:v", "dump_extra=freq=keyframe")
	case "h264_nvenc":
		a = append(a, "-c:v", "h264_nvenc", "-preset", "p1", "-tune", "ll", "-zerolatency", "1",
			"-profile:v", "baseline", "-b:v", br, "-g", gop, "-bf", "0",
			"-bsf:v", "dump_extra=freq=keyframe")
	case "h264_qsv":
		a = append(a, "-c:v", "h264_qsv", "-preset", "veryfast",
			"-profile:v", "baseline", "-b:v", br, "-g", gop, "-bf", "0",
			"-bsf:v", "dump_extra=freq=keyframe")
	default:
		// h264_v4l2m2m and anything else: the generic options only.
		a = append(a, "-c:v", e.codec, "-b:v", br, "-g", gop, "-bf", "0",
			"-bsf:v", "dump_extra=freq=keyframe")
	}
	return append(a, "-f", "h264", "pipe:1")
}

// start launches the process; the caller holds no lock or e.mu.
func (e *FFmpeg) start() error {
	cmd := exec.Command("ffmpeg", e.args()...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	e.cmd, e.stdin = cmd, stdin
	e.stamps = e.stamps[:0]
	e.restart = time.Now()
	e.readDone = make(chan struct{})
	go e.read(stdout, e.readDone)
	return nil
}

// read turns the process's output into samples until it ends.
func (e *FFmpeg) read(r io.Reader, done chan struct{}) {
	defer close(done)
	split := &AUSplitter{Emit: func(au []byte) {
		_, _, idr := NALTypes(au)
		e.mu.Lock()
		var ts time.Duration
		if len(e.stamps) > 0 {
			ts = e.stamps[0]
			e.stamps = e.stamps[1:]
		}
		e.mu.Unlock()
		select {
		case e.samples <- Sample{Data: au, Keyframe: idr, Timestamp: ts}:
		case <-e.quit:
		}
	}}
	br := bufio.NewReaderSize(r, 256<<10)
	buf := make([]byte, 64<<10)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			split.Write(buf[:n])
		}
		if err != nil {
			split.Flush()
			return
		}
	}
}

// Encode writes a frame to the process.
func (e *FFmpeg) Encode(f Frame) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return errors.New("encoder closed")
	}
	if f.Rect.Dx() != e.cfg.Width || f.Rect.Dy() != e.cfg.Height {
		return fmt.Errorf("frame is %dx%d, encoder wants %dx%d", f.Rect.Dx(), f.Rect.Dy(), e.cfg.Width, e.cfg.Height)
	}
	if len(e.stamps) > 8 {
		// ffmpeg is behind; the pipe would block the capture loop.
		e.dropped++
		return nil
	}
	NV12(e.frame, e.cfg.Width, e.cfg.Width*e.cfg.Height, f.Img, f.Rect)
	if _, err := e.stdin.Write(e.frame); err != nil {
		return fmt.Errorf("ffmpeg stdin: %w", err)
	}
	e.stamps = append(e.stamps, f.Timestamp)
	return nil
}

// Samples delivers access units.
func (e *FFmpeg) Samples() <-chan Sample { return e.samples }

// Dropped is how many frames were skipped because ffmpeg was behind.
func (e *FFmpeg) Dropped() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dropped
}

// SetBitrate restarts ffmpeg at a new bitrate when the change is worth the
// interruption: a quarter or more, and not within three seconds of the
// last restart.
func (e *FFmpeg) SetBitrate(bps int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || bps <= 0 {
		return nil
	}
	diff := bps - e.bitrate
	if diff < 0 {
		diff = -diff
	}
	if diff*4 < e.bitrate || time.Since(e.restart) < 3*time.Second {
		return nil
	}
	e.bitrate = bps
	return e.relaunch()
}

// ForceKeyframe restarts ffmpeg, whose first frame is an IDR, at most once
// every two seconds.
func (e *FFmpeg) ForceKeyframe() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || time.Since(e.restart) < 2*time.Second {
		return nil
	}
	return e.relaunch()
}

// relaunch replaces the process; the caller holds e.mu.
func (e *FFmpeg) relaunch() error {
	e.stop()
	return e.start()
}

// stop ends the process; the caller holds e.mu.
func (e *FFmpeg) stop() {
	if e.cmd == nil {
		return
	}
	e.stdin.Close()
	done := e.readDone
	e.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		e.cmd.Process.Kill()
		<-done
	}
	e.cmd.Wait()
	e.mu.Lock()
	e.cmd = nil
}

// Close ends the process and closes Samples.
func (e *FFmpeg) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	close(e.quit)
	e.stop()
	e.mu.Unlock()
	close(e.samples)
	return nil
}
