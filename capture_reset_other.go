//go:build !linux

package main

import "fmt"

func resetCaptureUSB(videoNode string) error {
	return fmt.Errorf("USB reset is only supported on Linux")
}
