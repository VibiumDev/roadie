package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The linux-gadget HID backend: Roadie's input commands written as HID
// reports straight to the /dev/hidgN nodes of the "roadie" configfs gadget,
// which gadget/roadie-gadget creates at boot. Roadie never creates, binds or
// removes the gadget itself, so restarting it never re-enumerates the phone.

const gadgetName = "roadie"

const (
	// How long a command waits for the phone to collect the report ahead of
	// its own. f_hid holds one report per device; at USB high speed the
	// phone polls every millisecond, so waiting this long means it is not
	// polling at all.
	hidWriteTimeout = 200 * time.Millisecond
	// The monitor's resend of a state that did not land: short, because it
	// runs again soon.
	hidRetryTimeout = 20 * time.Millisecond
	// Shutdown's release of anything still held.
	hidReleaseTimeout = 100 * time.Millisecond

	gadgetPollInterval  = time.Second
	gadgetDirtyInterval = 100 * time.Millisecond
	gadgetShutdownWait  = 2 * time.Second
	softConnectGap      = time.Second
)

var (
	// ESHUTDOWN from f_hid: no host has selected our configuration.
	errUSBNotConfigured = errors.New("no phone has enumerated the USB gadget (is it plugged in?)")
	// EAGAIN past the timeout: the host has stopped collecting reports.
	errHostNotReading = errors.New("the phone is not collecting input reports (USB suspended or asleep?)")
)

// reportDevice is one open /dev/hidgN.
type reportDevice interface {
	// WriteReport queues one report, waiting up to timeout for the previous
	// one to be collected.
	WriteReport(report []byte, timeout time.Duration) error
	// Stale reports that the node this was opened from has gone or been
	// replaced, as it is when the gadget is rebuilt.
	Stale() bool
	Close() error
}

// gadgetPaths finds the gadget in sysfs and configfs. Tests point sys and
// dev at a temporary tree.
type gadgetPaths struct {
	sys string // normally /sys
	dev string // normally /dev
}

func defaultGadgetPaths() gadgetPaths { return gadgetPaths{sys: "/sys", dev: "/dev"} }

func (p gadgetPaths) gadget() string {
	return filepath.Join(p.sys, "kernel/config/usb_gadget", gadgetName)
}

func (p gadgetPaths) function(fn string) string {
	return filepath.Join(p.gadget(), "functions", fn)
}

func (p gadgetPaths) udcAttr(udc, attr string) string {
	return filepath.Join(p.sys, "class/udc", udc, attr)
}

func readTrim(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.TrimRight(string(b), "\x00")), nil
}

// boundUDC is the controller the gadget is bound to; empty if it exists but
// is unbound.
func (p gadgetPaths) boundUDC() (string, error) {
	return readTrim(filepath.Join(p.gadget(), "UDC"))
}

// carrierDowns reads how many times the gadget's Ethernet function has lost
// carrier. It drops on every disconnect and host reset, never on a suspend,
// so it counts enumerations. False when the gadget has no network function.
func (p gadgetPaths) carrierDowns() (int, bool) {
	for _, fn := range []string{"ecm.phone", "ncm.phone", "rndis.phone"} {
		name, err := readTrim(filepath.Join(p.function(fn), "ifname"))
		if err != nil || name == "" || strings.Contains(name, "%") {
			continue
		}
		v, err := readTrim(filepath.Join(p.sys, "class/net", name, "carrier_down_count"))
		if err != nil {
			return 0, false
		}
		n, err := strconv.Atoi(v)
		return n, err == nil
	}
	return 0, false
}

// hidgNode resolves a HID function to its device node through the
// function's major:minor -- never by assuming /dev/hidg0 is the keyboard.
func (p gadgetPaths) hidgNode(fn string) (string, error) {
	devnum, err := readTrim(filepath.Join(p.function(fn), "dev"))
	if err != nil {
		return "", err
	}
	uevent, err := os.ReadFile(filepath.Join(p.sys, "dev/char", devnum, "uevent"))
	if err != nil {
		return "", fmt.Errorf("%s (%s) has no device yet: %w", fn, devnum, err)
	}
	for _, line := range strings.Split(string(uevent), "\n") {
		if name, ok := strings.CutPrefix(line, "DEVNAME="); ok {
			return filepath.Join(p.dev, name), nil
		}
	}
	return "", fmt.Errorf("%s (%s): no DEVNAME in uevent", fn, devnum)
}

// hidFunction is one HID function of the gadget and its open device node.
// mu guards everything here and the matching input state on GadgetHID.
type hidFunction struct {
	name      string // keyboard, mouse, touch
	reportLen int

	mu      sync.Mutex
	present bool   // the gadget has this function
	problem string // why it cannot be used, if it cannot
	dev     reportDevice
	path    string
	// dirty means the phone has not been sent the current state: the last
	// call failed part-way. The monitor resends state until it lands, so a
	// press is never left standing after its release failed.
	dirty bool
	state []byte // the report for the current state
	// closed is set by Shutdown once it has let go: nothing is sent after.
	closed bool
}

func (f *hidFunction) configfsName() string { return "hid." + f.name }

// GadgetHID is the linux-gadget HIDBackend.
type GadgetHID struct {
	paths gadgetPaths
	open  func(path string) (reportDevice, error)

	kbd, mouse, touch *hidFunction
	kstate            keyboardState  // guarded by kbd.mu
	mstate            mouseState     // guarded by mouse.mu
	tstate            []TouchContact // guarded by touch.mu

	micMu sync.Mutex
	mic   micSink // the gadget microphone; nil if it has none

	status   atomic.Value // HIDStatus
	usbState atomic.Value // string: the UDC's state, "configured" when attached
	udc      atomic.Value // string
	serial   atomic.Value // string

	logMu   sync.Mutex
	lastLog map[string]string

	// Monitor-only. observed: the UDC state has been read at least once.
	// cleanPending: roadie started while a phone may already have been
	// attached, so whatever a previous roadie left held must be released.
	observed     bool
	cleanPending bool
	carrierDowns int // last carrier_down_count of the gadget's netdev; -1 unknown

	// reenumerated: evidence the phone has enumerated us afresh since we
	// last saw it configured -- the Ethernet function lost carrier, or a
	// write found no host. A replug can complete between two polls and
	// look like configured -> configured, or like a resume; this tells.
	reenumerated atomic.Bool

	started atomic.Bool

	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// micSink takes the browser's 16 kHz mono PCM for the gadget microphone.
type micSink interface {
	Write(pcm []byte) error
}

// NewGadgetHID returns the backend for the "roadie" gadget. Call Run.
func NewGadgetHID() *GadgetHID {
	return newGadgetHID(defaultGadgetPaths(), openHIDGadget)
}

func newGadgetHID(paths gadgetPaths, open func(string) (reportDevice, error)) *GadgetHID {
	ctx, cancel := context.WithCancel(context.Background())
	g := &GadgetHID{
		paths:   paths,
		open:    open,
		kbd:     &hidFunction{name: "keyboard", reportLen: keyboardReportLen},
		mouse:   &hidFunction{name: "mouse", reportLen: mouseReportLen},
		touch:   &hidFunction{name: "touch", reportLen: touchReportLen},
		lastLog: map[string]string{},
		wake:    make(chan struct{}, 1),

		cleanPending: true,
		carrierDowns: -1,
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	g.status.Store(HIDDisconnected)
	g.usbState.Store("")
	g.udc.Store("")
	g.serial.Store("")
	return g
}

// SetMic connects the gadget microphone that SendAudio feeds.
func (g *GadgetHID) SetMic(m micSink) {
	g.micMu.Lock()
	g.mic = m
	g.micMu.Unlock()
}

func (g *GadgetHID) functions() []*hidFunction { return []*hidFunction{g.kbd, g.mouse, g.touch} }

// logChange logs msg under key unless it is what was last logged there, so a
// condition that persists is reported once rather than every second.
func (g *GadgetHID) logChange(key, msg string) {
	g.logMu.Lock()
	defer g.logMu.Unlock()
	if g.lastLog[key] == msg {
		return
	}
	g.lastLog[key] = msg
	if msg != "" {
		log.Print(msg)
	}
}

func (g *GadgetHID) nudge() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// ---- monitor ---------------------------------------------------------------

// Run watches the gadget until Shutdown: opens device nodes as they appear,
// follows the phone attaching and detaching, and resends any state that did
// not land.
func (g *GadgetHID) Run() {
	g.started.Store(true)
	defer close(g.done)
	for {
		g.refresh()
		interval := gadgetPollInterval
		if g.anyDirty() {
			interval = gadgetDirtyInterval
		}
		select {
		case <-g.ctx.Done():
			return
		case <-g.wake:
		case <-time.After(interval):
		}
	}
}

func (g *GadgetHID) anyDirty() bool {
	for _, f := range g.functions() {
		f.mu.Lock()
		d := f.dirty && f.dev != nil
		f.mu.Unlock()
		if d {
			return true
		}
	}
	return false
}

func (g *GadgetHID) refresh() {
	if _, err := os.Stat(g.paths.gadget()); err != nil {
		g.logChange("gadget", fmt.Sprintf("USB gadget %q is not configured: start it with "+
			"'sudo systemctl start roadie-gadget' (see 'roadie-gadget status')", gadgetName))
		for _, f := range g.functions() {
			f.mu.Lock()
			f.present = false
			f.closeLocked()
			f.mu.Unlock()
		}
		g.udc.Store("")
		g.setUSBState("")
		g.status.Store(HIDDisconnected)
		return
	}
	udc, _ := g.paths.boundUDC()
	g.udc.Store(udc)
	if s, err := readTrim(filepath.Join(g.paths.gadget(), "strings/0x409/serialnumber")); err == nil {
		g.serial.Store(s)
	}
	if udc == "" {
		g.logChange("gadget", fmt.Sprintf("USB gadget %q exists but is not bound to a controller "+
			"(sudo roadie-gadget up)", gadgetName))
	} else {
		g.logChange("gadget", fmt.Sprintf("USB gadget %q is bound to %s", gadgetName, udc))
	}

	state := "unbound"
	if udc != "" {
		state, _ = readTrim(g.paths.udcAttr(udc, "state"))
	}

	open, want := 0, 0
	for _, f := range g.functions() {
		if g.syncFunction(f) {
			want++
			f.mu.Lock()
			if f.dev != nil {
				open++
			}
			f.mu.Unlock()
		}
	}
	switch {
	case want > 0 && open == want:
		g.status.Store(HIDConnected)
	case want == 0:
		g.logChange("functions", fmt.Sprintf("USB gadget %q has no HID functions", gadgetName))
		g.status.Store(HIDDisconnected)
	default:
		g.status.Store(HIDConnecting)
	}

	if n, ok := g.paths.carrierDowns(); ok {
		if g.carrierDowns >= 0 && n != g.carrierDowns {
			g.reenumerated.Store(true)
		}
		g.carrierDowns = n
	}

	g.setUSBState(state)
	if state == "configured" {
		if g.cleanPending && open == want {
			// Whatever was held when the last roadie stopped -- killed in
			// the middle of a drag, say -- is still held on the phone. What
			// cannot be sent now is left dirty, and retried below.
			g.releaseAll(hidRetryTimeout, true)
			g.cleanPending = false
		}
		g.retryDirty()
	}
}

// setUSBState follows the phone attaching and detaching. A phone that has
// just enumerated starts with nothing pressed, so neither do we -- and there
// is nothing a previous roadie could have left held on it.
func (g *GadgetHID) setUSBState(state string) {
	first := !g.observed
	g.observed = true
	prev, _ := g.usbState.Swap(state).(string)
	fresh := state == "configured" && g.reenumerated.Load()
	if prev == state && !first && !fresh {
		return
	}
	switch {
	case state == "configured" && first:
		g.reenumerated.Store(false)
		log.Printf("USB gadget: phone is attached")
	case state == "configured" && prev == "suspended" && !fresh:
		// A resume, not a new enumeration: the phone still holds whatever
		// it held, and a release that could not be sent while it slept is
		// still owed to it.
		log.Printf("USB gadget: phone resumed")
	case state == "configured":
		g.reenumerated.Store(false)
		log.Printf("USB gadget: phone attached (enumerated)")
		g.resetInputState()
		g.cleanPending = false
	case first:
		log.Printf("USB gadget: no phone attached yet (state: %s)", orGone(state))
	case state == "suspended":
		log.Printf("USB gadget: phone suspended the bus (asleep?)")
	case prev == "configured":
		log.Printf("USB gadget: phone no longer attached (state: %s)", orGone(state))
	}
}

func orGone(state string) string {
	if state == "" {
		return "gadget gone"
	}
	return state
}

// syncFunction brings one function's device up to date with the gadget and
// reports whether the gadget has it.
func (g *GadgetHID) syncFunction(f *hidFunction) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := "fn:" + f.name
	dir := g.paths.function(f.configfsName())
	if _, err := os.Stat(dir); err != nil {
		f.present = false
		f.closeLocked()
		g.logChange(key, fmt.Sprintf("USB gadget has no %s function", f.name))
		return false
	}
	f.present = true

	// The descriptor and roadie's encoder must agree on the report size, or
	// every report would be garbled input on the phone.
	lenStr, err := readTrim(filepath.Join(dir, "report_length"))
	n, _ := strconv.Atoi(lenStr)
	if err != nil || n != f.reportLen {
		f.problem = fmt.Sprintf("%s: gadget report_length is %q but roadie sends %d-byte "+
			"reports; gadget/hid/%s.desc and roadie are out of step", f.name, lenStr, f.reportLen, f.name)
		f.closeLocked()
		g.logChange(key, "USB gadget "+f.problem)
		return true
	}

	if f.dev != nil && f.dev.Stale() {
		g.logChange(key, fmt.Sprintf("USB gadget %s node %s was replaced; reopening", f.name, f.path))
		f.closeLocked()
	}
	if f.dev != nil {
		return true
	}
	path, err := g.paths.hidgNode(f.configfsName())
	if err == nil {
		var dev reportDevice
		if dev, err = g.open(path); err == nil {
			f.dev, f.path, f.problem = dev, path, ""
			g.logChange(key, fmt.Sprintf("USB gadget %s -> %s", f.name, path))
			return true
		}
		if errors.Is(err, os.ErrPermission) {
			err = fmt.Errorf("%w (roadie's user needs to be in the roadie group; see gadget/install)", err)
		}
	}
	f.problem = fmt.Sprintf("%s: %v", f.name, err)
	g.logChange(key, "USB gadget cannot open "+f.problem)
	return true
}

func (f *hidFunction) closeLocked() {
	if f.dev != nil {
		f.dev.Close()
		f.dev = nil
	}
}

// resetInputState forgets everything held: what the phone sees after it
// enumerates us again.
func (g *GadgetHID) resetInputState() {
	g.kbd.mu.Lock()
	g.kstate = keyboardState{}
	g.kbd.dirty, g.kbd.state = false, nil
	g.kbd.mu.Unlock()
	g.mouse.mu.Lock()
	g.mstate = mouseState{}
	g.mouse.dirty, g.mouse.state = false, nil
	g.mouse.mu.Unlock()
	g.touch.mu.Lock()
	g.tstate = nil
	g.touch.dirty, g.touch.state = false, nil
	g.touch.mu.Unlock()
}

func (g *GadgetHID) retryDirty() {
	for _, f := range g.functions() {
		f.mu.Lock()
		if f.dirty && f.dev != nil && f.state != nil {
			if err := g.writeLocked(f, f.state, hidRetryTimeout); err == nil {
				f.dirty = false
				g.logChange("dirty:"+f.name, "")
				log.Printf("USB gadget %s: state delivered after an earlier failure", f.name)
			}
		}
		f.mu.Unlock()
	}
}

// ---- writing ---------------------------------------------------------------

// writeLocked sends one report. Errors from the node itself (as opposed to
// the phone not reading) close it for the monitor to reopen.
func (g *GadgetHID) writeLocked(f *hidFunction, report []byte, timeout time.Duration) error {
	if f.closed {
		return errors.New("roadie is shutting down")
	}
	if !f.present {
		return fmt.Errorf("the USB gadget has no %s (check /etc/roadie/gadget.conf)", f.name)
	}
	if f.dev == nil {
		if f.problem != "" {
			return errors.New(f.problem)
		}
		return fmt.Errorf("%s device is not open yet", f.name)
	}
	if g.USBState() == "suspended" {
		// Nothing can be queued until the phone wakes; don't hold callers.
		timeout = min(timeout, hidRetryTimeout)
	}
	err := f.dev.WriteReport(report, timeout)
	if errors.Is(err, errUSBNotConfigured) {
		g.reenumerated.Store(true)
	}
	if err == nil || errors.Is(err, errUSBNotConfigured) || errors.Is(err, errHostNotReading) {
		return err
	}
	f.closeLocked()
	g.status.Store(HIDConnecting)
	g.nudge()
	return fmt.Errorf("%s: %w", f.path, err)
}

// sendLocked delivers reports in order. If one fails, the device is marked
// dirty with safe, the state the monitor then makes the phone match until
// it lands. Callers pass as safe only what was held both before and after
// the call: a failed press is never delivered later (its caller was told it
// failed and will not release it), and a failed release still is.
func (g *GadgetHID) sendLocked(f *hidFunction, reports [][]byte, safe []byte) error {
	if len(reports) == 0 {
		return nil // nothing sent: whatever is owed is still owed
	}
	for i, r := range reports {
		if err := g.writeLocked(f, r, hidWriteTimeout); err != nil {
			// Owed: anything already sent, or a state still owed from
			// before. A first report refused for want of a host owes
			// nothing -- a phone that enumerates later starts clean.
			if f.dirty || i > 0 || !errors.Is(err, errUSBNotConfigured) {
				f.dirty, f.state = true, safe
				g.nudge()
			}
			if i > 0 {
				return fmt.Errorf("%d of %d reports sent, then: %w", i, len(reports), err)
			}
			return err
		}
	}
	f.dirty = false
	if len(reports) > 0 {
		f.state = reports[len(reports)-1]
	}
	return nil
}

// Held in both a and b: what a failed call converges on.
func (a keyboardState) and(b keyboardState) keyboardState {
	out := keyboardState{mods: a.mods & b.mods}
	n := 0
	for _, k := range a.keys {
		if k != 0 && bytes.IndexByte(b.keys[:], k) >= 0 {
			out.keys[n] = k
			n++
		}
	}
	return out
}

func anyTip(contacts []TouchContact) bool {
	for _, c := range contacts {
		if c.Tip {
			return true
		}
	}
	return false
}

func touchesAnd(a, b []TouchContact) []TouchContact {
	var out []TouchContact
	for _, c := range a {
		for _, d := range b {
			if c.ID == d.ID && c.Tip && d.Tip {
				out = append(out, c)
			}
		}
	}
	return out
}

// ---- HIDBackend ------------------------------------------------------------

func (g *GadgetHID) Backend() string { return BackendLinuxGadget }

func (g *GadgetHID) Status() HIDStatus { return g.status.Load().(HIDStatus) }

// BoardResponding is true when a phone has enumerated the gadget. False
// means nothing is attached, or the phone has suspended the bus.
func (g *GadgetHID) BoardResponding() bool { return g.USBState() == "configured" }

// USBState is the controller's state: "configured" once a phone has
// enumerated us, "not attached", "suspended", and so on.
func (g *GadgetHID) USBState() string { s, _ := g.usbState.Load().(string); return s }

// Port is the USB device controller the gadget is bound to.
func (g *GadgetHID) Port() string { s, _ := g.udc.Load().(string); return s }

// Serial is the gadget's USB serial number, which the phone sees.
func (g *GadgetHID) Serial() string { s, _ := g.serial.Load().(string); return s }

func (g *GadgetHID) KeyPress(keycode int) error {
	return g.keyboardChange(func(k *keyboardState) error { return k.press(keycode) })
}

// absent: the gadget has no such function. Releasing on it is then a no-op
// rather than an error, so /api/hid/release works on a gadget without, say,
// a touchscreen.
func (g *GadgetHID) absent(f *hidFunction) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.present
}

func (g *GadgetHID) KeyRelease(keycode int) error {
	if g.absent(g.kbd) {
		return nil
	}
	return g.keyboardChange(func(k *keyboardState) error { k.release(keycode); return nil })
}

func (g *GadgetHID) keyboardChange(change func(*keyboardState) error) error {
	f := g.kbd
	f.mu.Lock()
	defer f.mu.Unlock()
	next := g.kstate
	if err := change(&next); err != nil {
		return err
	}
	safe := g.kstate.and(next)
	if err := g.sendLocked(f, [][]byte{next.report()}, safe.report()); err != nil {
		g.kstate = safe
		return err
	}
	g.kstate = next
	return nil
}

// Type types text with a US layout, character by character, as the HID
// board does. Every character ends with all keys up, so a failure part-way
// leaves nothing held.
func (g *GadgetHID) Type(text string) error {
	f := g.kbd
	f.mu.Lock()
	defer f.mu.Unlock()
	reports, err := typeReports(g.kstate, text)
	if err != nil {
		return err
	}
	if len(reports) == 0 {
		return nil
	}
	err = g.sendLocked(f, reports, keyboardState{}.report())
	g.kstate = keyboardState{}
	return err
}

func (g *GadgetHID) MouseMove(x, y int) error {
	f := g.mouse
	f.mu.Lock()
	defer f.mu.Unlock()
	reports, next := g.mstate.moveReports(x, y)
	if err := g.sendLocked(f, reports, g.mstate.report()); err != nil {
		return err
	}
	g.mstate = next
	return nil
}

// mouseChange sends the reports for a button change; final is the state
// after them.
func (g *GadgetHID) mouseChange(reports [][]byte, final mouseState) error {
	safe := g.mstate
	safe.buttons &= final.buttons
	if err := g.sendLocked(g.mouse, reports, safe.report()); err != nil {
		g.mstate = safe
		return err
	}
	g.mstate = final
	return nil
}

func (g *GadgetHID) MousePress(buttons int) error {
	g.mouse.mu.Lock()
	defer g.mouse.mu.Unlock()
	next := g.mstate
	next.buttons |= byte(buttons)
	return g.mouseChange([][]byte{next.report()}, next)
}

func (g *GadgetHID) MouseRelease(buttons int) error {
	if g.absent(g.mouse) {
		return nil
	}
	g.mouse.mu.Lock()
	defer g.mouse.mu.Unlock()
	next := g.mstate
	next.buttons &^= byte(buttons)
	return g.mouseChange([][]byte{next.report()}, next)
}

// MouseClick presses and releases, as two reports. If the release fails the
// monitor still delivers it: the button is never left down.
func (g *GadgetHID) MouseClick(buttons int) error {
	g.mouse.mu.Lock()
	defer g.mouse.mu.Unlock()
	down := g.mstate
	down.buttons |= byte(buttons)
	up := down
	up.buttons &^= byte(buttons)
	return g.mouseChange([][]byte{down.report(), up.report()}, up)
}

// maxScroll bounds one scroll call (100 reports of 127).
const maxScroll = 100 * 127

func (g *GadgetHID) MouseScroll(amount int) error {
	f := g.mouse
	f.mu.Lock()
	defer f.mu.Unlock()
	amount = max(min(amount, maxScroll), -maxScroll)
	return g.sendLocked(f, g.mstate.scrollReports(amount), g.mstate.report())
}

func (g *GadgetHID) Touch(contacts []TouchContact) error {
	f := g.touch
	if g.absent(f) && !anyTip(contacts) {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	report, err := touchReport(contacts)
	if err != nil {
		return err
	}
	safe := touchesAnd(g.tstate, contacts)
	safeReport, _ := touchReport(safe)
	if err := g.sendLocked(f, [][]byte{report}, safeReport); err != nil {
		g.tstate = safe
		return err
	}
	g.tstate = nil
	for _, c := range contacts {
		if c.Tip {
			g.tstate = append(g.tstate, c)
		}
	}
	return nil
}

// SendAudio feeds the gadget microphone.
func (g *GadgetHID) SendAudio(pcm []byte) error {
	g.micMu.Lock()
	m := g.mic
	g.micMu.Unlock()
	if m == nil {
		return errors.New("the USB gadget has no microphone (AUDIO_MIC in /etc/roadie/gadget.conf)")
	}
	return m.Write(pcm)
}

// releaseAll lets go of every key, button and finger. force sends neutral
// reports even where roadie believes nothing is held, which is the only way
// to clear what a previous, killed roadie left held. The mouse is only
// touched when a button may be down: its report also moves the pointer.
// A report that cannot be sent is left owed, for the monitor to retry.
func (g *GadgetHID) releaseAll(timeout time.Duration, force bool) {
	for _, f := range g.functions() {
		f.mu.Lock()
		g.releaseLocked(f, timeout, force)
		f.mu.Unlock()
	}
}

func (g *GadgetHID) releaseLocked(f *hidFunction, timeout time.Duration, force bool) {
	if f.dev == nil {
		return
	}
	var report []byte
	switch f {
	case g.kbd:
		if !force && !g.kstate.held() {
			return
		}
		g.kstate = keyboardState{}
		report = g.kstate.report()
	case g.touch:
		if !force && len(g.tstate) == 0 {
			return
		}
		g.tstate = nil
		report, _ = touchReport(nil)
	case g.mouse:
		if !force && g.mstate.buttons == 0 {
			return
		}
		g.mstate.buttons = 0
		report = g.mstate.report()
	}
	if g.writeLocked(f, report, timeout) == nil {
		f.dirty = false
	} else {
		f.dirty, f.state = true, report
	}
}

// ResetHID makes the phone see the whole gadget unplugged and plugged back
// in, by dropping and restoring the controller's pull-up. Device nodes, the
// sound card and the network interface all survive. This is for exceptional
// recovery and runs only when asked (POST /api/hid/reset).
func (g *GadgetHID) ResetHID() error {
	udc := g.Port()
	if udc == "" {
		return errors.New("the USB gadget is not bound to a controller (sudo roadie-gadget up)")
	}
	path := g.paths.udcAttr(udc, "soft_connect")
	if err := os.WriteFile(path, []byte("disconnect"), 0); err != nil {
		return fmt.Errorf("re-enumerate: %w (roadie's user needs the roadie group, "+
			"or run: sudo roadie-gadget reset)", err)
	}
	log.Printf("USB gadget: re-enumerating on %s (requested)", udc)
	time.Sleep(softConnectGap)
	if err := os.WriteFile(path, []byte("connect"), 0); err != nil {
		return fmt.Errorf("reconnect after re-enumerate: %w", err)
	}
	g.resetInputState()
	g.nudge()
	return nil
}

// ResetRelay restarts roadie's side of the link: closes and reopens every
// device node, forgetting held state. The phone sees nothing.
func (g *GadgetHID) ResetRelay() error {
	for _, f := range g.functions() {
		f.mu.Lock()
		f.closeLocked()
		f.mu.Unlock()
	}
	g.resetInputState()
	g.status.Store(HIDConnecting)
	g.nudge()
	return nil
}

// Shutdown releases everything held and closes the device nodes. The gadget
// stays as it is: the phone keeps its keyboard, network and audio while
// roadie restarts.
func (g *GadgetHID) Shutdown() {
	g.cancel()
	if g.started.Load() {
		select {
		case <-g.done:
		case <-time.After(gadgetShutdownWait):
		}
	}
	// Release and close under one lock, then refuse anything else: a
	// command racing the exit must not press what was just released.
	for _, f := range g.functions() {
		f.mu.Lock()
		g.releaseLocked(f, hidReleaseTimeout, false)
		f.closeLocked()
		f.closed = true
		f.mu.Unlock()
	}
}

// describeGadget summarizes the gadget for --list-devices: where it is
// bound, whether a phone is attached, and which node each function has.
// Empty if there is no gadget.
func describeGadget(p gadgetPaths) string {
	if _, err := os.Stat(p.gadget()); err != nil {
		return ""
	}
	udc, _ := p.boundUDC()
	state := "unbound"
	if udc != "" {
		state, _ = readTrim(p.udcAttr(udc, "state"))
	}
	lines := []string{fmt.Sprintf("  - %s on %s (%s)", gadgetName, orDash(udc), state)}
	for _, fn := range []string{"keyboard", "mouse", "touch"} {
		if _, err := os.Stat(p.function("hid." + fn)); err != nil {
			continue
		}
		node, err := p.hidgNode("hid." + fn)
		if err != nil {
			node = "no node"
		}
		lines = append(lines, fmt.Sprintf("    %-9s %s", fn, node))
	}
	if cfg, err := readGadgetAudioConfig(p); err == nil {
		lines = append(lines, fmt.Sprintf("    %-9s %s: microphone %d ch, speaker %d ch",
			"audio", cfg.cardID, cfg.micChannels, cfg.spkChannels))
	}
	return strings.Join(lines, "\n")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
