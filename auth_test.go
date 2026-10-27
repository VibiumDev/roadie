package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
}

func newTokenAuth(t *testing.T) (*Auth, *Server) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a, err := NewAuth(AuthToken, "")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := tokenPath()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("token file mode %v, want 0600", st.Mode().Perm())
	}
	if !strings.HasPrefix(a.token, tokenPrefix) || len(a.token) != len(tokenPrefix)+64 {
		t.Errorf("token %q has the wrong shape", a.token)
	}
	return a, &Server{Auth: a, Buf: &FrameBuffer{}}
}

func TestTokenAuthRefuses(t *testing.T) {
	a, _ := newTokenAuth(t)
	h := a.Wrap(okHandler())

	// A browser is sent to the login page, with its destination kept.
	req := httptest.NewRequest(http.MethodGet, "/view?input=touch", nil)
	req.Header.Set("Accept", "text/html,*/*")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") {
		t.Errorf("browser got %d %s", rec.Code, rec.Header().Get("Location"))
	}
	next, _ := url.ParseQuery(strings.TrimPrefix(rec.Header().Get("Location"), "/login?"))
	if next.Get("next") != "/view?input=touch" {
		t.Errorf("next = %q", next.Get("next"))
	}

	// An agent gets a 401 that says what to send.
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("agent got %d", rec.Code)
	}

	// A wrong bearer token is refused, the right one passes.
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Authorization", "Bearer roadie_local_wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong bearer got %d", rec.Code)
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("right bearer got %d", rec.Code)
	}
}

func TestLoginCookie(t *testing.T) {
	a, s := newTokenAuth(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.Handle("/", okHandler())
	h := a.Wrap(mux)

	// The page itself needs no login.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login?next=/view", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="token"`) {
		t.Fatalf("login page: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `value="/view"`) {
		t.Error("next not carried into the form")
	}

	// A wrong token stays on the page.
	form := url.Values{"token": {"nope"}, "next": {"/view"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Wrong token") || len(rec.Result().Cookies()) != 0 {
		t.Errorf("wrong token: %d, cookies %v", rec.Code, rec.Result().Cookies())
	}

	// The right token sets the cookie and goes on to next.
	form.Set("token", a.token)
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/view" {
		t.Fatalf("login: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != sessionDays*24*3600 {
		t.Fatalf("cookie %+v", cookie)
	}
	if cookie.Value == a.token {
		t.Error("the cookie holds the token itself")
	}

	// The cookie gets in; a WebSocket from another origin does not.
	req = httptest.NewRequest(http.MethodGet, "/view", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("cookie got %d", rec.Code)
	}
	ws := httptest.NewRequest(http.MethodGet, "/api/hid/ws", nil)
	ws.Host = "roadie.local:8080"
	ws.AddCookie(cookie)
	ws.Header.Set("Upgrade", "websocket")
	ws.Header.Set("Origin", "http://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, ws)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin websocket got %d", rec.Code)
	}
	ws.Header.Set("Origin", "http://roadie.local:8080")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, ws)
	if rec.Code != http.StatusOK {
		t.Errorf("same-origin websocket got %d", rec.Code)
	}

	// Rotating the token signs the cookie out.
	path, _ := tokenPath()
	if _, err := writeNewToken(path); err != nil {
		t.Fatal(err)
	}
	a2, err := NewAuth(AuthToken, "")
	if err != nil {
		t.Fatal(err)
	}
	if a2.token == a.token {
		t.Error("rotate kept the token")
	}
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	a2.Wrap(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("old cookie after rotate got %d", rec.Code)
	}

	// Logout clears it.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/logout", nil))
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout did not clear the cookie")
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"/view": "/view", "": "/", "//evil": "/", "http://x": "/", "/a?b=c": "/a?b=c", "/x\r\n": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTailscaleAuth(t *testing.T) {
	a, err := NewAuth(AuthTailscale, "you@example.com, tag:ci")
	if err != nil {
		t.Fatal(err)
	}
	a.whois = func(addr string) (string, []string, error) {
		switch {
		case strings.HasPrefix(addr, "100.64.0.1:"):
			return "you@example.com", nil, nil
		case strings.HasPrefix(addr, "100.64.0.2:"):
			return "someone@example.com", nil, nil
		case strings.HasPrefix(addr, "100.64.0.3:"):
			return "", []string{"tag:ci"}, nil
		}
		return "", nil, errors.New("no such peer")
	}
	h := a.Wrap(okHandler())
	get := func(remote string, header string) int {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.RemoteAddr = remote
		if header != "" {
			req.Header.Set("Tailscale-User-Login", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := get("100.64.0.1:5000", ""); c != 200 {
		t.Errorf("allowed login got %d", c)
	}
	if c := get("100.64.0.2:5000", ""); c != 403 {
		t.Errorf("other login got %d", c)
	}
	if c := get("100.64.0.3:5000", ""); c != 200 {
		t.Errorf("allowed tag got %d", c)
	}
	if c := get("192.168.1.9:5000", ""); c != 403 {
		t.Errorf("LAN address got %d", c)
	}
	// The header is only trusted when Roadie listens on localhost.
	if c := get("127.0.0.1:5000", "you@example.com"); c != 403 {
		t.Errorf("header on a non-loopback listener got %d", c)
	}
	a.Loopback = true
	if c := get("127.0.0.1:5000", "you@example.com"); c != 200 {
		t.Errorf("header on a loopback listener got %d", c)
	}
	if c := get("127.0.0.1:5000", "stranger@example.com"); c != 403 {
		t.Errorf("stranger's header got %d", c)
	}

	// With no --allow, anyone on the tailnet is fine but a LAN address is not.
	anyone, _ := NewAuth(AuthTailscale, "")
	anyone.whois = a.whois
	h = anyone.Wrap(okHandler())
	if c := get("100.64.0.2:5000", ""); c != 200 {
		t.Errorf("tailnet login with no allow list got %d", c)
	}
	if c := get("192.168.1.9:5000", ""); c != 403 {
		t.Errorf("LAN address with no allow list got %d", c)
	}
}

func TestAuthModes(t *testing.T) {
	for _, mode := range []string{AuthUnset, AuthNone} {
		a, err := NewAuth(mode, "")
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		a.Wrap(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("mode %q got %d", mode, rec.Code)
		}
	}
	if _, err := NewAuth("password", ""); err == nil {
		t.Error("unknown mode accepted")
	}
	if _, err := NewAuth(AuthNone, "you@example.com"); err == nil {
		t.Error("--allow without tailscale accepted")
	}
	for in, want := range map[string]bool{"": false, "127.0.0.1": true, "localhost": true, "::1": true, "0.0.0.0": false, "10.1.10.104": false} {
		if got := isLoopbackAddr(in); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v", in, got)
		}
	}
}
