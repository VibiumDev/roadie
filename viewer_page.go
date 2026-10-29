package main

import "embed"

// The /view page's own parts, as files: the head (icon link and styles),
// the body (toolbar, picture, panels, keyboard) and the script that runs
// it. handleView writes them around the lines it computes per instance
// (title, platform, input mode, transport), so the page is byte for byte
// what it was when it lived inline.
//
//go:embed view/head.html view/body.html view/script.html
var viewFiles embed.FS

func mustView(name string) []byte {
	data, err := viewFiles.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return data
}

var (
	viewHead   = mustView("view/head.html")
	viewBody   = mustView("view/body.html")
	viewScript = mustView("view/script.html")
)
