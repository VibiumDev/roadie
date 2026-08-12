package main

import "testing"

func TestParsePlatform(t *testing.T) {
	tests := []struct {
		in      string
		want    Platform
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "android", want: PlatformAndroid},
		{in: "ios", want: PlatformIOS},
		{in: "iphone", want: PlatformIOS},
		{in: "ipad", want: PlatformIOS},
		{in: "mac", want: PlatformMac},
		{in: "macos", want: PlatformMac},
		{in: "windows", want: PlatformWindows},
		{in: "linux", want: PlatformLinux},
		{in: "  iOS  ", want: PlatformIOS}, // case and padding, as typed
		{in: "pixel", wantErr: true},       // a device, not a platform
		{in: "ios ipad", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParsePlatform(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParsePlatform(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("ParsePlatform(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPlatformPointerSpace(t *testing.T) {
	// iOS addresses its own display; everything else addresses the whole
	// captured frame, which is the behaviour Roadie has always had. Getting
	// this backwards confines the pointer to the middle of a mirrored phone
	// screen, so it is worth pinning per platform.
	tests := []struct {
		p    Platform
		want string
	}{
		{PlatformIOS, "screen"},
		{PlatformAndroid, "frame"},
		{PlatformMac, "frame"},
		{PlatformWindows, "frame"},
		{PlatformLinux, "frame"},
		{"", "frame"},
	}
	for _, tt := range tests {
		if got := tt.p.PointerSpace(); got != tt.want {
			t.Errorf("Platform(%q).PointerSpace() = %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestPlatformDefaultInputMode(t *testing.T) {
	tests := []struct {
		p    Platform
		want string
	}{
		{PlatformAndroid, "touch"},
		{PlatformIOS, "touch"},
		{PlatformMac, "mouse"},
		{PlatformWindows, "mouse"},
		{PlatformLinux, "mouse"},
		{"", ""}, // unspecified leaves the viewer to decide
	}
	for _, tt := range tests {
		if got := tt.p.DefaultInputMode(); got != tt.want {
			t.Errorf("Platform(%q).DefaultInputMode() = %q, want %q", tt.p, got, tt.want)
		}
	}
}
