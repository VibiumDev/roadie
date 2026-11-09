package main

import (
	"strings"
	"testing"
)

// The index page's This host section names the device by its label when
// Roadie has one, and says how to give it one when it has none.
func TestIndexShowsDeviceLabel(t *testing.T) {
	render := func(label string) string {
		var out strings.Builder
		data := map[string]any{"Title": "Roadie", "Name": "roadie", "Host": "pi", "Platform": "ios", "Label": label, "Model": "iPhone 15 Pro Max", "OSVersion": "26.6", "Input": "mouse", "Stations": []map[string]any(nil)}
		if err := pages.ExecuteTemplate(&out, "index", data); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if html := render("Dev iPhone 15"); !strings.Contains(html, "<th>Device</th><td>Dev iPhone 15 <span class=\"muted\">iPhone 15 Pro Max · phone</span></td>") || !strings.Contains(html, "<th>Platform</th><td>ios 26.6</td>") {
		t.Errorf("labelled device row missing:\n%s", html[strings.Index(html, "This host"):][:500])
	}
	if html := render(""); !strings.Contains(html, "<th>Device</th><td>iPhone 15 Pro Max <span class=\"muted\">phone</span></td>") {
		t.Error("without a label the model names the device")
	}
	if html := render("iPhone 15 Pro Max"); strings.Contains(html, "iPhone 15 Pro Max · phone") {
		t.Error("a label equal to the model should not repeat it")
	}
}
