//go:build !linux || !(arm64 || amd64)

package main

import "github.com/VibiumDev/roadie/internal/devices"

func openNativeCapture(dev deviceInfo, width, height, fps int) (frameReader, error) {
	return nil, errNoNativeCapture
}
