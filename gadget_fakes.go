package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Fakes for the USB gadget: a sysfs tree and HID device nodes that record
// what the gadget writes. They stand in for the hardware in this package's
// tests and in the server's, so they are not test files themselves.

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
	t     testing.TB
	paths gadgetPaths
	host  *fakeHost
	nodes map[string]string // function name -> /dev path
}

const fakeUDC = "fe980000.usb"

func newFakeGadget(t testing.TB) *fakeGadget {
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
func newTestGadgetHID(t testing.TB, attached bool) (*GadgetHID, *fakeGadget) {
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
