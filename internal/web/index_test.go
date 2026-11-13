package web

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
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
	if html := render(""); !strings.Contains(html, `<a href="/drive">Drive</a><a href="/view">View</a>`) || !strings.Contains(html, `<a href="/drive">/drive</a> <span>drive the target`) || !strings.Contains(html, `<a href="/view">/view</a> <span>watch the target`) {
		t.Error("the index should offer /drive and /view")
	}
	if html := render("iPhone 15 Pro Max"); strings.Contains(html, "iPhone 15 Pro Max · phone") {
		t.Error("a label equal to the model should not repeat it")
	}
}

// /drive drives and /view only watches: the config says so, and each
// page's toolbar leads to the other with the same query.
func TestDriveAndViewPages(t *testing.T) {
	s := &Server{Name: "roadie"}
	for _, c := range []struct {
		path  string
		watch bool
		link  string
	}{
		{"/drive?input=touch", false, `id="watchLink" class="tbtn" href="/view?input=touch"`},
		{"/view?input=touch", true, `id="driveLink" class="tbtn" href="/drive?input=touch"`},
		{"/view", true, `id="driveLink" class="tbtn" href="/drive"`},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		if c.watch {
			s.handleView(rec, req)
		} else {
			s.handleDrive(rec, req)
		}
		html := rec.Body.String()
		hasWatch := strings.Contains(html, `watch&#34;:true`) || strings.Contains(html, `"watch":true`)
		if hasWatch != c.watch || !strings.Contains(html, c.link) {
			t.Errorf("%s: watch %v, link present %v", c.path, hasWatch, strings.Contains(html, c.link))
		}
		if c.watch && !strings.HasPrefix(html, "<!DOCTYPE") && !strings.Contains(html, "<title>Watching") {
			t.Errorf("%s: title should say Watching", c.path)
		}
	}
}

// The viewer is told the device's name, which names the screenshots it
// saves: the label, else the model, else nothing.
func TestViewerConfigNamesTheDevice(t *testing.T) {
	for _, c := range []struct{ label, model, want string }{
		{"", "", ""},
		{"", "iPhone 15 Pro Max", "iPhone 15 Pro Max"},
		{"Test phone", "iPhone 15 Pro Max", "Test phone"},
	} {
		s := &Server{Name: "bench", Label: c.label, Model: c.model}
		rec := httptest.NewRecorder()
		s.handleDrive(rec, httptest.NewRequest(http.MethodGet, "/drive", nil))
		_, rest, _ := strings.Cut(rec.Body.String(), `data-config="`)
		raw, _, _ := strings.Cut(rest, `"`)
		var cfg struct{ Name string }
		if err := json.Unmarshal([]byte(html.UnescapeString(raw)), &cfg); err != nil {
			t.Fatalf("label %q, model %q: config: %v", c.label, c.model, err)
		}
		if cfg.Name != c.want {
			t.Errorf("label %q, model %q: the viewer's name is %q, want %q", c.label, c.model, cfg.Name, c.want)
		}
	}
}
