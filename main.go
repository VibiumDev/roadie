package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	device := flag.String("device", "", "device name substring (auto-detect if empty)")
	port := flag.Int("port", 0, "HTTP server port (default: auto, starting at 8080)")
	width := flag.Int("width", 1920, "capture width")
	height := flag.Int("height", 1080, "capture height")
	fps := flag.Int("fps", 30, "capture framerate")
	quality := flag.Int("quality", 80, "JPEG compression quality (1-100)")
	name := flag.String("name", "roadie", "Bonjour service name")
	flag.Parse()

	// Start the AVFoundation device observer so the driver manager stays
	// in sync with hardware (auto-registers on plug, auto-unregisters on unplug).
	if err := InitObserver(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Device observer failed: %v\n", err)
	}

	// Set up capture manager (handles detect → capture → reconnect loop).
	buf := &FrameBuffer{}
	ab := NewAudioBroadcaster()
	cm := NewCaptureManager(*device, *width, *height, *fps, *quality, buf, ab)

	// Try an initial detect for the startup banner, but don't exit on failure.
	dev, err := DetectDevice(*device)
	if err != nil {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "⚠️  No capture device found — will keep trying in the background")
		fmt.Fprintln(os.Stderr, "   Plug in an HDMI-to-USB capture dongle to start streaming.")
		fmt.Fprintln(os.Stderr, "")
	} else {
		fmt.Printf("📺 Found %q capture device\n", dev.Name)
		fmt.Printf("🎬 Capturing at %dx%d @ %dfps\n", *width, *height, *fps)
	}

	go cm.Run()

	// Find available port.
	listenPort := *port
	if listenPort == 0 {
		listenPort = findAvailablePort(8080)
	}

	// Register mDNS (for service discovery via dns-sd -B _roadie._tcp).
	resolution := fmt.Sprintf("%dx%d", *width, *height)
	mdnsShutdown, err := RegisterMDNS(*name, listenPort, resolution)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  mDNS registration failed: %v\n", err)
	}

	// Print startup banner.
	fmt.Println()
	fmt.Printf("🌐 http://localhost:%d\n", listenPort)
	if ip := getLANIP(); ip != "" {
		fmt.Printf("🌐 http://%s:%d\n", ip, listenPort)
	}
	fmt.Printf("🌐 http://%s.local:%d\n", *name, listenPort)

	// Start HTTP server.
	deviceName := ""
	if err == nil {
		deviceName = dev.Name
	}
	srv := &Server{
		Source:         buf,
		Device:         deviceName,
		Width:          *width,
		Height:         *height,
		FPS:            *fps,
		Quality:        *quality,
		AudioBroadcast: ab,
	}
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", listenPort),
		Handler: NewMux(srv),
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-done
	fmt.Println("\nShutting down...")

	cm.Shutdown()

	if mdnsShutdown != nil {
		mdnsShutdown()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)
}

// findAvailablePort tries ports starting from start and returns the first one available.
func findAvailablePort(start int) int {
	for p := start; p < start+100; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err == nil {
			ln.Close()
			return p
		}
	}
	return start
}

// getLANIP returns the first non-loopback IPv4 address, or empty string.
func getLANIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.String()
		}
	}
	return ""
}
