//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A sysfs tree like a Pi with a UGREEN dongle and the gadget: the dongle's
// video and sound card share a USB device; the gadget's card does not.
func fakeSoundSysfs(t *testing.T) string {
	t.Helper()
	sys := t.TempDir()
	mk := func(dir string) {
		if err := os.MkdirAll(filepath.Join(sys, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, s string) {
		if err := os.WriteFile(filepath.Join(sys, path), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, name string) {
		mk(filepath.Dir(name))
		if err := os.Symlink(filepath.Join(sys, target), filepath.Join(sys, name)); err != nil {
			t.Fatal(err)
		}
	}
	dongle := "devices/usb2/2-1"
	mk(dongle + "/2-1:1.0")
	mk(dongle + "/2-1:1.2")
	write(dongle+"/product", "UGREEN 15389\n")
	mk("devices/usb1/1-4/1-4:1.4")
	write("devices/usb1/1-4/product", "Roadie-HID\n")
	mk("devices/gadget/gadget.0")
	link(dongle+"/2-1:1.0", "class/video4linux/video0/device")
	for _, c := range []struct{ card, id, dev string }{
		{"card1", "RoadieHID", "devices/usb1/1-4/1-4:1.4"},
		{"card3", "U15389", dongle + "/2-1:1.2"},
		{"card4", "UAC2Gadget", "devices/gadget/gadget.0"},
	} {
		link(c.dev, "class/sound/"+c.card+"/device")
		write("class/sound/"+c.card+"/id", c.id+"\n")
	}
	return sys
}

func TestDongleSoundCard(t *testing.T) {
	sys := fakeSoundSysfs(t)
	if n, name, err := dongleSoundCard(sys, "video0", ""); err != nil || n != 3 || name != "UGREEN 15389" {
		t.Errorf("by USB device: card %d %q %v; want card 3, the dongle", n, name, err)
	}
	if n, _, err := dongleSoundCard(sys, "video0", "roadie-hid"); err != nil || n != 1 {
		t.Errorf("by --audio name: card %d %v; want card 1", n, err)
	}
	if n, _, err := dongleSoundCard(sys, "video0", "uac2gadget"); err != nil || n != 4 {
		t.Errorf("by --audio id: card %d %v; want card 4", n, err)
	}
	if _, _, err := dongleSoundCard(sys, "video9", ""); err != errNoDongleCard {
		t.Errorf("unknown video node: %v; want errNoDongleCard (fall back to the driver)", err)
	}
	if _, _, err := dongleSoundCard(sys, "", ""); err != errNoDongleCard {
		t.Errorf("no video node: %v", err)
	}
}
