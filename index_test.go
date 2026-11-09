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
		data := map[string]any{"Title": "Roadie", "Name": "roadie", "Host": "pi", "Platform": "ios", "Label": label, "Input": "mouse", "Stations": []map[string]any(nil)}
		if err := pages.ExecuteTemplate(&out, "index", data); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if html := render("Dev iPhone 15"); !strings.Contains(html, "<th>Device</th><td>Dev iPhone 15 <span class=\"muted\">phone</span></td>") {
		t.Errorf("labelled device row missing:\n%s", html[strings.Index(html, "This host"):][:400])
	}
	if html := render(""); !strings.Contains(html, "no label (--label)") {
		t.Error("the unlabelled device should say how to label it")
	}
}
