package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"go.bug.st/serial"
)

const relayDataGlob = "/dev/serial/by-id/usb-Adafruit_Roadie-Relay_*-if02"

// HIDStatus represents the connection state of the relay serial port.
type HIDStatus string

const (
	HIDConnected    HIDStatus = "connected"
	HIDDisconnected HIDStatus = "disconnected"
	HIDConnecting   HIDStatus = "connecting"
)

// HIDController manages the serial connection to the relay board
// and provides methods to send HID commands.
type HIDController struct {
	mu     sync.Mutex
	port   io.WriteCloser
	status atomic.Value // stores HIDStatus

	ctx    context.Context
	cancel context.CancelFunc
}

// NewHIDController creates a new HID controller.
func NewHIDController() *HIDController {
	ctx, cancel := context.WithCancel(context.Background())
	hc := &HIDController{ctx: ctx, cancel: cancel}
	hc.status.Store(HIDDisconnected)
	return hc
}

// Run detects the relay serial port and maintains the connection.
// It reconnects automatically if the port disappears.
func (hc *HIDController) Run() {
	retryDelay := 2 * time.Second
	maxRetryDelay := 30 * time.Second

	for {
		select {
		case <-hc.ctx.Done():
			return
		default:
		}

		hc.status.Store(HIDConnecting)

		path, err := findRelayPort()
		if err != nil {
			log.Printf("HID relay not found: %v", err)
			select {
			case <-hc.ctx.Done():
				return
			case <-time.After(retryDelay):
				retryDelay = min(retryDelay*2, maxRetryDelay)
				continue
			}
		}

		port, err := serial.Open(path, &serial.Mode{BaudRate: 115200})
		if err != nil {
			log.Printf("HID relay open failed: %v", err)
			select {
			case <-hc.ctx.Done():
				return
			case <-time.After(retryDelay):
				retryDelay = min(retryDelay*2, maxRetryDelay)
				continue
			}
		}

		hc.mu.Lock()
		hc.port = port
		hc.mu.Unlock()
		hc.status.Store(HIDConnected)
		retryDelay = 2 * time.Second
		log.Printf("HID relay connected: %s", path)

		// Wait for disconnect or shutdown.
		<-hc.ctx.Done()
		port.Close()
		return
	}
}

// Shutdown closes the serial port and stops the controller.
func (hc *HIDController) Shutdown() {
	hc.cancel()
	hc.mu.Lock()
	if hc.port != nil {
		hc.port.Close()
		hc.port = nil
	}
	hc.mu.Unlock()
}

// Status returns the current connection status.
func (hc *HIDController) Status() HIDStatus {
	return hc.status.Load().(HIDStatus)
}

// sendJSON marshals the command and writes it to the serial port as a newline-terminated JSON string.
func (hc *HIDController) sendJSON(cmd map[string]any) error {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if hc.port == nil {
		return fmt.Errorf("not connected")
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = hc.port.Write(data)
	if err != nil {
		// Write failed — port likely disconnected.
		hc.port.Close()
		hc.port = nil
		hc.status.Store(HIDDisconnected)
		return fmt.Errorf("write failed: %w", err)
	}
	return nil
}

// Ping sends a ping command to verify the relay is responsive.
func (hc *HIDController) Ping() error {
	return hc.sendJSON(map[string]any{"cmd": "ping"})
}

// Type sends text to be typed on the target. Text longer than 29
// characters is chunked with 50ms delays between chunks.
func (hc *HIDController) Type(text string) error {
	const chunkSize = 29
	for i := 0; i < len(text); i += chunkSize {
		end := i + chunkSize
		if end > len(text) {
			end = len(text)
		}
		if i > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		if err := hc.sendJSON(map[string]any{
			"cmd":  "type",
			"text": text[i:end],
		}); err != nil {
			return err
		}
	}
	return nil
}

// KeyPress sends a key press for the given USB HID keycode.
func (hc *HIDController) KeyPress(keycode int) error {
	return hc.sendJSON(map[string]any{"cmd": "key_press", "keycode": keycode})
}

// KeyRelease sends a key release for the given USB HID keycode.
func (hc *HIDController) KeyRelease(keycode int) error {
	return hc.sendJSON(map[string]any{"cmd": "key_release", "keycode": keycode})
}

// MouseMove sends an absolute mouse move (0-32767 range) immediately.
func (hc *HIDController) MouseMove(x, y int) error {
	return hc.sendJSON(map[string]any{"cmd": "mouse_move", "x": x, "y": y})
}

// MouseClick sends a mouse button click.
func (hc *HIDController) MouseClick(buttons int) error {
	return hc.sendJSON(map[string]any{"cmd": "mouse_click", "buttons": buttons})
}

// MousePress sends a mouse button press (hold).
func (hc *HIDController) MousePress(buttons int) error {
	return hc.sendJSON(map[string]any{"cmd": "mouse_press", "buttons": buttons})
}

// MouseRelease sends a mouse button release.
func (hc *HIDController) MouseRelease(buttons int) error {
	return hc.sendJSON(map[string]any{"cmd": "mouse_release", "buttons": buttons})
}

// MouseScroll sends a scroll wheel event. Positive = scroll down, negative = scroll up.
func (hc *HIDController) MouseScroll(amount int) error {
	return hc.sendJSON(map[string]any{"cmd": "mouse_scroll", "amount": amount})
}

// TouchContact represents a single touch point.
type TouchContact struct {
	ID  int  `json:"id"`
	Tip bool `json:"tip"`
	X   int  `json:"x"`
	Y   int  `json:"y"`
}

// Touch sends a multi-touch report with up to 2 contacts.
func (hc *HIDController) Touch(contacts []TouchContact) error {
	return hc.sendJSON(map[string]any{"cmd": "touch", "contacts": contacts})
}

func findRelayPort() (string, error) {
	matches, err := filepath.Glob(relayDataGlob)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no relay data port found")
	}
	return matches[0], nil
}
