// Command soundcheck serves the soundcheck page, index.html, and a plain
// white page, white.html, both built into the binary: deploying it is
// copying one file. See README.md.
//
//	soundcheck [-addr :8000]
//
// It serves the soundcheck page at / and the white page at /white, and
// nothing else. HTTPS is left to whatever sits in front of it (exe.dev's
// proxy), but the soundcheck page needs it: Safari gives the microphone only
// to a secure page.
package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"time"
)

//go:embed index.html
var page []byte

// white is a page that is white to the screen's edges, for saving a crop
// in Roadie.
//
//go:embed white.html
var white []byte

func handler(page, white []byte) http.Handler {
	pages := map[string][]byte{"/": page, "/index.html": page, "/white": white}
	etags := map[string]string{}
	for path, p := range pages {
		sum := sha256.Sum256(p)
		etags[path] = `"` + hex.EncodeToString(sum[:8]) + `"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("ETag", etags[r.URL.Path])
		h.Set("Cache-Control", "no-cache") // always check, so an update shows
		// The pages are all inline and load nothing, so they may load
		// nothing, be framed by nobody, and use the microphone themselves.
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; "+
			"style-src 'unsafe-inline'; img-src data:; frame-ancestors 'none'; "+
			"base-uri 'none'; form-action 'none'")
		h.Set("Permissions-Policy", "microphone=(self)")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, "page.html", time.Time{}, bytes.NewReader(page))
	})
}

func main() {
	addr := flag.String("addr", ":8000", "address to listen on")
	flag.Parse()
	srv := &http.Server{
		Addr:    *addr,
		Handler: handler(page, white),
		// Slow or idle clients don't get to hold a connection open.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	log.Printf("soundcheck: serving on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
