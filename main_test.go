package main

import (
	"github.com/VibiumDev/roadie/internal/hid"
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
	suggestCommand(&b, []string{"UGREEN 15389: UGREEN;usb-1"}, []hid.RelayInfo{{Serial: "AAA"}})
	one := b.String()
	if !strings.Contains(one, `--video "UGREEN 15389"`) {
		t.Errorf("single device: missing --video, got:\n%s", one)
	}
	if strings.Contains(one, "--port") || strings.Contains(one, "--relay") {
		t.Errorf("single device should not need --port or --relay, got:\n%s", one)
	}

	b.Reset()
	suggestCommand(&b, []string{"Cap A;usb-1", "Cap B;usb-2"},
		[]hid.RelayInfo{{Serial: "AAA"}, {Serial: "BBB"}})
	two := b.String()
	for _, want := range []string{`--video "Cap A"`, `--video "Cap B"`,
		"--relay AAA", "--relay BBB", "--port 8080", "--port 8081"} {
		if !strings.Contains(two, want) {
			t.Errorf("two devices: missing %q, got:\n%s", want, two)
		}
	}
}

// The soundcheck page opens the visitor's microphone as soon as it loads.
// Where it sends it must not come from the page's address, or a link could
// have it streamed to any host.
func TestSoundcheckPageStreamsToItsOwnHostOnly(t *testing.T) {
	page := string(soundcheckPage)
	if n := strings.Count(page, "new WebSocket("); n != 1 {
		t.Fatalf("the page opens %d WebSockets, want the one to /api/mic", n)
	}
	if !strings.Contains(page, "const target = location.host;") || !strings.Contains(page, "new WebSocket('ws://' + target + '/api/mic')") {
		t.Error("the microphone's WebSocket is not built from location.host alone")
	}
	// The address may say where to report progress, and nothing else.
	for _, line := range strings.Split(page, "\n") {
		if strings.Contains(line, "params.get(") && !strings.Contains(line, "params.get('report')") {
			t.Errorf("the page reads its address for more than the report URL: %s", strings.TrimSpace(line))
		}
	}
}
