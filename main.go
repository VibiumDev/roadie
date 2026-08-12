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
	device := flag.String("device", "", "video capture device name substring (auto-detect if empty)")
	relay := flag.String("relay", "", "relay board serial number substring (auto-detect if empty)")
	source := flag.String("source", "", "HTTP MJPEG source URL (e.g. http://host:8080/raw-stream)")
	listDevices := flag.Bool("list-devices", false, "list all video and audio devices, then exit")
	port := flag.Int("port", 0, "HTTP server port (default: auto, starting at 8080)")
	width := flag.Int("width", 1920, "capture width")
	height := flag.Int("height", 1080, "capture height")
	fps := flag.Int("fps", 30, "capture framerate")
	quality := flag.Int("quality", 80, "JPEG compression quality (1-100)")
	name := flag.String("name", "roadie", "Bonjour service name")
	inputMode := flag.String("input", "", "default pointer mode for viewers: mouse or touch (empty: viewer decides)")
	platformName := flag.String("platform", "", "target OS: android, ios, mac, windows, linux (tunes pointer mapping and input mode)")
	flag.Parse()

	if *listDevices {
		if err := InitObserver(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: device observer failed: %v\n", err)
		}

		fmt.Println("Video devices:")
		for _, d := range ListDevices() {
			fmt.Printf("  - %s\n", d)
		}
		fmt.Println()
		fmt.Println("Audio devices:")
		for _, d := range ListAudioDevices() {
			fmt.Printf("  - %s\n", d)
		}
		fmt.Println()
		fmt.Println("Relay boards:")
		relays := ListRelays()
		if len(relays) == 0 {
			fmt.Println("  (none found)")
		}
		for _, r := range relays {
			fmt.Printf("  - %s\n", r)
		}
		fmt.Println()
		fmt.Println("Tip: use --device <substring> to select a capture device,")
		fmt.Println("     and --relay <serial substring> to select a relay board.")
		fmt.Println("     Run one roadie per target device, each with its own")
		fmt.Println("     --device, --relay, --port, and --name.")
		return
	}

	if *source != "" && *device != "" {
		log.Fatal("--source and --device are mutually exclusive")
	}

	if *inputMode != "" && *inputMode != "mouse" && *inputMode != "touch" {
		log.Fatalf("--input must be mouse or touch, got %q", *inputMode)
	}

	platform, err := ParsePlatform(*platformName)
	if err != nil {
		log.Fatal(err)
	}

	buf := &FrameBuffer{}
	buf.SetQuality(*quality)
	buf.SetFPS(*fps)
	buf.SetWidth(*width)
	buf.SetHeight(*height)
	var ab *AudioBroadcaster
	var sourceType string
	var deviceName string
	var shutdownFunc func()
	var cm *CaptureManager

	if *source != "" {
		// HTTP MJPEG source mode.
		sourceType = "http"
		deviceName = *source
		mgr := NewHTTPSourceManager(*source, buf)
		shutdownFunc = mgr.Shutdown
		fmt.Printf("📡 HTTP source: %s\n", *source)
		go mgr.Run()
	} else {
		// Hardware capture mode.
		sourceType = "hardware"
		ab = NewAudioBroadcaster()

		// Start the AVFoundation device observer so the driver manager stays
		// in sync with hardware (auto-registers on plug, auto-unregisters on unplug).
		if err := InitObserver(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  Device observer failed: %v\n", err)
		}

		cm = NewCaptureManager(*device, buf, ab)
		shutdownFunc = cm.Shutdown

		// Try an initial detect for the startup banner, but don't exit on failure.
		dev, err := DetectDevice(*device)
		if err != nil {
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "⚠️  No capture device found — will keep trying in the background")
			fmt.Fprintln(os.Stderr, "   Plug in an HDMI-to-USB capture dongle to start streaming.")
			fmt.Fprintln(os.Stderr, "")
		} else {
			deviceName = dev.Name
			fmt.Printf("📺 Found %q capture device\n", dev.Name)
			fmt.Printf("🎬 Capturing at %dx%d @ %dfps\n", *width, *height, *fps)
			if cands := CaptureCandidates(); len(cands) > 1 && *device == "" {
				fmt.Fprintf(os.Stderr, "\n⚠️  %d capture devices connected — use --device to pick one:\n", len(cands))
				for _, c := range cands {
					fmt.Fprintf(os.Stderr, "   %s\n", c)
				}
				fmt.Fprintln(os.Stderr, "")
			}
		}

		go cm.Run()
	}

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

	// Start HID controller (relay board serial connection).
	hid := NewHIDController(*relay)
	if relays := ListRelays(); len(relays) > 1 && *relay == "" {
		fmt.Fprintf(os.Stderr, "\n⚠️  %d relay boards connected — use --relay to pick one:\n", len(relays))
		for _, r := range relays {
			fmt.Fprintf(os.Stderr, "   %s\n", r)
		}
		fmt.Fprintln(os.Stderr, "")
	}
	go hid.Run()

	// Print startup banner.
	fmt.Println()
	fmt.Printf("🌐 http://localhost:%d\n", listenPort)
	if ip := getLANIP(); ip != "" {
		fmt.Printf("🌐 http://%s:%d\n", ip, listenPort)
	}
	fmt.Printf("🌐 http://%s.local:%d\n", MDNSHostname(*name), listenPort)

	// Start HTTP server.
	srv := &Server{
		Source:         buf,
		Buf:            buf,
		Device:         deviceName,
		AudioBroadcast: ab,
		SourceType:     sourceType,
		HID:            hid,
		Capture:        cm,
		InputMode:      *inputMode,
		Platform:       platform,
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

	shutdownFunc()
	hid.Shutdown()

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
