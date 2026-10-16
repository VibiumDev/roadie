package main

import (
	"strings"
	"testing"
)

func TestDeviceHint(t *testing.T) {
	cases := map[string]string{
		"UGREEN 15389: UGREEN 15389;usb-0000:01:00.0-1": "UGREEN 15389",
		"bcm2835-isp;platform:bcm2835-isp":              "bcm2835-isp",
		"Plain Name":                                    "Plain Name",
	}
	for in, want := range cases {
		if got := deviceHint(in); got != want {
			t.Errorf("deviceHint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuggestCommand(t *testing.T) {
	var b strings.Builder
	suggestCommand(&b, []string{"UGREEN 15389: UGREEN;usb-1"}, []RelayInfo{{Serial: "AAA"}})
	one := b.String()
	if !strings.Contains(one, `--video "UGREEN 15389"`) {
		t.Errorf("single device: missing --video, got:\n%s", one)
	}
	if strings.Contains(one, "--port") || strings.Contains(one, "--relay") {
		t.Errorf("single device should not need --port or --relay, got:\n%s", one)
	}

	b.Reset()
	suggestCommand(&b, []string{"Cap A;usb-1", "Cap B;usb-2"},
		[]RelayInfo{{Serial: "AAA"}, {Serial: "BBB"}})
	two := b.String()
	for _, want := range []string{`--video "Cap A"`, `--video "Cap B"`,
		"--relay AAA", "--relay BBB", "--port 8080", "--port 8081"} {
		if !strings.Contains(two, want) {
			t.Errorf("two devices: missing %q, got:\n%s", want, two)
		}
	}
}
