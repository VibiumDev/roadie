//go:build !linux

package main

import "fmt"

func resetCaptureUSB() error {
	return fmt.Errorf("USB reset is only supported on Linux")
}
