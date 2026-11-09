//go:build !linux

package devices

import "fmt"

func ResetCaptureUSB(videoNode string) error {
	return fmt.Errorf("USB reset is only supported on Linux")
}

func ResetRelayUSB(ttyPath string) error {
	return fmt.Errorf("USB reset is only supported on Linux")
}
