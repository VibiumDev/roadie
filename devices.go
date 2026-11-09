package main

import (
	"os"
	"strings"
)

// The devices on the host as the rest of Roadie sees them: a media
// device's name and label, the V4L2 node behind it, and the sysfs reads
// the USB code shares.

// deviceInfo holds both the friendly name and the internal label (UID) of a device.
type deviceInfo struct {
	Name  string // friendly name (e.g. "USB Video")
	Label string // internal UID used for driver matching
}

// VideoNode returns the V4L2 node backing this device (e.g. "video0").
//
// The mediadevices camera driver builds Linux labels as "<discovered>;<node>",
// where the first segment is whichever path it enumerated the device through.
// USB dongles are found via /dev/v4l/by-id, so for them that segment is a
// by-id name and only the trailing one is the node:
//
//	usb-Example_HDMI_Capture_B-video-index0;video2
//	video10;video10
//
// Returns empty on other platforms or for labels that don't end in a node.
func (d deviceInfo) VideoNode() string {
	node := d.Label
	if i := strings.LastIndex(node, ";"); i >= 0 {
		node = node[i+1:]
	}
	if !strings.HasPrefix(node, "video") {
		return ""
	}
	return node
}

func readTrim(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.TrimRight(string(b), "\x00")), nil
}
