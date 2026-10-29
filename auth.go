package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Local access control, chosen with --auth:
//
//	unset      no checks, and Roadie listens on localhost only
//	token      the local admin token: /login for browsers, Bearer for agents
//	tailscale  whoever the local tailscaled says the connection belongs to
//	none       no checks on any address, chosen deliberately
//
// Remote sessions through a station need a hold grant whatever this says.

const (
	AuthUnset     = ""
	AuthToken     = "token"
	AuthTailscale = "tailscale"
	AuthNone      = "none"

	tokenPrefix   = "roadie_local_"
	sessionCookie = "roadie_session"
	sessionDays   = 30
)

// Auth checks every request against the chosen mode.
type Auth struct {
	Mode  string
	Allow []string // tailscale: logins and tags allowed; empty means any tailnet identity
	// Loopback says Roadie listens on localhost only, which is when the
	// Tailscale-User-Login header from tailscale serve can be trusted.
	Loopback bool
	// Secure says pages are served over TLS, so cookies are marked Secure.
	Secure bool

	token string
	whois func(addr string) (login string, tags []string, err error)

	mu       sync.Mutex
	failures map[string][]time.Time // login failures by address
	cache    map[string]whoisEntry
}

type whoisEntry struct {
	login string
	tags  []string
	err   error
	at    time.Time
}

// NewAuth sets up the mode, creating the local token on first use.
func NewAuth(mode, allow string) (*Auth, error) {
	a := &Auth{Mode: mode, failures: map[string][]time.Time{}, cache: map[string]whoisEntry{}}
	for _, s := range strings.Split(allow, ",") {
		if s = strings.TrimSpace(s); s != "" {
			a.Allow = append(a.Allow, s)
		}
	}
	switch mode {
	case AuthUnset, AuthNone:
	case AuthToken:
		path, err := tokenPath()
		if err != nil {
			return nil, err
		}
		if a.token, err = loadOrCreateToken(path); err != nil {
			return nil, err
		}
	case AuthTailscale:
		a.whois = tailscaleWhoIs
	default:
		return nil, fmt.Errorf("--auth must be token, tailscale or none, got %q", mode)
	}
	if len(a.Allow) > 0 && mode != AuthTailscale {
		return nil, errors.New("--allow only applies with --auth tailscale")
	}
	return a, nil
}

// tokenPath is where the local admin token lives: a file only Roadie's
// user can read, next to the saved crops.
func tokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "roadie", "token"), nil
}

// loadOrCreateToken reads the token file, making a new token if there is
// none.
func loadOrCreateToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		t := strings.TrimSpace(string(b))
		if strings.HasPrefix(t, tokenPrefix) && len(t) > len(tokenPrefix) {
			return t, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return writeNewToken(path)
}

// writeNewToken replaces the token file with a fresh token: the prefix and
// 32 random bytes in hex.
func writeNewToken(path string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	t := tokenPrefix + hex.EncodeToString(raw[:])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return t, nil
}

// sessionValue is what the login cookie holds: derived from the token, so
// rotating the token signs everyone out, and not the token itself.
func (a *Auth) sessionValue() string {
	m := hmac.New(sha256.New, []byte(a.token))
	m.Write([]byte("roadie-session-v1"))
	return hex.EncodeToString(m.Sum(nil))
}

func equalSecret(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Wrap checks every request but the login pages.
func (a *Auth) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Mode == AuthUnset || a.Mode == AuthNone || r.URL.Path == "/login" || r.URL.Path == "/logout" {
			next.ServeHTTP(w, r)
			return
		}
		via, err := a.authorize(r)
		if err != nil {
			a.refuse(w, r, err)
			return
		}
		// A browser's cookie goes along with cross-site WebSocket requests
		// too, so those must come from this origin.
		if via == "cookie" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || !strings.EqualFold(u.Host, r.Host) {
					http.Error(w, "cross-origin WebSocket refused", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

var errUnauthorized = errors.New("unauthorized")

// authorize says how a request is allowed in: cookie, bearer, tailscale or
// header, or why not.
func (a *Auth) authorize(r *http.Request) (string, error) {
	switch a.Mode {
	case AuthToken:
		if h := r.Header.Get("Authorization"); h != "" {
			if t, ok := strings.CutPrefix(h, "Bearer "); ok && equalSecret(strings.TrimSpace(t), a.token) {
				return "bearer", nil
			}
			return "", errUnauthorized
		}
		if c, err := r.Cookie(sessionCookie); err == nil && equalSecret(c.Value, a.sessionValue()) {
			return "cookie", nil
		}
		return "", errUnauthorized
	case AuthTailscale:
		if a.Loopback {
			if login := r.Header.Get("Tailscale-User-Login"); login != "" {
				if a.allowed(login, nil) {
					return "header", nil
				}
				return "", fmt.Errorf("%s is not allowed", login)
			}
		}
		login, tags, err := a.cachedWhoIs(r.RemoteAddr)
		if err != nil {
			return "", fmt.Errorf("not a tailnet connection: %w", err)
		}
		if a.allowed(login, tags) {
			return "tailscale", nil
		}
		return "", fmt.Errorf("%s is not allowed", firstNonEmpty(login, strings.Join(tags, " ")))
	}
	return "open", nil
}

// allowed applies the --allow list: logins and tags, or anyone when empty.
func (a *Auth) allowed(login string, tags []string) bool {
	if len(a.Allow) == 0 {
		return login != "" || len(tags) > 0
	}
	for _, want := range a.Allow {
		if login != "" && strings.EqualFold(want, login) {
			return true
		}
		for _, t := range tags {
			if strings.EqualFold(want, t) {
				return true
			}
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// refuse sends a browser to the login page and anything else a 401 or 403.
func (a *Auth) refuse(w http.ResponseWriter, r *http.Request, err error) {
	if a.Mode == AuthTailscale {
		http.Error(w, "Forbidden: "+err.Error(), http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") && !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="roadie"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized: send Authorization: Bearer <token>, from `roadie token`"})
}

// cachedWhoIs asks tailscaled who an address belongs to, remembering the
// answer for a while.
func (a *Auth) cachedWhoIs(addr string) (string, []string, error) {
	ip, _, err := net.SplitHostPort(addr)
	if err != nil {
		ip = addr
	}
	a.mu.Lock()
	e, ok := a.cache[ip]
	a.mu.Unlock()
	if ok && time.Since(e.at) < 30*time.Second {
		return e.login, e.tags, e.err
	}
	login, tags, err := a.whois(addr)
	a.mu.Lock()
	a.cache[ip] = whoisEntry{login: login, tags: tags, err: err, at: time.Now()}
	a.mu.Unlock()
	return login, tags, err
}

// tailscaledSocket is where tailscaled takes local API requests.
var tailscaledSocket = "/var/run/tailscale/tailscaled.sock"

// tailscaleWhoIs asks the local tailscaled which login or tags are behind
// an address, over its local API on the unix socket. It needs no library:
// one GET, one small JSON answer.
func tailscaleWhoIs(addr string) (string, []string, error) {
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", tailscaledSocket)
		}},
	}
	req, err := http.NewRequest(http.MethodGet, "http://local-tailscaled.sock/localapi/v0/whois?addr="+url.QueryEscape(addr), nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Sec-Tailscale", "localapi")
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("tailscaled: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("tailscaled has no peer for %s", addr)
	}
	var who struct {
		Node        struct{ Tags []string }
		UserProfile struct{ LoginName string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&who); err != nil {
		return "", nil, fmt.Errorf("tailscaled: %w", err)
	}
	return who.UserProfile.LoginName, who.Node.Tags, nil
}

// noteFailure records a bad login and says whether this address has had
// too many lately.
func (a *Auth) noteFailure(addr string) (tooMany bool) {
	ip, _, err := net.SplitHostPort(addr)
	if err != nil {
		ip = addr
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	recent := a.failures[ip][:0]
	for _, t := range a.failures[ip] {
		if now.Sub(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	a.failures[ip] = recent
	return len(recent) > 20
}

var loginPage = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  body { font: 16px/1.5 -apple-system, system-ui, sans-serif; margin: 0; padding: 24px; background: #fff; color: #111; }
  main { max-width: 22em; margin: 12vh auto; }
  h1 { font-size: 1.2em; font-weight: 600; }
  label { display: block; margin-top: 1em; }
  input { width: 100%; box-sizing: border-box; font: inherit; padding: 8px 10px; border: 1px solid #bbb; border-radius: 6px; background: #fff; color: inherit; }
  button { margin-top: 12px; font: inherit; padding: 8px 14px; border: 0; border-radius: 6px; background: #2563eb; color: #fff; cursor: pointer; }
  .err { color: #c62828; }
  .hint { color: #777; font-size: .9em; }
  code { font-size: .95em; }
  @media (prefers-color-scheme: dark) { body { background: #111; color: #eee; } input { background: #222; border-color: #444; } .hint { color: #999; } }
</style></head>
<body><main>
<h1>{{.Title}}</h1>
<form method="post" action="/login">
<input type="hidden" name="next" value="{{.Next}}">
<label>Token<input name="token" type="password" autofocus autocomplete="current-password"></label>
<button>Sign in</button>
</form>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<p class="hint">The token is in the host's token file: run <code>roadie token</code> there.</p>
</main></body></html>
`))

// safeNext keeps a post-login destination on this site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return "/"
	}
	return next
}

// handleLogin is the one-field login page for --auth token.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	a := s.Auth
	if a == nil || a.Mode != AuthToken {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	page := struct {
		Title, Next, Error string
	}{Title: s.pageTitle() + " login", Next: safeNext(r.FormValue("next"))}
	if r.Method == http.MethodPost {
		if a.noteFailure(r.RemoteAddr) {
			http.Error(w, "too many attempts; try again later", http.StatusTooManyRequests)
			return
		}
		if equalSecret(strings.TrimSpace(r.FormValue("token")), a.token) {
			a.forgetFailures(r.RemoteAddr)
			http.SetCookie(w, &http.Cookie{
				Name: sessionCookie, Value: a.sessionValue(), Path: "/",
				MaxAge: sessionDays * 24 * 3600, HttpOnly: true, Secure: a.Secure || r.TLS != nil,
				SameSite: http.SameSiteLaxMode,
			})
			http.Redirect(w, r, page.Next, http.StatusSeeOther)
			return
		}
		time.Sleep(time.Second)
		page.Error = "Wrong token."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	loginPage.Execute(w, page)
}

func (a *Auth) forgetFailures(addr string) {
	ip, _, err := net.SplitHostPort(addr)
	if err != nil {
		ip = addr
	}
	a.mu.Lock()
	delete(a.failures, ip)
	a.mu.Unlock()
}

// handleLogout clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// isLoopbackAddr says whether a bind address only reaches this machine.
func isLoopbackAddr(bind string) bool {
	if bind == "localhost" {
		return true
	}
	ip := net.ParseIP(bind)
	return ip != nil && ip.IsLoopback()
}
