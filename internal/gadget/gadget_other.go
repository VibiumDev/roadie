//go:build !linux

package gadget

import "errors"

func openHIDGadget(path string) (reportDevice, error) {
	return nil, errors.New("the linux-gadget backend needs Linux with a USB device controller")
}
