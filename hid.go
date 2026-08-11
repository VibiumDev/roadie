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
	"sort"
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
	// Filter selects which relay board to bind to when more than one is
	// connected. It matches as a case-insensitive substring against the
	// board's USB serial number or its serial port path. Empty means
	// "the only connected board".
	Filter string

	mu     sync.Mutex
	port   io.WriteCloser
	status atomic.Value // stores HIDStatus
	path   atomic.Value // stores string: the bound serial port path

	ctx    context.Context
	cancel context.CancelFunc
}

// NewHIDController creates a new HID controller bound to the relay board
// matching filter. See HIDController.Filter for the matching rules.
func NewHIDController(filter string) *HIDController {
	ctx, cancel := context.WithCancel(context.Background())
	hc := &HIDController{Filter: filter, ctx: ctx, cancel: cancel}
	hc.status.Store(HIDDisconnected)
	hc.path.Store("")
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

		path, err := findRelayPort(hc.Filter)
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
		hc.path.Store(path)
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
	hc.path.Store("")
}

// Status returns the current connection status.
func (hc *HIDController) Status() HIDStatus {
	return hc.status.Load().(HIDStatus)
}

// Port returns the serial port path of the bound relay board, or empty
// if not connected.
func (hc *HIDController) Port() string {
	p, _ := hc.path.Load().(string)
	return p
}

// Serial returns the USB serial number of the bound relay board, or empty
// if not connected.
func (hc *HIDController) Serial() string {
	return relaySerialFromPath(hc.Port())
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
		hc.path.Store("")
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

// RelayInfo describes a connected relay board.
type RelayInfo struct {
	Serial string // USB serial number — unique per board
	Path   string // data serial port path (the second CDC interface)
}

// String renders a relay as "SERIAL (path)" for diagnostic output.
func (r RelayInfo) String() string {
	if r.Serial == "" {
		return r.Path
	}
	return fmt.Sprintf("%s (%s)", r.Serial, r.Path)
}

// ListRelays returns every connected relay board, sorted by serial number
// so the ordering is stable across runs.
func ListRelays() []RelayInfo {
	var relays []RelayInfo
	if runtime.GOOS == "darwin" {
		relays = listRelaysMacOS()
	} else {
		relays = listRelaysLinux()
	}
	sort.Slice(relays, func(i, j int) bool {
		if relays[i].Serial != relays[j].Serial {
			return relays[i].Serial < relays[j].Serial
		}
		return relays[i].Path < relays[j].Path
	})
	return relays
}

// findRelayPort returns the data serial port of the relay board selected by
// filter, a case-insensitive substring matched against each board's serial
// number and port path. An empty filter is only valid when exactly one board
// is connected — with several attached, picking one arbitrarily would let two
// roadie instances fight over the same board, so it is an error instead.
func findRelayPort(filter string) (string, error) {
	r, err := selectRelay(ListRelays(), filter)
	if err != nil {
		return "", err
	}
	return r.Path, nil
}

// selectRelay resolves filter against the connected boards. It is the pure
// half of findRelayPort, split out so the matching rules can be tested
// without hardware attached.
func selectRelay(relays []RelayInfo, filter string) (RelayInfo, error) {
	if len(relays) == 0 {
		return RelayInfo{}, fmt.Errorf("no relay data port found")
	}

	if filter == "" {
		if len(relays) > 1 {
			return RelayInfo{}, fmt.Errorf("%d relay boards connected, use --relay to pick one: %s",
				len(relays), formatRelays(relays))
		}
		return relays[0], nil
	}

	f := strings.ToLower(filter)
	var matched []RelayInfo
	for _, r := range relays {
		if strings.Contains(strings.ToLower(r.Serial), f) || strings.Contains(strings.ToLower(r.Path), f) {
			matched = append(matched, r)
		}
	}
	switch len(matched) {
	case 0:
		return RelayInfo{}, fmt.Errorf("no relay board matching %q, connected: %s", filter, formatRelays(relays))
	case 1:
		return matched[0], nil
	default:
		return RelayInfo{}, fmt.Errorf("relay selector %q is ambiguous, matches: %s", filter, formatRelays(matched))
	}
}

// formatRelays joins relays into a comma-separated list for error messages.
func formatRelays(relays []RelayInfo) string {
	parts := make([]string, len(relays))
	for i, r := range relays {
		parts[i] = r.String()
	}
	return strings.Join(parts, ", ")
}

// listRelaysLinux enumerates relay boards from /dev/serial/by-id/ symlinks,
// which embed the USB product name, serial number, and interface number.
func listRelaysLinux() []RelayInfo {
	matches, err := filepath.Glob(relayDataGlob)
	if err != nil {
		return nil
	}
	relays := make([]RelayInfo, 0, len(matches))
	for _, m := range matches {
		relays = append(relays, RelayInfo{Serial: relaySerialFromPath(m), Path: m})
	}
	return relays
}

// relaySerialFromPath extracts the USB serial number from a by-id symlink
// such as "usb-Adafruit_Roadie-Relay_A1B2C3D4E5F60001-if02". It returns
// empty for paths that aren't by-id symlinks (e.g. macOS /dev/cu.* ports).
func relaySerialFromPath(path string) string {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "usb-") {
		return ""
	}
	base = strings.TrimSuffix(base, "-if02")
	if i := strings.LastIndex(base, "_"); i >= 0 {
		return base[i+1:]
	}
	return ""
}

// listRelaysMacOS uses ioreg to find every Roadie-Relay USB device and pairs
// each one with its data serial port. ioreg prints a device node followed by
// its interface children, so the serial number of a board always precedes
// that board's IOCalloutDevice entries.
func listRelaysMacOS() []RelayInfo {
	out, err := exec.Command("ioreg", "-n", "Roadie-Relay", "-r", "-l").Output()
	if err != nil || len(out) == 0 {
		return nil
	}

	var relays []RelayInfo
	cur := -1
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.Contains(line, `"USB Serial Number"`):
			relays = append(relays, RelayInfo{Serial: ioregValue(line)})
			cur = len(relays) - 1
		case strings.Contains(line, "IOCalloutDevice"):
			p := ioregValue(line)
			if !strings.HasPrefix(p, "/dev/") {
				continue
			}
			if cur < 0 {
				// A port with no preceding serial number — keep it as an
				// unlabelled board rather than dropping it.
				relays = append(relays, RelayInfo{})
				cur = len(relays) - 1
			}
			// The data port is the last CDC interface listed; the first is
			// the console. Overwriting leaves us with the data port.
			relays[cur].Path = p
		}
	}

	// Drop devices that reported a serial but exposed no serial ports.
	kept := relays[:0]
	for _, r := range relays {
		if r.Path != "" {
			kept = append(kept, r)
		}
	}
	return kept
}

// ioregValue extracts the quoted value from an ioreg line such as
// `"IOCalloutDevice" = "/dev/cu.usbmodem2103"`.
func ioregValue(line string) string {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(parts[1]), `"`)
}
