package main

import (
	"bytes"
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

// relayMissedPingLimit is how many unanswered pings, five seconds apart, mean
// the board is wedged rather than briefly busy.
const relayMissedPingLimit = 3

// relayGiveUpDelay is how long to wait between attempts once a restart and a
// USB reset have both failed, so a board needing hands does not fill the log.
const relayGiveUpDelay = 60 * time.Second

// softRebootRelay restarts the program on the relay board through its REPL.
//
// This is the recovery that actually works. A USB re-enumeration re-opens
// the port but leaves the microcontroller exactly as it was, so a board that
// has stopped running its loop stays stopped. Ctrl-C interrupts whatever it
// is doing and Ctrl-D restarts code.py, which is what a person does at the
// REPL by hand.
//
// The REPL is a second port on the same board, which roadie does not
// otherwise hold open.
func softRebootRelay(dataPath string) error {
	replPath := strings.Replace(dataPath, "-if02", "-if00", 1)
	if replPath == dataPath {
		return fmt.Errorf("cannot work out the REPL port from %q", dataPath)
	}
	port, err := serial.Open(replPath, &serial.Mode{BaudRate: 115200})
	if err != nil {
		return fmt.Errorf("open %s: %w", replPath, err)
	}
	defer port.Close()
	if _, err := port.Write([]byte{0x03}); err != nil { // Ctrl-C
		return err
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := port.Write([]byte{0x04}); err != nil { // Ctrl-D
		return err
	}
	time.Sleep(2 * time.Second) // let code.py come back up
	return nil
}

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

	// How many recoveries have been tried since the relay last answered.
	// This outlives a reconnect on purpose: each attempt ends by
	// reconnecting, so a counter scoped to one connection would reset
	// before it could ever escalate.
	attempts := 0

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

		// Monitor the connection. The ping is a round trip: a board that
		// has stopped running its loop still accepts everything written to
		// it, so only its answer distinguishes working from wedged.
		missed := 0
		for {
			select {
			case <-hc.ctx.Done():
				hc.closePort()
				return
			case <-time.After(5 * time.Second):
				hc.mu.Lock()
				disconnected := hc.port == nil
				hc.mu.Unlock()
				if disconnected {
					log.Printf("HID relay disconnected")
					break
				}

				if err := hc.pingRoundTrip(2 * time.Second); err != nil {
					missed++
					log.Printf("HID relay did not answer (%d/%d): %v",
						missed, relayMissedPingLimit, err)
					if missed < relayMissedPingLimit {
						continue
					}
					path, _ := hc.path.Load().(string)
					attempts++
					switch attempts {
					case 1:
						log.Printf("HID relay is not answering; restarting its program")
						if err := softRebootRelay(path); err != nil {
							log.Printf("HID relay soft reboot failed: %v", err)
						}
					case 2:
						log.Printf("HID relay still not answering; re-enumerating it over USB")
						if err := resetRelayUSB(path); err != nil {
							log.Printf("HID relay USB reset failed: %v", err)
						}
					default:
						// Beyond this it is not something software can fix,
						// and retrying every few seconds only fills the log.
						log.Printf("HID relay is still not answering after a " +
							"restart and a USB reset — unplug and replug it")
						select {
						case <-hc.ctx.Done():
							return
						case <-time.After(relayGiveUpDelay):
						}
					}
					// Close after the recovery, not before. Closing a tty
					// waits for whatever is queued to reach the device, and
					// a board that has stopped reading never takes it: the
					// close then sits there for the kernel's full 30-second
					// drain timeout. Restarting the board first means it is
					// reading again by the time we let go, and the close
					// returns at once.
					hc.closePort()
					break
				}
				if missed > 0 || attempts > 0 {
					log.Printf("HID relay is answering again")
				}
				missed, attempts = 0, 0
				continue
			}
			break
		}
		hc.closePort()

		// Small delay before reconnect attempt.
		select {
		case <-hc.ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// closePort lets go of the serial port and marks the relay disconnected.
//
// Every exit from the monitor loop has to come through here. The loop
// reconnects by opening the port again, so a handle left open is a handle
// the loop then fails to open itself, reporting the board as busy for ever
// when the only thing holding it is roadie.
func (hc *HIDController) closePort() {
	hc.mu.Lock()
	if hc.port != nil {
		hc.port.Close()
		hc.port = nil
	}
	hc.mu.Unlock()
	hc.status.Store(HIDDisconnected)
	hc.path.Store("")
}

// hidShutdownCloseWait is how long shutdown waits for the serial port to
// close before leaving it to the kernel.
const hidShutdownCloseWait = time.Second

// Shutdown closes the serial port and stops the controller.
func (hc *HIDController) Shutdown() {
	hc.cancel()
	hc.mu.Lock()
	port := hc.port
	hc.port = nil
	hc.mu.Unlock()
	hc.path.Store("")
	if port == nil {
		return
	}
	// Don't let a wedged board hold up the exit. Closing a tty waits for
	// queued output to reach the device, and a board that has stopped
	// reading never takes it, so the close sits there for the kernel's
	// 30-second drain timeout and roadie looks like it is ignoring the
	// signal. The fd is closed by the kernel when the process goes.
	closed := make(chan struct{})
	go func() {
		port.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(hidShutdownCloseWait):
	}
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

// pingRoundTrip sends a ping and waits for the relay's answer.
//
// Writing alone proves only that the port still exists. A relay that is
// enumerated but no longer running its loop accepts bytes forever and does
// nothing with them, which is indistinguishable from health unless the
// reply is read. The relay answers {"ok":...} on the same port, and that
// is the only thing it ever writes there.
func (hc *HIDController) pingRoundTrip(timeout time.Duration) error {
	hc.mu.Lock()
	port := hc.port
	hc.mu.Unlock()
	if port == nil {
		return fmt.Errorf("not connected")
	}
	// A real serial port can be read back from; a test's writer cannot, and
	// there is nothing to verify against in that case.
	readable, ok := port.(interface {
		Read([]byte) (int, error)
		SetReadTimeout(time.Duration) error
		ResetInputBuffer() error
	})
	if !ok {
		return hc.Ping()
	}

	// Anything already buffered is the answer to an older ping.
	_ = readable.ResetInputBuffer()
	if err := hc.Ping(); err != nil {
		return err
	}
	if err := readable.SetReadTimeout(timeout); err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)
	var line []byte
	buf := make([]byte, 64)
	for time.Now().Before(deadline) {
		n, err := readable.Read(buf)
		if err != nil {
			return fmt.Errorf("read failed: %w", err)
		}
		if n == 0 {
			break // read timed out
		}
		line = append(line, buf[:n]...)
		if bytes.Contains(line, []byte("\"ok\"")) {
			return nil
		}
		if len(line) > 4096 {
			break
		}
	}
	return fmt.Errorf("no answer within %s", timeout)
}

// Audio frame constants. These restate board/shared/protocol.py, which the
// boards themselves import, so the two can drift apart -- and did, silently,
// when the command was renumbered here but not there. TestAudioConstants
// reads the Python and fails if they disagree.
const (
	audioLead  = 0x00 // CDC lead-in byte; never occurs in the JSON line traffic
	audioCmd   = 0x40
	audioMagic = 0xA5
	audioMax   = 512 // max PCM payload bytes per frame
)

// SendAudio forwards raw PCM (16 kHz S16LE mono) to the HID board's USB
// microphone, chunked into unacknowledged audio frames. The caller is
// expected to deliver audio at real-time pace; the board buffers only
// ~100 ms ahead.
func (hc *HIDController) SendAudio(pcm []byte) error {
	if len(pcm)%2 == 1 {
		pcm = pcm[:len(pcm)-1] // whole S16 samples only
	}
	for off := 0; off < len(pcm); off += audioMax {
		end := min(off+audioMax, len(pcm))
		chunk := pcm[off:end]
		frame := make([]byte, 0, 5+len(chunk))
		frame = append(frame, audioLead, audioCmd, audioMagic,
			byte(len(chunk)>>8), byte(len(chunk)))
		frame = append(frame, chunk...)

		hc.mu.Lock()
		if hc.port == nil {
			hc.mu.Unlock()
			return fmt.Errorf("not connected")
		}
		_, err := hc.port.Write(frame)
		if err != nil {
			// Write failed — port likely disconnected.
			hc.port.Close()
			hc.port = nil
			hc.status.Store(HIDDisconnected)
			hc.path.Store("")
			hc.mu.Unlock()
			return fmt.Errorf("write failed: %w", err)
		}
		hc.mu.Unlock()
	}
	return nil
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
