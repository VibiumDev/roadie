//go:build !linux || !(arm64 || amd64)

package main

func openNativeCapture(dev deviceInfo, width, height, fps int) (frameReader, error) {
	return nil, errNoNativeCapture
}
