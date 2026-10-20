package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, method, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler(page, white).ServeHTTP(rec, req)
	return rec
}

func TestServesThePage(t *testing.T) {
	for _, path := range []string{"/", "/?seconds=11", "/index.html"} {
		rec := get(t, "GET", path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		if !bytes.Equal(rec.Body.Bytes(), page) {
			t.Fatalf("GET %s: body isn't the page", path)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("GET %s: Content-Type %q", path, ct)
		}
		if pp := rec.Header().Get("Permissions-Policy"); pp != "microphone=(self)" {
			t.Errorf("GET %s: Permissions-Policy %q", path, pp)
		}
	}
}

func TestServesTheWhitePage(t *testing.T) {
	rec := get(t, "GET", "/white", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), white) {
		t.Fatalf("GET /white: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("GET /white: Content-Type %q", ct)
	}
	if rec.Header().Get("ETag") == get(t, "GET", "/", nil).Header().Get("ETag") {
		t.Errorf("the two pages share an ETag")
	}
}

func TestServesNothingElse(t *testing.T) {
	for _, path := range []string{"/main.go", "/README.md", "/white.html", "/white/", "/../go.mod", "/x/", "//evil.example/"} {
		if rec := get(t, "GET", path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, rec.Code)
		}
	}
	if rec := get(t, "POST", "/", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /: %d, want 405", rec.Code)
	}
}

func TestRevalidates(t *testing.T) {
	etag := get(t, "GET", "/", nil).Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if rec := get(t, "GET", "/", map[string]string{"If-None-Match": etag}); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d, want 304", rec.Code)
	}
}

// The page's own content has to fit the policy it is served under: inline
// script and style, a data: icon, and nothing loaded from anywhere.
func TestPageFitsItsPolicy(t *testing.T) {
	for name, p := range map[string][]byte{"index.html": page, "white.html": white} {
		for _, banned := range []string{"<script src", "<link rel=\"stylesheet\"", "@import", "fetch(", "XMLHttpRequest"} {
			if strings.Contains(string(p), banned) {
				t.Errorf("%s uses %q, which the Content-Security-Policy blocks", name, banned)
			}
		}
	}
}
