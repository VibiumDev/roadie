package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

		// Monitor connection: periodic pings detect disconnect.
		for {
			select {
			case <-hc.ctx.Done():
				port.Close()
				return
			case <-time.After(5 * time.Second):
				hc.mu.Lock()
				disconnected := hc.port == nil
				hc.mu.Unlock()
				if disconnected {
					log.Printf("HID relay disconnected")
					break
				}
				if err := hc.Ping(); err != nil {
					log.Printf("HID relay ping failed: %v", err)
					break
				}
				continue
			}
			break
		}

		// Small delay before reconnect attempt.
		select {
		case <-hc.ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
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

// ResetHID sends a reset command to the HID board via the relay.
// The HID board will reboot after acknowledging the command.
func (hc *HIDController) ResetHID() error {
	return hc.sendJSON(map[string]any{"cmd": "reset_hid"})
}

// ResetRelay sends a reset command that causes the relay board to reboot.
// The serial connection will drop and reconnect automatically.
func (hc *HIDController) ResetRelay() error {
	return hc.sendJSON(map[string]any{"cmd": "reset_self"})
}

func findRelayPort() (string, error) {
	// Linux: /dev/serial/by-id/ symlinks include product name and interface.
	matches, err := filepath.Glob(relayDataGlob)
	if err == nil && len(matches) > 0 {
		return matches[0], nil
	}

	// macOS: use ioreg to find Roadie-Relay by USB product name.
	if runtime.GOOS == "darwin" {
		return findRelayPortMacOS()
	}

	return "", fmt.Errorf("no relay data port found")
}

// findRelayPortMacOS uses ioreg to find the Roadie-Relay USB device
// and returns its data serial port (the second CDC interface).
func findRelayPortMacOS() (string, error) {
	out, err := exec.Command("ioreg", "-n", "Roadie-Relay", "-r", "-l").Output()
	if err != nil {
		return "", fmt.Errorf("ioreg failed: %w", err)
	}
	if len(out) == 0 {
		return "", fmt.Errorf("Roadie-Relay not found in ioreg")
	}

	// Extract all IOCalloutDevice paths — interface order means
	// the first is console (CDC 0), the second is data (CDC 2).
	var ports []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "IOCalloutDevice") {
			// Line looks like: "IOCalloutDevice" = "/dev/cu.usbmodem2103"
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				p := strings.Trim(strings.TrimSpace(parts[1]), `"`)
				if strings.HasPrefix(p, "/dev/") {
					ports = append(ports, p)
				}
			}
		}
	}

	if len(ports) == 0 {
		return "", fmt.Errorf("Roadie-Relay found but no serial ports")
	}
	// Data port is the last one (second CDC interface).
	return ports[len(ports)-1], nil
}
