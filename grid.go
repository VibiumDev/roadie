package main

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// gridTarget is one Roadie instance rendered as a panel on the /grid page.
type gridTarget struct {
	URL      string // absolute /view URL, rendered without page furniture
	Snapshot string // absolute /snapshot URL, used to measure the target's shape
	Label    string
}

// maxGridTargets bounds the panel count so a stray query string can't ask the
// browser to open hundreds of MJPEG streams.
const maxGridTargets = 12

// parseGridTargets turns the targets, labels and input query values into panels.
//
// Each target is a host[:port] or an absolute http(s) URL. Only the scheme and
// host are taken from the input — the path and query of the iframe src are
// always rebuilt here, so no caller-supplied path, query, or javascript: URL
// can reach the rendered page.
//
// Targets must be reachable from the *browser*, not from the server: the grid
// typically runs on the same host as the Roadie instances, but is viewed from
// another machine, so "localhost" would resolve to the viewer's own machine.
func parseGridTargets(targets, labels, inputs string) ([]gridTarget, error) {
	var labelList []string
	if labels != "" {
		labelList = strings.Split(labels, ",")
	}

	// input is either one mode for every panel or a positional list, matching
	// how labels works. Panels embed a minimal view whose own mode toggle is
	// hidden, so this is the only way to choose one from the grid.
	var inputList []string
	for _, in := range strings.Split(inputs, ",") {
		in = strings.TrimSpace(in)
		if in != "" && in != "mouse" && in != "touch" {
			return nil, fmt.Errorf("input %q must be mouse or touch", in)
		}
		inputList = append(inputList, in)
	}

	var out []gridTarget
	for i, raw := range strings.Split(targets, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if len(out) >= maxGridTargets {
			return nil, fmt.Errorf("too many targets (max %d)", maxGridTargets)
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
			label = defaultGridLabel(u.Hostname())
		}

		input := ""
		if len(inputList) == 1 {
			input = inputList[0] // one mode covers every panel
		} else if i < len(inputList) {
			input = inputList[i]
		}

		// Rebuild from scratch: scheme + host only, plus our own path/query.
		q := url.Values{"minimal": {"1"}}
		if input != "" {
			q.Set("input", input)
		}
		viewURL := url.URL{
			Scheme:   u.Scheme,
			Host:     u.Host,
			Path:     "/view",
			RawQuery: q.Encode(),
		}
		snapURL := url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/snapshot"}
		out = append(out, gridTarget{
			URL:      viewURL.String(),
			Snapshot: snapURL.String(),
			Label:    label,
		})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no targets given")
	}
	return out, nil
}

// defaultGridLabel derives a panel caption from a hostname, trimming the
// mDNS suffix so "roadie-a.local" reads as "roadie-a".
func defaultGridLabel(hostname string) string {
	return strings.TrimSuffix(hostname, ".local")
}

// handleGrid renders a grid of Roadie viewers, one iframe per target.
//
// The grid carries no video itself: each panel loads its stream straight from
// its own Roadie, so frames travel point-to-point from each capture host to
// the browser instead of being relayed through here. Each iframe is also
// same-origin with the instance it embeds, so the stream, HID WebSocket, and
// audio all work without any cross-origin handling.
func (s *Server) handleGrid(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	targets, err := parseGridTargets(q.Get("targets"), q.Get("labels"), q.Get("input"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeGridUsage(w, err.Error())
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

	// minimal=1 drops the captions and padding for a bare video grid.
	minimal := q.Get("minimal") == "1"
	gap := 10
	if minimal {
		gap = 0
	}

	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Roadie Grid</title><link rel="icon" href="data:,">
<style>
  :root { --page-bg:#1a1a1a; --label:#6f6f6f; --label-on:#c8c8c8; --focus:rgba(255,255,255,0.30); }
  :root[data-theme="light"] { --page-bg:#e8e8ed; --label:#8a8a8a; --label-on:#1a1a1a; --focus:rgba(0,0,0,0.28); }
  @media (prefers-color-scheme:light) { :root:not([data-theme="dark"]) { --page-bg:#e8e8ed; --label:#8a8a8a; --label-on:#1a1a1a; --focus:rgba(0,0,0,0.28); } }
  * { box-sizing:border-box; }
  body { margin:0; height:100vh; overflow:hidden; background:var(--page-bg);
         display:grid; grid-template-columns:repeat(%d, minmax(0, 1fr));
         grid-auto-rows:minmax(0, 1fr); gap:%dpx; padding:%dpx; }
  .panel { display:flex; align-items:center; justify-content:center; min-width:0; min-height:0; }
  /* The stack is only as wide as the screen inside it, so the caption sits
     over the phone rather than over the whole share of the grid it occupies. */
  .stack { display:flex; flex-direction:column; align-items:center; justify-content:flex-end;
           height:100%%; max-width:100%%; min-width:0; min-height:0; }
  .stack h2 { flex:none; margin:0 0 6px; font:600 11px/1.4 -apple-system,BlinkMacSystemFont,sans-serif;
              color:var(--label); text-align:center; text-transform:uppercase; letter-spacing:0.06em;
              transition:color 120ms ease; }
  /* Width starts at 100%% and becomes aspect-driven once the target has been
     measured, so an unmeasured panel still fills its cell rather than
     collapsing to an iframe's default width. */
  .stack iframe { flex:1 1 auto; width:100%%; min-height:0; border:0; background:#000;
                  box-shadow:0 0 0 1px transparent; transition:box-shadow 120ms ease; }
  .stack iframe.framed { border-radius:8px; }
  .panel.focused .stack iframe { box-shadow:0 0 0 1px var(--focus); }
  .panel.focused .stack h2 { color:var(--label-on); }
</style>
</head>
<body>
`, cols, gap, gap)

	for _, t := range targets {
		class := "framed"
		if minimal {
			class = ""
		}
		fmt.Fprintf(w, "  <div class=\"panel\" data-snapshot=\"%s\">\n    <div class=\"stack\">\n",
			html.EscapeString(t.Snapshot))
		if !minimal {
			fmt.Fprintf(w, "      <h2>%s</h2>\n", html.EscapeString(t.Label))
		}
		fmt.Fprintf(w, `      <iframe class="%s" src="%s" title="%s" allow="autoplay" scrolling="no"></iframe>`+"\n",
			class, html.EscapeString(t.URL), html.EscapeString(t.Label))
		fmt.Fprint(w, "    </div>\n  </div>\n")
	}

	// Keyboard input goes to whichever iframe holds focus, so move focus with
	// the pointer — the same thing already decides which target receives mouse
	// and touch. The embedded viewer claims focus itself when the pointer
	// reaches the feed; doing it here as well covers the padding and caption
	// around it, and gives automation a single predictable rule: point at a
	// panel, then type.
	//
	// Which panel is live has to be visible, otherwise keystrokes land
	// somewhere the operator did not choose and nothing says so. A
	// cross-origin iframe will not report its own focus, but the parent can
	// see which iframe element is active, which is enough to mark it.
	fmt.Fprint(w, `<script>
(function () {
  var panels = [].slice.call(document.querySelectorAll('.panel'));

  // Size each panel to its target's actual shape. A cross-origin image will
  // not give up its pixels, but naturalWidth/naturalHeight are readable, so a
  // snapshot is enough to learn the aspect without needing CORS on the API.
  // Until then the panel keeps filling its cell.
  function measure(panel) {
    var src = panel.getAttribute('data-snapshot');
    if (!src) return;
    var frame = panel.querySelector('iframe');
    var probe = new Image();
    probe.onload = function () {
      if (!probe.naturalWidth || !probe.naturalHeight) return;
      frame.style.aspectRatio = probe.naturalWidth + ' / ' + probe.naturalHeight;
      frame.style.width = 'auto';
    };
    probe.src = src + (src.indexOf('?') < 0 ? '?' : '&') + 'grid=' + Date.now();
  }
  function measureAll() { panels.forEach(measure); }
  measureAll();
  // Re-measure occasionally: a target that rotates or changes what it is
  // letterboxing changes shape, and the panel should follow it.
  setInterval(measureAll, 30000);

  panels.forEach(function (panel) {
    var frame = panel.querySelector('iframe');
    panel.addEventListener('mouseenter', function () {
      try { frame.contentWindow.focus(); } catch (e) {}
      mark();
    });
  });

  function mark() {
    var active = document.activeElement;
    panels.forEach(function (panel) {
      var focused = panel.querySelector('iframe') === active;
      panel.classList.toggle('focused', focused);
    });
  }

  // activeElement changes when focus moves into an iframe without firing an
  // event the parent can hear, so sample it as well as reacting to events.
  window.addEventListener('focus', mark, true);
  window.addEventListener('blur', mark, true);
  setInterval(mark, 250);
  mark();
})();
</script>
</body>
</html>
`)
}

// writeGridUsage renders a short help page when the targets list is missing or
// malformed — the grid is usually opened by hand-typing a URL, so an example is
// more useful than a bare 400.
func writeGridUsage(w http.ResponseWriter, reason string) {
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Roadie Grid</title><link rel="icon" href="data:,">
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
<h1>&#128506; Roadie Grid</h1>
<p class="err">%s</p>
<p>Show several Roadie instances side by side:</p>
<p><code>/grid?targets=roadie-a.local:8080,roadie-b.local:8081</code></p>
<p>Optional: <code>&amp;input=touch</code> to drive the targets by touch rather
than mouse (one mode for all panels, or a positional list like
<code>touch,mouse</code>), <code>&amp;labels=Pixel,iPhone</code> to caption them,
<code>&amp;cols=2</code> to set the grid width, and <code>&amp;minimal=1</code>
for a bare grid with no captions or padding.</p>
<p>Targets must be reachable from this browser, so use LAN hostnames or IPs
rather than <code>localhost</code> when viewing from another machine.</p>
</div></body>
</html>
`, html.EscapeString(reason))
}
