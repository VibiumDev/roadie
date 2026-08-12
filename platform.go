package main

import (
	"fmt"
	"strings"
)

// Platform names the operating system of the target device. Roadie cannot
// discover it — nothing about a capture stream or a USB HID link identifies
// the far end — but it changes how the target behaves, so it is worth
// stating once at launch rather than working around per symptom.
type Platform string

const (
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
	PlatformMac     Platform = "mac"
	PlatformWindows Platform = "windows"
	PlatformLinux   Platform = "linux"
)

// platformAliases maps what people actually type to the canonical name.
var platformAliases = map[string]Platform{
	"android": PlatformAndroid,
	"ios":     PlatformIOS,
	"iphone":  PlatformIOS,
	"ipad":    PlatformIOS,
	"mac":     PlatformMac,
	"macos":   PlatformMac,
	"osx":     PlatformMac,
	"darwin":  PlatformMac,
	"windows": PlatformWindows,
	"win":     PlatformWindows,
	"linux":   PlatformLinux,
}

// ParsePlatform normalizes a --platform value. Empty stays empty, meaning
// unspecified: Roadie then behaves exactly as it did before the flag existed.
func ParsePlatform(s string) (Platform, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", nil
	}
	if p, ok := platformAliases[s]; ok {
		return p, nil
	}
	return "", fmt.Errorf("unknown platform %q, expected one of: android, ios, mac, windows, linux", s)
}

// PointerSpace reports which coordinate space the target's absolute pointer
// addresses — the only place a wrong answer is silently destructive.
//
// "screen" means 0-32767 spans the target's own display; "frame" means it
// spans the whole captured frame, letterbox bars included.
//
// This is measured, not assumed. An iPhone mirroring into 1920x1080 maps the
// range across its own screen: sweeping X put the pointer at 25%, 50% and 75%
// of the phone's display, with no clamping at the pillarbox edges. A Pixel
// mirroring into the same frame does the opposite — a tap aimed with the
// frame mapping opened the app under it, while the screen mapping landed on
// empty wallpaper 35% away.
//
// Everything unverified keeps "frame", which is what Roadie has always done.
func (p Platform) PointerSpace() string {
	if p == PlatformIOS {
		return "screen"
	}
	return "frame"
}

// DefaultInputMode is the pointer mode that suits the platform, used when
// --input is not given. Empty means "no opinion", leaving the viewer to
// decide as before.
func (p Platform) DefaultInputMode() string {
	switch p {
	case PlatformAndroid, PlatformIOS:
		return "touch"
	case PlatformMac, PlatformWindows, PlatformLinux:
		return "mouse"
	}
	return ""
}
