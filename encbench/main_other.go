//go:build !linux || !(arm64 || amd64)

// encbench measures the Pi 4's capture-to-H.264 pipeline. It needs Linux.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "encbench needs 64-bit Linux with V4L2 devices")
	os.Exit(1)
}
