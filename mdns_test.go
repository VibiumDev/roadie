package main

import "testing"

func TestMDNSHostname(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"roadie", "roadie"},
		{"Android", "android"},
		{"iPhone", "iphone"},
		{"ANDROID", "android"},
		{"Roadie-A", "roadie-a"},
	}
	for _, tt := range tests {
		// Browsers lowercase hostnames before resolving, and the responder
		// matches the queried name exactly, so whatever capitalization the
		// user passes must advertise as lowercase to be reachable.
		if got := MDNSHostname(tt.name); got != tt.want {
			t.Errorf("MDNSHostname(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
