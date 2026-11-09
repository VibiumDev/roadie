package main

import (
	"github.com/VibiumDev/roadie/internal/platform"
	"testing"
)

// The device's name in its station address comes from --device-name,
// which must fit an address and a URL.
func TestDeviceName(t *testing.T) {
	for _, ok := range []string{"phone", "iphone_1", "pixel-8a", "a", "abcdefghijklmnopqrstuvwxyz012345"} {
		if err := checkDeviceName(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Phone", "1phone", "iphone 1", "iphone/1", "_x", "abcdefghijklmnopqrstuvwxyz0123456"} {
		if err := checkDeviceName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	o := deviceOffer("iphone_1", platform.PlatformIOS, true, true, "mouse", " iPhone 15 Pro Max ", "iPhone 15 Pro Max", " 26.6 ")
	if o.Name != "iphone_1" || o.Kind != "phone" || o.Label != "iPhone 15 Pro Max" || o.Platform != "ios" || o.Model != "iPhone 15 Pro Max" || o.OSVersion != "26.6" {
		t.Errorf("offer: %+v", o)
	}
	if o := deviceOffer("", platform.PlatformAndroid, false, false, "", "", "", ""); o.Name != defaultDeviceName || o.Model != "" {
		t.Errorf("default name: %+v", o)
	}
}
