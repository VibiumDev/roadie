package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"time"
)

// HTTPSourceManager reads MJPEG frames from a remote Roadie instance's
// /raw-stream endpoint and feeds them into a FrameBuffer.
type HTTPSourceManager struct {
	URL string
	Buf *FrameBuffer

	ctx    context.Context
	cancel context.CancelFunc
}

// NewHTTPSourceManager creates a new HTTPSourceManager.
func NewHTTPSourceManager(url string, buf *FrameBuffer) *HTTPSourceManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &HTTPSourceManager{
		URL:    url,
		Buf:    buf,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Run connects to the MJPEG URL and reads frames in a loop with automatic
// reconnection on failure. It mirrors CaptureManager.Run in structure.
func (h *HTTPSourceManager) Run() {
	retryDelay := 2 * time.Second
	const maxRetryDelay = 30 * time.Second

	for {
		h.Buf.SetStatus(StatusConnecting)

		err := h.stream()
		if err == context.Canceled {
			return
		}
		if err != nil {
			log.Printf("http source %s: %v", h.URL, err)
		}

		h.Buf.Clear()

		select {
		case <-h.ctx.Done():
			return
		case <-time.After(retryDelay):
			if retryDelay < maxRetryDelay {
				retryDelay = min(retryDelay*2, maxRetryDelay)
			}
		}
	}
}

// stream performs a single HTTP GET, parses the MJPEG multipart stream, and
// processes frames until the connection drops or context is cancelled.
func (h *HTTPSourceManager) stream() error {
	req, err := http.NewRequestWithContext(h.ctx, "GET", h.URL, nil)
	if err != nil {
		return fmt.Errorf("bad url: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	mediaType, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return fmt.Errorf("bad content-type: %w", err)
	}
	if mediaType != "multipart/x-mixed-replace" {
		return fmt.Errorf("expected multipart/x-mixed-replace, got %s", mediaType)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return fmt.Errorf("no boundary in content-type")
	}

	mr := multipart.NewReader(resp.Body, boundary)
	h.Buf.SetStatus(StatusConnected)

	var activeCrop image.Rectangle
	var consecutiveBlack int
	var frameCount int
	var prevBlack bool
	jpegOpts := &jpeg.Options{}

	// Use a modest default fps for periodic detection; HTTP sources don't
	// expose the upstream frame rate, so ~30 frames ≈ 1 s at typical rates.
	const httpCropInterval = 30

	for {
		select {
		case <-h.ctx.Done():
			return context.Canceled
		default:
		}

		part, err := mr.NextPart()
		if err == io.EOF {
			return fmt.Errorf("stream ended")
		}
		if err != nil {
			return fmt.Errorf("read part: %w", err)
		}

		rawBytes, err := io.ReadAll(part)
		part.Close()
		if err != nil {
			return fmt.Errorf("read frame: %w", err)
		}

		img, err := jpeg.Decode(bytes.NewReader(rawBytes))
		if err != nil {
			log.Printf("http source: skipping bad jpeg: %v", err)
			continue
		}
		frameCount++
		jpegOpts.Quality = h.Buf.Quality()

		// Black frame detection.
		black := isBlackFrame(img)
		if black {
			consecutiveBlack++
			if consecutiveBlack == maxConsecutiveBlack {
				log.Printf("http source %s: no signal (black frames)", h.URL)
				h.Buf.SetStatus(StatusNoSignal)
			}
		} else {
			if consecutiveBlack >= maxConsecutiveBlack {
				log.Printf("http source %s: signal restored", h.URL)
				h.Buf.SetStatus(StatusConnected)
			}
			consecutiveBlack = 0
		}

		// Periodic crop detection: first frame, every ~1 s, or on signal transitions.
		transitioned := black != prevBlack
		prevBlack = black
		fullBounds := img.Bounds()
		if frameCount == 1 || transitioned || frameCount%httpCropInterval == 0 {
			rect := detectContentRect(img, cropThreshold)
			if isMajorCropChange(activeCrop, rect, fullBounds) {
				activeCrop = rect
			}
		}

		if activeCrop == (image.Rectangle{}) || activeCrop == fullBounds {
			// No crop needed — use original JPEG bytes for both.
			h.Buf.UpdateBoth(rawBytes, rawBytes, fullBounds)
		} else {
			// Crop active — re-encode cropped version, keep original raw.
			type subImager interface {
				SubImage(r image.Rectangle) image.Image
			}
			var cropped image.Image
			if si, ok := img.(subImager); ok {
				cropped = si.SubImage(activeCrop)
			} else {
				dst := image.NewRGBA(activeCrop)
				for y := activeCrop.Min.Y; y < activeCrop.Max.Y; y++ {
					for x := activeCrop.Min.X; x < activeCrop.Max.X; x++ {
						dst.Set(x, y, img.At(x, y))
					}
				}
				cropped = dst
			}

			var croppedBuf bytes.Buffer
			if err := jpeg.Encode(&croppedBuf, cropped, jpegOpts); err != nil {
				continue
			}
			h.Buf.UpdateBoth(croppedBuf.Bytes(), rawBytes, activeCrop)
		}
	}
}

// Device returns the MJPEG source URL.
func (h *HTTPSourceManager) Device() string {
	return h.URL
}

// Shutdown stops the HTTP source manager.
func (h *HTTPSourceManager) Shutdown() {
	h.cancel()
}
