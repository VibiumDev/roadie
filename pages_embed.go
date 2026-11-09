package main

import _ "embed"

// The e2e test pages live under board/ beside the firmware they test, so
// the main package embeds them and hands them to the server.

//go:embed board/e2e/gesture-page.html
var gesturePageHTML []byte

//go:embed board/e2e/mic-page.html
var soundcheckPage []byte
