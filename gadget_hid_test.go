package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeHost stands in for the phone on the far side of every fake hidg node:
// it records the reports each device delivers, and a test can make a node
// refuse writes to act out a phone that is absent, asleep, or gone.
type fakeHost struct {
	mu      sync.Mutex
	reports map[string][][]byte // by device path
	fail    map[string]error    // next writes to a path fail with this
	opens   map[string]int
	stale   map[string]bool
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		reports: map[string][][]byte{},
		fail:    map[string]error{},
		opens:   map[string]int{},
		stale:   map[string]bool{},
	}
}

type fakeHIDDev struct {
	host   *fakeHost
	path   string
	closed bool
}

func (h *fakeHost) open(path string) (reportDevice, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.fail["open:"+path]; err != nil {
		return nil, err
	}
	h.opens[path]++
	h.stale[path] = false
	return &fakeHIDDev{host: h, path: path}, nil
}

func (d *fakeHIDDev) WriteReport(r []byte, timeout time.Duration) error {
	d.host.mu.Lock()
	defer d.host.mu.Unlock()
	if d.closed {
		return errors.New("write to closed fake device")
	}
	if err := d.host.fail[d.path]; err != nil {
		return err
	}
	d.host.reports[d.path] = append(d.host.reports[d.path], append([]byte(nil), r...))
	return nil
}

func (d *fakeHIDDev) Stale() bool {
	d.host.mu.Lock()
	defer d.host.mu.Unlock()
	return d.host.stale[d.path]
}

func (d *fakeHIDDev) Close() error { d.closed = true; return nil }

func (h *fakeHost) setFail(path string, err error) {
	h.mu.Lock()
	h.fail[path] = err
	h.mu.Unlock()
}

func (h *fakeHost) got(path string) [][]byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]byte(nil), h.reports[path]...)
}

func (h *fakeHost) last(path string) []byte {
	r := h.got(path)
	if len(r) == 0 {
		return nil
	}
	return r[len(r)-1]
}

func (h *fakeHost) reset() {
	h.mu.Lock()
	h.reports = map[string][][]byte{}
	h.mu.Unlock()
}

// fakeGadget builds the sysfs and configfs files GadgetHID reads, under a
// temporary directory, the way roadie-gadget leaves them on a real Pi.
type fakeGadget struct {
	t     *testing.T
	paths gadgetPaths
	host  *fakeHost
	nodes map[string]string // function name -> /dev path
}

const fakeUDC = "fe980000.usb"

func newFakeGadget(t *testing.T) *fakeGadget {
	t.Helper()
	root := t.TempDir()
	fg := &fakeGadget{
		t:     t,
		paths: gadgetPaths{sys: filepath.Join(root, "sys"), dev: filepath.Join(root, "dev")},
		host:  newFakeHost(),
		nodes: map[string]string{},
	}
	g := fg.paths.gadget()
	fg.write(filepath.Join(g, "UDC"), fakeUDC+"\n")
	fg.write(filepath.Join(g, "strings/0x409/serialnumber"), "10000000ff2137f1\n")
	fg.write(fg.paths.udcAttr(fakeUDC, "state"), "not attached\n")
	fg.write(fg.paths.udcAttr(fakeUDC, "soft_connect"), "")
	// Deliberately not in keyboard/mouse/touch order: the backend must map
	// by device number, not by assuming hidg0 is the keyboard.
	fg.addHID("touch", 13, "236:0", "hidg0")
	fg.addHID("keyboard", 8, "236:1", "hidg1")
	fg.addHID("mouse", 6, "236:2", "hidg2")
	return fg
}

func (fg *fakeGadget) write(path, content string) {
	fg.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fg.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fg.t.Fatal(err)
	}
}

func (fg *fakeGadget) addHID(name string, reportLen int, devnum, node string) {
	dir := fg.paths.function("hid." + name)
	fg.write(filepath.Join(dir, "dev"), devnum+"\n")
	fg.write(filepath.Join(dir, "report_length"), fmt.Sprintf("%d\n", reportLen))
	fg.write(filepath.Join(fg.paths.sys, "dev/char", devnum, "uevent"),
		"MAJOR=236\nMINOR=x\nDEVNAME="+node+"\n")
	fg.nodes[name] = filepath.Join(fg.paths.dev, node)
}

func (fg *fakeGadget) setState(state string) {
	fg.write(fg.paths.udcAttr(fakeUDC, "state"), state+"\n")
}

// newTestGadgetHID returns a backend on a fake gadget, refreshed once so its
// devices are open, with the phone attached unless attached is false.
func newTestGadgetHID(t *testing.T, attached bool) (*GadgetHID, *fakeGadget) {
	t.Helper()
	fg := newFakeGadget(t)
	if attached {
		fg.setState("configured")
	}
	g := newGadgetHID(fg.paths, fg.host.open)
	g.refresh()
	fg.host.reset() // drop the start-up clean slate; tests check it on its own
	return g, fg
}

func TestGadgetMapsFunctionsByDeviceNumber(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	if g.Status() != HIDConnected {
		t.Fatalf("status %s, want connected", g.Status())
	}
	if !g.BoardResponding() || g.USBState() != "configured" {
		t.Errorf("phone attached but BoardResponding=%v state=%q", g.BoardResponding(), g.USBState())
	}
	if g.Port() != fakeUDC || g.Serial() != "10000000ff2137f1" {
		t.Errorf("port %q serial %q", g.Port(), g.Serial())
	}
	if err := g.KeyPress(4); err != nil {
		t.Fatal(err)
	}
	if got := fg.host.got(fg.nodes["keyboard"]); len(got) != 1 || !strings.HasSuffix(fg.nodes["keyboard"], "hidg1") {
		t.Errorf("keyboard report went to the wrong node: %v", fg.host.reports)
	}
}

func TestGadgetNotAttached(t *testing.T) {
	g, fg := newTestGadgetHID(t, false)
	if g.Status() != HIDConnected || g.BoardResponding() {
		t.Fatalf("status %s responding %v; want connected, not responding", g.Status(), g.BoardResponding())
	}
	fg.host.setFail(fg.nodes["keyboard"], errUSBNotConfigured)
	if err := g.KeyPress(4); !errors.Is(err, errUSBNotConfigured) {
		t.Errorf("KeyPress with no phone: %v", err)
	}
	// Nothing to resend: a phone that enumerates later starts clean.
	if g.anyDirty() {
		t.Error("a first write refused for want of a phone left the device dirty")
	}
}

func TestGadgetReportLengthMismatch(t *testing.T) {
	fg := newFakeGadget(t)
	fg.write(filepath.Join(fg.paths.function("hid.mouse"), "report_length"), "7\n")
	fg.setState("configured")
	g := newGadgetHID(fg.paths, fg.host.open)
	g.refresh()
	if g.Status() != HIDConnecting {
		t.Errorf("status %s with an unusable mouse, want connecting", g.Status())
	}
	err := g.MouseMove(1, 1)
	if err == nil || !strings.Contains(err.Error(), "report_length") {
		t.Errorf("MouseMove on a mismatched mouse: %v", err)
	}
	if g.KeyPress(4) != nil {
		t.Error("the keyboard should still work")
	}
}

// A release that cannot be delivered right away must still be delivered:
// the caller hears about the failure, and the button does not stay down.
func TestGadgetFailedReleaseIsRetried(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	mouse := fg.nodes["mouse"]
	if err := g.MousePress(1); err != nil {
		t.Fatal(err)
	}
	fg.host.setFail(mouse, errHostNotReading)
	if err := g.MouseRelease(1); !errors.Is(err, errHostNotReading) {
		t.Fatalf("release while the phone is not reading: %v", err)
	}
	if !g.anyDirty() {
		t.Fatal("failed release did not mark the mouse dirty")
	}
	g.refresh() // still failing: stays dirty
	fg.host.setFail(mouse, nil)
	g.refresh()
	if g.anyDirty() {
		t.Error("still dirty after the phone started reading again")
	}
	if last := fg.host.last(mouse); last[0] != 0 {
		t.Errorf("last mouse report % x leaves a button down", last)
	}
}

// A press whose caller was told it failed must never land later.
func TestGadgetFailedPressIsRolledBack(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	kbd := fg.nodes["keyboard"]
	fg.host.setFail(kbd, errHostNotReading)
	if err := g.KeyPress(0x04); err == nil {
		t.Fatal("press succeeded with the phone not reading")
	}
	fg.host.setFail(kbd, nil)
	g.refresh()
	if err := g.KeyPress(0x05); err != nil {
		t.Fatal(err)
	}
	for _, r := range fg.host.got(kbd) {
		if bytes.Contains(r[2:], []byte{0x04}) {
			t.Errorf("report % x carries the key whose press failed", r)
		}
	}
}

func TestGadgetClickReleaseRetried(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	mouse := fg.nodes["mouse"]
	// Let the press through and refuse the release, as f_hid does when the
	// phone stops polling between the two.
	calls := 0
	g.mouse.mu.Lock()
	g.mouse.dev = &scriptedDev{fn: func(r []byte) error {
		calls++
		if calls == 2 {
			return errHostNotReading
		}
		return fg.host.devWrite(mouse, r)
	}}
	g.mouse.mu.Unlock()
	if err := g.MouseClick(1); err == nil {
		t.Fatal("click reported success with its release refused")
	}
	g.refresh()
	got := fg.host.got(mouse)
	if len(got) != 2 || got[0][0] != 1 || got[1][0] != 0 {
		t.Errorf("phone saw % x; want press then (resent) release", got)
	}
}

type scriptedDev struct{ fn func([]byte) error }

func (s *scriptedDev) WriteReport(r []byte, _ time.Duration) error { return s.fn(r) }
func (s *scriptedDev) Stale() bool                                 { return false }
func (s *scriptedDev) Close() error                                { return nil }

func (h *fakeHost) devWrite(path string, r []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reports[path] = append(h.reports[path], append([]byte(nil), r...))
	return nil
}

// A phone that re-enumerates has nothing held. Neither may roadie: a key
// held before the replug must not ride along on the next report.
func TestGadgetReconnectForgetsHeldState(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	kbd := fg.nodes["keyboard"]
	if err := g.KeyPress(0xE1); err != nil { // shift
		t.Fatal(err)
	}
	fg.setState("not attached")
	g.refresh()
	if g.BoardResponding() {
		t.Error("still responding after the phone went away")
	}
	fg.setState("configured")
	g.refresh()
	fg.host.reset()
	if err := g.KeyPress(0x04); err != nil {
		t.Fatal(err)
	}
	if r := fg.host.last(kbd); r[0] != 0 {
		t.Errorf("shift from before the replug is still in % x", r)
	}
	// And a fresh phone gets no clean-slate reports: the mouse stays put.
	if n := len(fg.host.got(fg.nodes["mouse"])); n != 0 {
		t.Errorf("%d mouse reports sent on a fresh attach", n)
	}
}

// Starting with a phone already attached releases whatever a previous,
// killed roadie may have left held -- once.
func TestGadgetCleanSlateOnStart(t *testing.T) {
	fg := newFakeGadget(t)
	fg.setState("configured")
	g := newGadgetHID(fg.paths, fg.host.open)
	g.refresh()
	for _, fn := range []string{"keyboard", "touch", "mouse"} {
		got := fg.host.got(fg.nodes[fn])
		if len(got) != 1 {
			t.Fatalf("%s: %d clean-slate reports, want 1", fn, len(got))
		}
		for i, b := range got[0] {
			if fn == "touch" && i == 6 {
				continue // slot 1's contact id
			}
			if b != 0 {
				t.Errorf("%s clean-slate report % x is not neutral", fn, got[0])
				break
			}
		}
	}
	fg.host.reset()
	g.refresh()
	if len(fg.host.got(fg.nodes["keyboard"])) != 0 {
		t.Error("clean slate sent twice")
	}
}

func TestGadgetShutdownReleasesHeld(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	g.KeyPress(0x04)
	g.MousePress(2)
	g.Touch([]TouchContact{{ID: 0, Tip: true, X: 10, Y: 10}, {ID: 1, Tip: true, X: 20, Y: 20}})
	fg.host.reset()
	g.Shutdown()
	if r := fg.host.last(fg.nodes["keyboard"]); r == nil || r[2] != 0 {
		t.Errorf("keyboard after shutdown: % x", r)
	}
	if r := fg.host.last(fg.nodes["mouse"]); r == nil || r[0] != 0 {
		t.Errorf("mouse after shutdown: % x", r)
	}
	if r := fg.host.last(fg.nodes["touch"]); r == nil || r[1] != 0 || r[7] != 0 || r[12] != 0 {
		t.Errorf("touch after shutdown: % x", r)
	}
}

func TestGadgetShutdownLeavesIdleMouseAlone(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	g.MouseMove(500, 500)
	fg.host.reset()
	g.Shutdown()
	if n := len(fg.host.got(fg.nodes["mouse"])); n != 0 {
		t.Errorf("shutdown sent %d mouse reports with no button down", n)
	}
}

func TestGadgetReopensReplacedNode(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	kbd := fg.nodes["keyboard"]
	fg.host.mu.Lock()
	fg.host.stale[kbd] = true
	fg.host.mu.Unlock()
	g.refresh()
	if fg.host.opens[kbd] != 2 {
		t.Errorf("keyboard opened %d times, want a reopen", fg.host.opens[kbd])
	}
}

func TestGadgetReopensAfterWriteError(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	kbd := fg.nodes["keyboard"]
	fg.host.setFail(kbd, syscall.EIO)
	if err := g.KeyPress(4); err == nil {
		t.Fatal("EIO not reported")
	}
	if g.Status() != HIDConnecting {
		t.Errorf("status %s after the node failed, want connecting", g.Status())
	}
	fg.host.setFail(kbd, nil)
	g.refresh()
	if g.Status() != HIDConnected || fg.host.opens[kbd] != 2 {
		t.Errorf("status %s, opens %d; want reconnected", g.Status(), fg.host.opens[kbd])
	}
}

func TestGadgetMissingThenAppears(t *testing.T) {
	root := t.TempDir()
	paths := gadgetPaths{sys: filepath.Join(root, "sys"), dev: filepath.Join(root, "dev")}
	host := newFakeHost()
	g := newGadgetHID(paths, host.open)
	g.refresh()
	if g.Status() != HIDDisconnected {
		t.Errorf("status %s with no gadget, want disconnected", g.Status())
	}
	if err := g.KeyPress(4); err == nil || !strings.Contains(err.Error(), "no keyboard") {
		t.Errorf("KeyPress with no gadget: %v", err)
	}
	fg := &fakeGadget{t: t, paths: paths, host: host, nodes: map[string]string{}}
	fg.write(filepath.Join(paths.gadget(), "UDC"), fakeUDC)
	fg.setState("configured")
	fg.addHID("keyboard", 8, "236:0", "hidg0")
	g.refresh()
	if g.Status() != HIDConnected {
		t.Errorf("status %s once the gadget exists", g.Status())
	}
	if err := g.Touch([]TouchContact{{ID: 0, Tip: true}}); err == nil {
		t.Error("touch accepted by a gadget without a touch function")
	}
	// Letting go of nothing is fine: /api/hid/release must still work.
	if err := g.Touch(nil); err != nil {
		t.Errorf("release on a gadget without touch: %v", err)
	}
	if err := g.MouseRelease(7); err != nil {
		t.Errorf("mouse release on a gadget without a mouse: %v", err)
	}
}

func TestGadgetResetSoftConnect(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	g.KeyPress(0xE1)
	var writes []string
	stop := make(chan struct{})
	done := make(chan struct{})
	path := fg.paths.udcAttr(fakeUDC, "soft_connect")
	go func() { // watch the attribute the way the kernel would see writes
		defer close(done)
		last := ""
		for {
			b, _ := os.ReadFile(path)
			if s := string(b); s != last && s != "" {
				writes = append(writes, s)
				last = s
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	if err := g.ResetHID(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	close(stop)
	<-done
	if strings.Join(writes, ",") != "disconnect,connect" {
		t.Errorf("soft_connect saw %q, want disconnect then connect", writes)
	}
	if g.kstate.held() {
		t.Error("held keys survived a re-enumeration")
	}
}

func TestGadgetResetRelayReopens(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	if err := g.ResetRelay(); err != nil {
		t.Fatal(err)
	}
	g.refresh()
	for _, fn := range []string{"keyboard", "mouse", "touch"} {
		if fg.host.opens[fg.nodes[fn]] != 2 {
			t.Errorf("%s not reopened", fn)
		}
	}
}

func TestGadgetTypeRejectsWholeText(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	if err := g.Type("abc€"); err == nil {
		t.Fatal("text with € accepted")
	}
	if n := len(fg.host.got(fg.nodes["keyboard"])); n != 0 {
		t.Errorf("%d reports typed before rejecting the text", n)
	}
	if err := g.Type("Hi"); err != nil {
		t.Fatal(err)
	}
	if last := fg.host.last(fg.nodes["keyboard"]); !bytes.Equal(last, make([]byte, 8)) {
		t.Errorf("typing ended with % x held", last)
	}
}

func TestGadgetSendAudio(t *testing.T) {
	g, _ := newTestGadgetHID(t, true)
	if err := g.SendAudio(make([]byte, 640)); err == nil {
		t.Error("SendAudio succeeded with no microphone")
	}
	m := newMicStream(48000, 1)
	g.SetMic(m)
	if err := g.SendAudio(make([]byte, 640)); err != nil {
		t.Fatal(err)
	}
	if n := len(m.queue); n != 960 {
		t.Errorf("20 ms of 16 kHz queued as %d samples at 48 kHz, want 960", n)
	}
}

// Everything at once, with the monitor running and the phone coming and
// going: for the race detector, and to show nothing deadlocks.
func TestGadgetConcurrentUse(t *testing.T) {
	fg := newFakeGadget(t)
	fg.setState("configured")
	g := newGadgetHID(fg.paths, fg.host.open)
	go g.Run()
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch (i + w) % 5 {
				case 0:
					g.MouseMove(i*10, i*20)
				case 1:
					g.MouseClick(1)
				case 2:
					g.Touch([]TouchContact{{ID: 0, Tip: i%2 == 0, X: i, Y: i}})
				case 3:
					g.KeyPress(4 + w)
					g.KeyRelease(4 + w)
				case 4:
					g.Status()
					g.BoardResponding()
				}
			}
		}(w)
	}
	for i := 0; i < 5; i++ {
		fg.setState("suspended")
		g.nudge()
		time.Sleep(2 * time.Millisecond)
		fg.setState("configured")
		g.nudge()
	}
	wg.Wait()
	g.Shutdown()
}

func TestGadgetFailedLiftIsRetried(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	touch := fg.nodes["touch"]
	if err := g.Touch([]TouchContact{{ID: 0, Tip: true, X: 100, Y: 100}, {ID: 1, Tip: true, X: 200, Y: 200}}); err != nil {
		t.Fatal(err)
	}
	fg.host.setFail(touch, errHostNotReading)
	if err := g.Touch([]TouchContact{{ID: 0, Tip: false, X: 100, Y: 100}, {ID: 1, Tip: false, X: 200, Y: 200}}); err == nil {
		t.Fatal("lift reported success while refused")
	}
	fg.host.setFail(touch, nil)
	g.refresh()
	if r := fg.host.last(touch); r[1] != 0 || r[7] != 0 {
		t.Errorf("fingers still down after the retried lift: % x", r)
	}
}

func TestGadgetFailedTouchDownNeverLands(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	touch := fg.nodes["touch"]
	fg.host.setFail(touch, errHostNotReading)
	if err := g.Touch([]TouchContact{{ID: 0, Tip: true, X: 1, Y: 1}}); err == nil {
		t.Fatal("touch-down reported success while refused")
	}
	fg.host.setFail(touch, nil)
	g.refresh()
	for _, r := range fg.host.got(touch) {
		if r[1] != 0 {
			t.Errorf("a refused touch-down was delivered later: % x", r)
		}
	}
}

func TestGadgetFailedKeyReleaseIsRetried(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	kbd := fg.nodes["keyboard"]
	g.KeyPress(0xE0)
	g.KeyPress(0x06)
	fg.host.setFail(kbd, errHostNotReading)
	if err := g.KeyRelease(0x06); err == nil {
		t.Fatal("release reported success while refused")
	}
	fg.host.setFail(kbd, nil)
	g.refresh()
	if r := fg.host.last(kbd); !bytes.Equal(r, []byte{0x01, 0, 0, 0, 0, 0, 0, 0}) {
		t.Errorf("after the retried release: % x, want ctrl alone", r)
	}
}

// A phone that sleeps and wakes has not re-enumerated: what it holds, it
// still holds, and a release owed to it must still arrive.
func TestGadgetResumeKeepsOwedRelease(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	kbd := fg.nodes["keyboard"]
	g.KeyPress(0xE1)
	fg.setState("suspended")
	g.refresh()
	fg.host.setFail(kbd, errHostNotReading)
	if err := g.KeyRelease(0xE1); err == nil {
		t.Fatal("release succeeded on a suspended bus")
	}
	g.refresh() // still suspended: nothing resent yet
	fg.host.setFail(kbd, nil)
	fg.setState("configured")
	g.refresh()
	if r := fg.host.last(kbd); r[0] != 0 {
		t.Errorf("after resume the phone was left with % x", r)
	}
	if g.anyDirty() {
		t.Error("owed release still pending after resume")
	}
}

// The gadget's Ethernet function losing carrier means the phone enumerated
// us again, even if every poll saw "configured": held state is forgotten.
func TestGadgetCarrierDropMeansReenumerated(t *testing.T) {
	fg := newFakeGadget(t)
	fg.write(filepath.Join(fg.paths.function("ecm.phone"), "ifname"), "phone0\n")
	counter := filepath.Join(fg.paths.sys, "class/net/phone0/carrier_down_count")
	fg.write(counter, "3\n")
	fg.setState("configured")
	g := newGadgetHID(fg.paths, fg.host.open)
	g.refresh()
	g.KeyPress(0xE1)
	fg.setState("suspended") // a replug seen only as suspended -> configured
	g.refresh()
	fg.write(counter, "4\n")
	fg.setState("configured")
	g.refresh()
	fg.host.reset()
	g.KeyPress(0x04)
	if r := fg.host.last(fg.nodes["keyboard"]); r[0] != 0 {
		t.Errorf("shift from before the replug rode along: % x", r)
	}
}

// A write that finds no host is proof of a new enumeration to come.
func TestGadgetNotConfiguredWriteMeansReenumerated(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	kbd := fg.nodes["keyboard"]
	g.KeyPress(0xE1)
	fg.host.setFail(kbd, errUSBNotConfigured)
	g.KeyPress(0x05) // the phone is mid-replug
	fg.host.setFail(kbd, nil)
	g.refresh() // still "configured" by the time we look
	fg.host.reset()
	g.KeyPress(0x04)
	if r := fg.host.last(kbd); r[0] != 0 {
		t.Errorf("stale shift after a missed replug: % x", r)
	}
}

func TestGadgetScrollZeroKeepsOwedRelease(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	mouse := fg.nodes["mouse"]
	g.MousePress(1)
	fg.host.setFail(mouse, errHostNotReading)
	g.MouseRelease(1)
	fg.host.setFail(mouse, nil)
	if err := g.MouseScroll(0); err != nil {
		t.Fatal(err)
	}
	if !g.anyDirty() {
		t.Fatal("scrolling by zero cleared a release still owed")
	}
	g.refresh()
	if r := fg.host.last(mouse); r[0] != 0 {
		t.Errorf("button left down: % x", r)
	}
	fg.host.reset()
	g.MouseScroll(1 << 40) // bounded, not billions of reports
	if n := len(fg.host.got(mouse)); n != 100 {
		t.Errorf("huge scroll sent %d reports, want 100", n)
	}
}

func TestGadgetCleanSlateRetried(t *testing.T) {
	fg := newFakeGadget(t)
	fg.setState("configured")
	fg.host.setFail(fg.nodes["touch"], errHostNotReading)
	g := newGadgetHID(fg.paths, fg.host.open)
	g.refresh() // the clean-slate lift cannot be sent
	fg.host.setFail(fg.nodes["touch"], nil)
	g.refresh()
	if got := fg.host.got(fg.nodes["touch"]); len(got) != 1 || got[0][1] != 0 {
		t.Errorf("clean-slate lift not retried: % x", got)
	}
}

func TestGadgetNothingSentAfterShutdown(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	g.Shutdown()
	fg.host.reset()
	if err := g.KeyPress(4); err == nil {
		t.Error("a key press was accepted after shutdown")
	}
	if n := len(fg.host.got(fg.nodes["keyboard"])); n != 0 {
		t.Errorf("%d reports sent after shutdown", n)
	}
}

func TestGadgetPartialSendSaysSo(t *testing.T) {
	g, fg := newTestGadgetHID(t, true)
	calls := 0
	g.mouse.mu.Lock()
	g.mouse.dev = &scriptedDev{fn: func(r []byte) error {
		calls++
		if calls == 2 {
			return errHostNotReading
		}
		return fg.host.devWrite(fg.nodes["mouse"], r)
	}}
	g.mouse.mu.Unlock()
	err := g.MouseClick(1)
	if err == nil || !strings.Contains(err.Error(), "1 of 2 reports sent") || !errors.Is(err, errHostNotReading) {
		t.Errorf("partial click error: %v", err)
	}
}
