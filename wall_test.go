package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseWallTargets(t *testing.T) {
	tests := []struct {
		name       string
		targets    string
		labels     string
		wantURLs   []string
		wantLabels []string
		wantErr    bool
	}{
		{
			name:       "bare host:port pairs",
			targets:    "roadie-a.local:8080,roadie-b.local:8081",
			wantURLs:   []string{"http://roadie-a.local:8080/view?minimal=1", "http://roadie-b.local:8081/view?minimal=1"},
			wantLabels: []string{"roadie-a", "roadie-b"},
		},
		{
			name:       "explicit labels",
			targets:    "192.0.2.10:8080,192.0.2.10:8081",
			labels:     "Pixel, iPhone",
			wantURLs:   []string{"http://192.0.2.10:8080/view?minimal=1", "http://192.0.2.10:8081/view?minimal=1"},
			wantLabels: []string{"Pixel", "iPhone"},
		},
		{
			name:       "absolute URLs keep their scheme",
			targets:    "https://roadie-a.local:8443",
			wantURLs:   []string{"https://roadie-a.local:8443/view?minimal=1"},
			wantLabels: []string{"roadie-a"},
		},
		{
			name:       "caller path and query are discarded",
			targets:    "roadie-a.local:8080/evil?x=1#frag",
			wantURLs:   []string{"http://roadie-a.local:8080/view?minimal=1"},
			wantLabels: []string{"roadie-a"},
		},
		{
			name:       "fewer labels than targets falls back to hostname",
			targets:    "a.local:8080,b.local:8081",
			labels:     "Pixel",
			wantURLs:   []string{"http://a.local:8080/view?minimal=1", "http://b.local:8081/view?minimal=1"},
			wantLabels: []string{"Pixel", "b"},
		},
		{
			name:       "surrounding whitespace is trimmed",
			targets:    " a.local:8080 , b.local:8081 ",
			wantURLs:   []string{"http://a.local:8080/view?minimal=1", "http://b.local:8081/view?minimal=1"},
			wantLabels: []string{"a", "b"},
		},
		{name: "empty", targets: "", wantErr: true},
		{name: "only separators", targets: ",,", wantErr: true},
		{name: "javascript scheme rejected", targets: "javascript:alert(1)", wantErr: true},
		{name: "file scheme rejected", targets: "file:///etc/passwd", wantErr: true},
		{name: "data scheme rejected", targets: "data:text/html,<script>", wantErr: true},
		{name: "over the target cap", targets: strings.Repeat("a.local:8080,", maxWallTargets+1), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseWallTargets(tt.targets, tt.labels)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseWallTargets(%q) error = %v, wantErr %v", tt.targets, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.wantURLs) {
				t.Fatalf("got %d targets, want %d", len(got), len(tt.wantURLs))
			}
			for i, want := range tt.wantURLs {
				if got[i].URL != want {
					t.Errorf("target %d URL = %q, want %q", i, got[i].URL, want)
				}
				if got[i].Label != tt.wantLabels[i] {
					t.Errorf("target %d label = %q, want %q", i, got[i].Label, tt.wantLabels[i])
				}
			}
		})
	}
}

func TestHandleWall(t *testing.T) {
	srv := &Server{}

	t.Run("renders one iframe per target", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/wall?targets=a.local:8080,b.local:8081&labels=Pixel,iPhone", nil)
		srv.handleWall(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			`src="http://a.local:8080/view?minimal=1"`,
			`src="http://b.local:8081/view?minimal=1"`,
			"<h2>Pixel</h2>",
			"<h2>iPhone</h2>",
			"repeat(2, minmax(0, 1fr))",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("body missing %q", want)
			}
		}
	})

	t.Run("minimal=1 drops captions and padding", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/wall?targets=a.local:8080&minimal=1", nil)
		srv.handleWall(rec, req)

		body := rec.Body.String()
		if strings.Contains(body, "<h2>") {
			t.Error("minimal=1 should not render captions")
		}
		if !strings.Contains(body, "gap:0px; padding:0px;") {
			t.Error("minimal=1 should remove gap and padding")
		}
	})

	t.Run("cols overrides the grid width", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/wall?targets=a.local:8080,b.local:8081,c.local:8082&cols=2", nil)
		srv.handleWall(rec, req)

		if !strings.Contains(rec.Body.String(), "repeat(2, minmax(0, 1fr))") {
			t.Error("cols=2 not applied")
		}
	})

	t.Run("label markup is escaped", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/wall?targets=a.local:8080&labels=%3Cscript%3Ealert(1)%3C/script%3E", nil)
		srv.handleWall(rec, req)

		body := rec.Body.String()
		if strings.Contains(body, "<script>alert(1)</script>") {
			t.Error("label was not escaped")
		}
		if !strings.Contains(body, "&lt;script&gt;") {
			t.Error("expected escaped label in output")
		}
	})

	t.Run("missing targets returns usage", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.handleWall(rec, httptest.NewRequest(http.MethodGet, "/wall", nil))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "targets=roadie-a.local:8080") {
			t.Error("usage page should show an example")
		}
	})
}
