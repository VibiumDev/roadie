package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// serveMJPEG writes n JPEG frames as a multipart/x-mixed-replace response.
func serveMJPEG(w http.ResponseWriter, frames [][]byte) {
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	for _, frame := range frames {
		fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame))
		w.Write(frame)
		fmt.Fprint(w, "\r\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	// Write closing boundary so the reader gets EOF cleanly.
	fmt.Fprint(w, "--frame--\r\n")
}

func TestHTTPSourceParseFrames(t *testing.T) {
	frames := [][]byte{
		testJPEG(320, 240),
		testJPEG(320, 240),
		testJPEG(320, 240),
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveMJPEG(w, frames)
	}))
	defer ts.Close()

	buf := &FrameBuffer{}
	mgr := NewHTTPSourceManager(ts.URL, 80, buf)

	// Run in goroutine; it will exit when the stream ends and retries.
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run()
	}()

	// Wait for frames to arrive.
	deadline := time.After(3 * time.Second)
	for {
		if buf.Latest() != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for frames")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Verify we got a valid JPEG.
	got := buf.Latest()
	if len(got) < 2 || got[0] != 0xFF || got[1] != 0xD8 {
		t.Error("frame is not a valid JPEG")
	}

	mgr.Shutdown()
	<-done
}

func TestHTTPSourceReconnect(t *testing.T) {
	var mu sync.Mutex
	callCount := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		n := callCount
		mu.Unlock()

		frames := [][]byte{testJPEG(320, 240)}
		if n == 1 {
			// First connection: serve one frame then close.
			serveMJPEG(w, frames)
			return
		}
		// Second connection: serve frames and keep open until client disconnects.
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 50; i++ {
			fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frames[0]))
			if _, err := w.Write(frames[0]); err != nil {
				return
			}
			fmt.Fprint(w, "\r\n")
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer ts.Close()

	buf := &FrameBuffer{}
	mgr := NewHTTPSourceManager(ts.URL, 80, buf)

	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run()
	}()

	// Wait for the first connection to complete and the status to transition
	// through connecting → connected → disconnected → connecting → connected.
	deadline := time.After(10 * time.Second)
	for {
		mu.Lock()
		n := callCount
		mu.Unlock()
		if n >= 2 && buf.Status() == StatusConnected {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for reconnect (calls=%d, status=%s)", callCount, buf.Status())
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	mgr.Shutdown()
	<-done
}

func TestHTTPSourceCrop(t *testing.T) {
	// Create a frame with black bars on left/right (pillarbox).
	black := color.RGBA{0, 0, 0, 255}
	bright := color.RGBA{200, 200, 200, 255}
	img := makeImage(1920, 1080, black, bright, image.Rect(520, 0, 1400, 1080))
	var jpegBuf bytes.Buffer
	jpeg.Encode(&jpegBuf, img, &jpeg.Options{Quality: 90})
	pillarboxFrame := jpegBuf.Bytes()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve several identical pillarbox frames so crop stabilizes.
		frames := make([][]byte, 5)
		for i := range frames {
			frames[i] = pillarboxFrame
		}
		serveMJPEG(w, frames)
	}))
	defer ts.Close()

	buf := &FrameBuffer{}
	mgr := NewHTTPSourceManager(ts.URL, 80, buf)

	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run()
	}()

	deadline := time.After(3 * time.Second)
	for {
		if buf.Latest() != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for frames")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Raw should be original 1920x1080.
	raw := buf.LatestRaw()
	rawImg, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("failed to decode raw frame: %v", err)
	}
	rawBounds := rawImg.Bounds()
	if rawBounds.Dx() != 1920 || rawBounds.Dy() != 1080 {
		t.Errorf("raw frame size = %dx%d, want 1920x1080", rawBounds.Dx(), rawBounds.Dy())
	}

	// Cropped should be narrower than 1920.
	cropped := buf.Latest()
	croppedImg, err := jpeg.Decode(bytes.NewReader(cropped))
	if err != nil {
		t.Fatalf("failed to decode cropped frame: %v", err)
	}
	croppedBounds := croppedImg.Bounds()
	if croppedBounds.Dx() >= 1920 {
		t.Errorf("cropped frame width = %d, expected less than 1920", croppedBounds.Dx())
	}

	// CropRect should reflect the detected content area.
	cropRect := buf.CropRect()
	if cropRect.Min.X == 0 && cropRect.Max.X == 1920 {
		t.Error("crop rect spans full width, expected pillarbox crop")
	}

	mgr.Shutdown()
	<-done
}
