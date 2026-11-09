package web

import (
	_ "embed"
	"net/http"
)

// Roadie's icon, for browser tabs and home screens: the guitar, as a
// station's is the satellite antenna.
//
//go:embed icon.png
var iconPNG []byte

func serveIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Write(iconPNG)
}
