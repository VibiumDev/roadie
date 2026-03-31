//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// resetCaptureUSB performs a USB unbind/rebind for the capture device,
// simulating a physical unplug/replug. This forces HDMI re-negotiation.
func resetCaptureUSB() error {
	usbPath, err := findCaptureUSBDevice()
	if err != nil {
		return fmt.Errorf("find USB device: %w", err)
	}

	devID := filepath.Base(usbPath)
	const usbDriverPath = "/sys/bus/usb/drivers/usb"

	if err := sysfsWrite(filepath.Join(usbDriverPath, "unbind"), devID); err != nil {
		return fmt.Errorf("unbind %s: %w", devID, err)
	}

	time.Sleep(2 * time.Second)

	if err := sysfsWrite(filepath.Join(usbDriverPath, "bind"), devID); err != nil {
		return fmt.Errorf("rebind %s: %w", devID, err)
	}

	return nil
}

// findCaptureUSBDevice locates the USB device sysfs path for the first
// video4linux device that looks like an external capture dongle.
func findCaptureUSBDevice() (string, error) {
	entries, err := filepath.Glob("/sys/class/video4linux/video*")
	if err != nil {
		return "", err
	}

	skipKeywords := []string{"bcm2835", "rpi-", "integrated", "built-in"}

	for _, entry := range entries {
		nameBytes, err := os.ReadFile(filepath.Join(entry, "name"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(nameBytes))
		lower := strings.ToLower(name)

		skip := false
		for _, kw := range skipKeywords {
			if strings.Contains(lower, kw) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		// Resolve the device symlink to get the full sysfs path.
		devPath, err := filepath.EvalSymlinks(filepath.Join(entry, "device"))
		if err != nil {
			continue
		}

		// Walk up from the interface (e.g., 1-1.1:1.0) to the USB device (1-1.1).
		usbDev := parentUSBDevice(devPath)
		if usbDev != "" {
			return usbDev, nil
		}
	}

	return "", fmt.Errorf("no USB capture device found in sysfs")
}

// sysfsWrite writes a string to a sysfs file. Unlike os.WriteFile, it opens
// with O_WRONLY only (no O_CREATE/O_TRUNC which sysfs doesn't support).
func sysfsWrite(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	f.Close()
	return err
}

// parentUSBDevice walks up from the USB interface (e.g., .../1-1.1/1-1.1:1.0)
// to the USB device directory (e.g., .../1-1.1) which has the driver symlink.
func parentUSBDevice(ifacePath string) string {
	base := filepath.Base(ifacePath)
	if strings.Contains(base, ":") {
		// Interface path — parent directory is the USB device.
		return filepath.Dir(ifacePath)
	}
	// Already at device level.
	if _, err := os.Stat(filepath.Join(ifacePath, "driver")); err == nil {
		return ifacePath
	}
	return ""
}
