package main

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// wallTarget is one Roadie instance rendered as a panel on the /wall page.
type wallTarget struct {
	URL   string // absolute /view URL, chrome disabled
	Label string
}

// maxWallTargets bounds the panel count so a stray query string can't ask the
// browser to open hundreds of MJPEG streams.
const maxWallTargets = 12

// parseWallTargets turns the targets and labels query values into panels.
//
// Each target is a host[:port] or an absolute http(s) URL. Only the scheme and
// host are taken from the input — the path and query of the iframe src are
// always rebuilt here, so no caller-supplied path, query, or javascript: URL
// can reach the rendered page.
//
// Targets must be reachable from the *browser*, not from the server: the wall
// typically runs on the same host as the Roadie instances, but is viewed from
// another machine, so "localhost" would resolve to the viewer's own machine.
func parseWallTargets(targets, labels string) ([]wallTarget, error) {
	var labelList []string
	if labels != "" {
		labelList = strings.Split(labels, ",")
	}

	var out []wallTarget
	for i, raw := range strings.Split(targets, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if len(out) >= maxWallTargets {
			return nil, fmt.Errorf("too many targets (max %d)", maxWallTargets)
		}

		// Bare host:port is the common form; give it a scheme so url.Parse
		// treats it as a host rather than as a path or a scheme:opaque URL.
		withScheme := raw
		if !strings.Contains(raw, "://") {
			withScheme = "http://" + raw
		}
		u, err := url.Parse(withScheme)
		if err != nil {
			return nil, fmt.Errorf("invalid target %q", raw)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("target %q must be http or https", raw)
		}
		if u.Hostname() == "" {
			return nil, fmt.Errorf("target %q has no host", raw)
		}

		label := ""
		if i < len(labelList) {
			label = strings.TrimSpace(labelList[i])
		}
		if label == "" {
			label = defaultWallLabel(u.Hostname())
		}

		// Rebuild from scratch: scheme + host only, plus our own path/query.
		viewURL := url.URL{
			Scheme:   u.Scheme,
			Host:     u.Host,
			Path:     "/view",
			RawQuery: "chrome=0",
		}
		out = append(out, wallTarget{URL: viewURL.String(), Label: label})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no targets given")
	}
	return out, nil
}

// defaultWallLabel derives a panel caption from a hostname, trimming the
// mDNS suffix so "roadie-a.local" reads as "roadie-a".
func defaultWallLabel(hostname string) string {
	return strings.TrimSuffix(hostname, ".local")
}

// handleWall renders a grid of Roadie viewers, one iframe per target.
//
// The wall carries no video itself: each panel loads its stream straight from
// its own Roadie, so frames travel point-to-point from each capture host to
// the browser instead of being relayed through here. Each iframe is also
// same-origin with the instance it embeds, so the stream, HID WebSocket, and
// audio all work without any cross-origin handling.
func (s *Server) handleWall(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	targets, err := parseWallTargets(q.Get("targets"), q.Get("labels"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeWallUsage(w, err.Error())
		return
	}

	// Panels sit in a single row by default, which suits portrait phone
	// screens on a landscape display. ?cols= overrides for other shapes.
	cols := len(targets)
	if c := q.Get("cols"); c != "" {
		if n, err := strconv.Atoi(c); err == nil && n > 0 {
			cols = n
		}
	}

	// chrome=0 drops the captions and padding for a bare video wall.
	showChrome := q.Get("chrome") != "0"
	gap := 0
	if showChrome {
		gap = 10
	}

	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Roadie Wall</title><link rel="icon" href="data:,">
<style>
  :root { --page-bg:#1a1a1a; --label:#8a8a8a; }
  :root[data-theme="light"] { --page-bg:#e8e8ed; --label:#555; }
  @media (prefers-color-scheme:light) { :root:not([data-theme="dark"]) { --page-bg:#e8e8ed; --label:#555; } }
  * { box-sizing:border-box; }
  body { margin:0; height:100vh; overflow:hidden; background:var(--page-bg);
         display:grid; grid-template-columns:repeat(%d, minmax(0, 1fr));
         grid-auto-rows:minmax(0, 1fr); gap:%dpx; padding:%dpx; }
  .panel { display:flex; flex-direction:column; min-width:0; min-height:0; }
  .panel h2 { margin:0 0 6px; font:600 11px/1.4 -apple-system,BlinkMacSystemFont,sans-serif;
              color:var(--label); text-align:center; text-transform:uppercase; letter-spacing:0.06em; }
  .panel iframe { flex:1; width:100%%; min-height:0; border:0; background:#000; }
  .panel iframe.framed { border-radius:8px; }
</style>
</head>
<body>
`, cols, gap, gap)

	for _, t := range targets {
		esc := html.EscapeString(t.URL)
		fmt.Fprint(w, `  <div class="panel">`+"\n")
		if showChrome {
			fmt.Fprintf(w, "    <h2>%s</h2>\n", html.EscapeString(t.Label))
		}
		class := "framed"
		if !showChrome {
			class = ""
		}
		fmt.Fprintf(w, `    <iframe class="%s" src="%s" title="%s" allow="autoplay" scrolling="no"></iframe>`+"\n",
			class, esc, html.EscapeString(t.Label))
		fmt.Fprint(w, "  </div>\n")
	}

	fmt.Fprint(w, `</body>
</html>
`)
}

// writeWallUsage renders a short help page when the targets list is missing or
// malformed — the wall is usually opened by hand-typing a URL, so an example is
// more useful than a bare 400.
func writeWallUsage(w http.ResponseWriter, reason string) {
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Roadie Wall</title><link rel="icon" href="data:,">
<style>
  body { margin:0; min-height:100vh; background:#1a1a1a; color:#ddd;
         font:14px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace;
         display:flex; align-items:center; justify-content:center; padding:24px; }
  .box { max-width:640px; }
  h1 { font-size:16px; margin:0 0 12px; }
  code { color:#8ec07c; word-break:break-all; }
  p { margin:12px 0; }
  .err { color:#e88; }
</style>
</head>
<body><div class="box">
<h1>&#128506; Roadie Wall</h1>
<p class="err">%s</p>
<p>Show several Roadie instances side by side:</p>
<p><code>/wall?targets=roadie-a.local:8080,roadie-b.local:8081</code></p>
<p>Optional: <code>&amp;labels=Pixel,iPhone</code> to caption the panels,
<code>&amp;cols=2</code> to set the grid width, and <code>&amp;chrome=0</code>
for a bare wall with no captions or padding.</p>
<p>Targets must be reachable from this browser, so use LAN hostnames or IPs
rather than <code>localhost</code> when viewing from another machine.</p>
</div></body>
</html>
`, html.EscapeString(reason))
}
