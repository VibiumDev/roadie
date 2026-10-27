// Package encoder turns captured frames into H.264 for WebRTC.
//
// An Encoder takes the cropped picture of each frame and emits Annex-B
// access units. Three implementations sit behind Open: the Pi 4's hardware
// encoder driven directly through V4L2 (v4l2m2m), and an ffmpeg subprocess
// using either that same device or whatever the host has (VideoToolbox,
// VAAPI, NVENC, Quick Sync, or libx264 as the fallback that always works).
package encoder

import (
	"errors"
	"fmt"
	"image"
	"strings"
	"time"
)

// Frame is one captured picture. Only Rect, the crop, is encoded; its size
// must match the encoder's configuration.
type Frame struct {
	Img       image.Image
	Rect      image.Rectangle
	Timestamp time.Duration // monotonic; copied to the matching Sample
}

// Sample is one encoded access unit in Annex-B byte-stream form, SPS and
// PPS included on keyframes.
type Sample struct {
	Data      []byte
	Keyframe  bool
	Timestamp time.Duration
}

// Config is what an encoder is opened with. Size changes mean a new
// encoder; bitrate changes do not.
type Config struct {
	Width, Height int
	FPS           int
	Bitrate       int           // bits per second to start with
	GOP           time.Duration // keyframe interval; 2 s if zero
	Device        string        // v4l2m2m: the encoder node, /dev/video11 if empty
}

// Encoder is one running encoder.
type Encoder interface {
	// Encode submits a frame without waiting for its result. A frame is
	// skipped, not queued, when the encoder is still busy with earlier ones.
	Encode(f Frame) error
	// Samples delivers encoded access units in order. It closes after Close.
	Samples() <-chan Sample
	// SetBitrate changes the target bitrate while running.
	SetBitrate(bps int) error
	// ForceKeyframe makes the next frame an IDR, with SPS and PPS.
	ForceKeyframe() error
	// Dropped is how many frames Encode skipped so far.
	Dropped() int
	Close() error
}

// ErrUnavailable means this kind of encoder cannot run on this host.
var ErrUnavailable = errors.New("encoder unavailable")

// GOPFrames is the keyframe interval in frames for a configuration.
func (c Config) GOPFrames() int {
	gop := c.GOP
	if gop <= 0 {
		gop = 2 * time.Second
	}
	fps := c.FPS
	if fps <= 0 {
		fps = 30
	}
	n := int(gop.Seconds()*float64(fps) + 0.5)
	if n < 1 {
		n = 1
	}
	return n
}

// Open starts an encoder of the kind spec names:
//
//	auto             v4l2m2m when the host has it, else ffmpeg with its best codec
//	v4l2m2m          the Pi 4's hardware encoder through V4L2
//	ffmpeg           an ffmpeg subprocess with the best codec for this host
//	ffmpeg:<codec>   an ffmpeg subprocess with that codec, such as libx264
//	none             no WebRTC video
//
// It returns the encoder and the name of what it opened.
func Open(spec string, cfg Config) (Encoder, string, error) {
	kind, codec, _ := strings.Cut(spec, ":")
	switch kind {
	case "", "auto":
		if e, err := newV4L2M2M(cfg); err == nil {
			return e, "v4l2m2m", nil
		} else if !errors.Is(err, ErrUnavailable) {
			return nil, "", err
		}
		return openFFmpeg(cfg, "")
	case "v4l2m2m":
		e, err := newV4L2M2M(cfg)
		if err != nil {
			return nil, "", err
		}
		return e, "v4l2m2m", nil
	case "ffmpeg":
		return openFFmpeg(cfg, codec)
	case "none":
		return nil, "", ErrUnavailable
	}
	return nil, "", fmt.Errorf("unknown encoder %q", spec)
}

func openFFmpeg(cfg Config, codec string) (Encoder, string, error) {
	if codec == "" {
		var err error
		if codec, err = PickFFmpegCodec(); err != nil {
			return nil, "", err
		}
	}
	e, err := NewFFmpeg(cfg, codec)
	if err != nil {
		return nil, "", err
	}
	return e, "ffmpeg:" + codec, nil
}

// Probe says what Open would use for spec, without encoding anything, so
// the host can announce it as a capability.
func Probe(spec string) (string, error) {
	kind, codec, _ := strings.Cut(spec, ":")
	switch kind {
	case "", "auto":
		if err := probeV4L2M2M(""); err == nil {
			return "v4l2m2m", nil
		}
		c, err := PickFFmpegCodec()
		if err != nil {
			return "", err
		}
		return "ffmpeg:" + c, nil
	case "v4l2m2m":
		return "v4l2m2m", probeV4L2M2M("")
	case "ffmpeg":
		if codec == "" {
			c, err := PickFFmpegCodec()
			if err != nil {
				return "", err
			}
			return "ffmpeg:" + c, nil
		}
		if !ffmpegHas(codec) {
			return "", fmt.Errorf("ffmpeg has no encoder %q: %w", codec, ErrUnavailable)
		}
		return "ffmpeg:" + codec, nil
	case "none":
		return "", ErrUnavailable
	}
	return "", fmt.Errorf("unknown encoder %q", spec)
}

var epoch = time.Now()

// Now is a monotonic clock for Frame timestamps on hosts without a kernel
// one. Any monotonic clock works; only differences matter.
func Now() time.Duration { return time.Since(epoch) }

// EvenRect shrinks r to even coordinates and sides, which 4:2:0 and the
// hardware encoders need.
func EvenRect(r image.Rectangle) image.Rectangle {
	r.Min.X += r.Min.X & 1
	r.Min.Y += r.Min.Y & 1
	r.Max.X -= (r.Max.X - r.Min.X) & 1
	r.Max.Y -= (r.Max.Y - r.Min.Y) & 1
	if r.Dx() < 2 || r.Dy() < 2 {
		return image.Rectangle{}
	}
	return r
}
