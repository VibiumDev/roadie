package web

import (
	"context"
	"errors"
	"github.com/VibiumDev/roadie/internal/audio"
	"github.com/VibiumDev/roadie/internal/capture"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
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
	path, _ := TokenPath()
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
	return a, &Server{Auth: a, Buf: &capture.FrameBuffer{}}
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
	path, _ := TokenPath()
	if _, err := WriteNewToken(path); err != nil {
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
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Host = "localhost:8080"
		rec := httptest.NewRecorder()
		a.Wrap(okHandler()).ServeHTTP(rec, req)
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
		if got := IsLoopbackAddr(in); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v", in, got)
		}
	}
}

// A page on another site rides on whatever lets its visitor in: the cookie,
// the tailnet address, or nothing where nothing is asked. In every mode a
// request that changes something, or opens a socket, is refused when its
// Origin is not the host it was sent to.
func TestOtherOriginsAreRefused(t *testing.T) {
	token, _ := newTokenAuth(t)
	unset, _ := NewAuth(AuthUnset, "")
	none, _ := NewAuth(AuthNone, "")
	tailnet, _ := NewAuth(AuthTailscale, "")
	tailnet.whois = func(string) (string, []string, error) { return "member", nil, nil }
	const host, evil = "localhost:8080", "https://evil.example"
	for name, a := range map[string]*Auth{"unset": unset, "none": none, "tailscale": tailnet, "token": token} {
		h := a.Wrap(okHandler())
		do := func(method, origin string, socket bool) int {
			req := httptest.NewRequest(method, "/api/hid/type", strings.NewReader(`{"text":"x"}`))
			req.Host = host
			req.Header.Set("Content-Type", "text/plain") // what a form on another site can send
			if a.Mode == AuthToken {
				req.AddCookie(&http.Cookie{Name: sessionCookie, Value: a.sessionValue()})
			}
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			if socket {
				req.Header.Set("Upgrade", "websocket")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec.Code
		}
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
			if c := do(method, evil, false); c != http.StatusForbidden {
				t.Errorf("%s: %s from %s got %d", name, method, evil, c)
			}
		}
		if c := do(http.MethodGet, evil, true); c != http.StatusForbidden {
			t.Errorf("%s: a WebSocket from %s got %d", name, evil, c)
		}
		// A sandboxed frame or a file says "null", and another port of
		// this host is another origin.
		for _, origin := range []string{"null", "http://localhost:9999", "http://localhost"} {
			if c := do(http.MethodPost, origin, false); c != http.StatusForbidden {
				t.Errorf("%s: POST from %q got %d", name, origin, c)
			}
		}
		// This origin, and a client that is no browser and sends none.
		for _, origin := range []string{"http://" + host, "https://" + host, "http://LOCALHOST:8080", ""} {
			if c := do(http.MethodPost, origin, false); c != http.StatusOK {
				t.Errorf("%s: POST from %q got %d", name, origin, c)
			}
			if c := do(http.MethodGet, origin, true); c != http.StatusOK {
				t.Errorf("%s: a WebSocket from %q got %d", name, origin, c)
			}
		}
		// A read is left alone: the browser keeps the answer from the page.
		if c := do(http.MethodGet, evil, false); c != http.StatusOK {
			t.Errorf("%s: GET from %s got %d", name, evil, c)
		}
	}
	// A bearer token is not something a browser sends by itself, so it
	// gets in from anywhere, and the sockets' own check never sees its
	// Origin.
	var seen http.Header
	h := token.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Header.Clone() }))
	for _, socket := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodPost, "/api/hid/type", nil)
		req.Host = host
		req.Header.Set("Authorization", "Bearer "+token.token)
		req.Header.Set("Origin", evil)
		if socket {
			req.Method = http.MethodGet
			req.Header.Set("Upgrade", "websocket")
		}
		rec := httptest.NewRecorder()
		seen = nil
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || seen == nil || seen.Get("Origin") != "" {
			t.Errorf("bearer from %s (socket %v): %d, Origin passed on: %q", evil, socket, rec.Code, seen.Get("Origin"))
		}
	}
}

// Without --auth Roadie serves this machine only. A page elsewhere can
// point a name of its own at 127.0.0.1, so any other Host is refused.
func TestUnsetModeAnswersToLocalhostOnly(t *testing.T) {
	get := func(a *Auth, host string) int {
		req := httptest.NewRequest(http.MethodGet, "/snapshot", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		a.Wrap(okHandler()).ServeHTTP(rec, req)
		return rec.Code
	}
	unset, _ := NewAuth(AuthUnset, "")
	for host, want := range map[string]int{
		"localhost:8080": 200, "localhost": 200, "LocalHost:8080": 200, "127.0.0.1:8080": 200, "127.0.0.2:8080": 200, "[::1]:8080": 200, "[::1]": 200,
		"rebind.evil.example:8080": 403, "localhost.evil.example": 403, "192.168.1.5:8080": 403, "roadie.local:8080": 403, "": 403,
	} {
		if c := get(unset, host); c != want {
			t.Errorf("Host %q got %d, want %d", host, c, want)
		}
	}
	// The other modes serve whatever name the machine is reached by.
	none, _ := NewAuth(AuthNone, "")
	if c := get(none, "roadie.local:8080"); c != 200 {
		t.Errorf("--auth none, Host roadie.local: %d", c)
	}
}

// The sockets check Origin themselves too, so the rule holds wherever the
// mux is served: behind Wrap, and through a station's tunnel, whose relay
// sends no Origin.
func TestSocketsRefuseAnotherOrigin(t *testing.T) {
	resetBidiState()
	s, mux := newTestServer()
	s.HID = &keyHID{}
	s.AudioBroadcast = audio.NewAudioBroadcaster()
	s.AudioBroadcast.SetParams(audio.AudioParams{SampleRate: 48000, Channels: 2, Format: "f32-interleaved"})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base := "ws" + strings.TrimPrefix(ts.URL, "http")
	for _, path := range []string{"/api/hid/ws", "/session", "/audio", "/api/mic"} {
		from := &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}}
		if c, resp, err := websocket.Dial(ctx, base+path, from); err == nil {
			c.CloseNow()
			t.Errorf("%s opened from another origin", path)
		} else if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s from another origin: %v", path, err)
		}
		for _, opts := range []*websocket.DialOptions{nil, {HTTPHeader: http.Header{"Origin": {ts.URL}}}} {
			c, _, err := websocket.Dial(ctx, base+path, opts)
			if err != nil {
				t.Errorf("%s with %+v: %v", path, opts, err)
				continue
			}
			c.CloseNow()
		}
	}
}
