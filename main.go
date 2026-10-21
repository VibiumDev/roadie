package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// deviceHint shortens a capture device name to the part worth typing.
// Driver names carry their own bus path -- "UGREEN 15389: UGREEN
// 15389;usb-0000:01:00.0-1" -- and only the leading name is useful as a
// --video substring.
func deviceHint(name string) string {
	if i := strings.IndexAny(name, ":;"); i > 0 {
		name = name[:i]
	}
	return strings.TrimSpace(name)
}

// suggestCommand prints a command line for what is actually plugged in.
// Listing the devices only answers half the question; the other half is
// what to type, and these names are too long to retype by hand.
func suggestCommand(w io.Writer, cands []string, relays []RelayInfo) {
	if len(cands) == 0 {
		fmt.Fprintln(w, "No capture device found. Plug in an HDMI-to-USB dongle.")
		return
	}
	many := len(cands) > 1 || len(relays) > 1
	if many {
		fmt.Fprintln(w, "More than one device is connected, so each target needs its own")
		fmt.Fprintln(w, "flags. Run one roadie per target:")
	} else {
		fmt.Fprintln(w, "Suggested command:")
	}
	for i, c := range cands {
		line := "  ./roadie"
		if many {
			line += fmt.Sprintf(" --name target%d --port %d", i+1, 8080+i)
		}
		line += fmt.Sprintf(" --video %q", deviceHint(c))
		if many && i < len(relays) {
			line += " --relay " + relays[i].Serial
		}
		fmt.Fprintln(w, line+" --platform <android|ios|mac|windows|linux>")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Sound follows the video device unless --audio says otherwise.")
}

func main() {
	device := flag.String("video", "", "video capture device name substring (auto-detect if empty)")
	audioDevice := flag.String("audio", "", "audio capture device name substring (defaults to --video)")
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
	glide := flag.Bool("glide", false, "glide the pointer to each new spot along an arc, as a hand would, instead of jumping (a switch in /settings too)")
	hidBackendName := flag.String("hid-backend", BackendRP2040, "input hardware: rp2040 (relay and HID boards) or linux-gadget (this machine's USB device port, set up by gadget/roadie-gadget)")
	flag.Parse()

	hidBackend, err := ParseHIDBackend(*hidBackendName)
	if err != nil {
		log.Fatal(err)
	}

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
		if desc := describeGadget(defaultGadgetPaths()); desc != "" {
			fmt.Println("USB gadget (for --hid-backend linux-gadget):")
			fmt.Println(desc)
			fmt.Println()
		}
		suggestCommand(os.Stdout, CaptureCandidates(), relays)
		return
	}

	if *source != "" && *device != "" {
		log.Fatal("--source and --video are mutually exclusive")
	}

	if *inputMode != "" && *inputMode != "mouse" && *inputMode != "touch" {
		log.Fatalf("--input must be mouse or touch, got %q", *inputMode)
	}

	if hidBackend == BackendLinuxGadget && *relay != "" {
		fmt.Fprintln(os.Stderr, "⚠️  --relay is ignored with --hid-backend linux-gadget")
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
	cropFile := defaultCropFile(*name)
	if use, crops, err := loadSavedCrops(cropFile); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Saved crop not used: %v\n", err)
	} else {
		buf.SetCropUse(use)
		for o, c := range crops {
			buf.SetSavedCrop(o, c)
			fmt.Printf("✂️  Saved %s crop: %dx%d at (%d,%d)\n", o, c.Rect.Dx(), c.Rect.Dy(), c.Rect.Min.X, c.Rect.Min.Y)
			if use == CropSaved && len(crops) == 1 {
				// The usual case: one crop, for the way the device is
				// normally held. Use it from the first frame.
				buf.presumeOrientation(o)
			}
		}
		if use != CropAuto {
			fmt.Printf("✂️  Crop: %s\n", use)
		}
	}
	var ab *AudioBroadcaster
	var gadgetAudio *GadgetAudio
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
	}

	// With the gadget's speaker the phone can send its sound over USB or
	// HDMI -- Android picks USB, iOS either, and it can switch -- so /audio
	// follows whichever is live (audioMux). Without a speaker, the capture
	// dongle's audio is used as it always was.
	captureAudio := ab
	var mux *audioMux
	if hidBackend == BackendLinuxGadget {
		cfg, err := readGadgetAudioConfig(defaultGadgetPaths())
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  USB audio: %v (no USB microphone or speaker until roadie restarts)\n", err)
		} else {
			var usbAudio *AudioBroadcaster
			if cfg.hasSpeaker() {
				if ab == nil {
					ab = NewAudioBroadcaster()
				}
				usbAudio = NewAudioBroadcaster()
				var hdmiAudio *AudioBroadcaster
				if captureAudio != nil {
					hdmiAudio = NewAudioBroadcaster()
					captureAudio = hdmiAudio
				}
				mux = newAudioMux(ab, usbAudio, hdmiAudio)
			}
			gadgetAudio = NewGadgetAudio(cfg, usbAudio)
			fmt.Printf("🔊 USB audio: %s (microphone %d ch @ %d Hz, speaker %d ch @ %d Hz)\n",
				cfg.cardID, cfg.micChannels, cfg.micRate, cfg.spkChannels, cfg.spkRate)
		}
	}

	if *source == "" {
		// Start the AVFoundation device observer so the driver manager stays
		// in sync with hardware (auto-registers on plug, auto-unregisters on unplug).
		if err := InitObserver(); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  Device observer failed: %v\n", err)
		}

		cm = NewCaptureManager(*device, *audioDevice, buf, captureAudio)
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
				fmt.Fprintf(os.Stderr, "\n⚠️  %d capture devices connected — use --video to pick one:\n", len(cands))
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

	// Start the HID backend: the relay boards, or this machine's USB gadget.
	var hid HIDBackend
	if hidBackend == BackendLinuxGadget {
		g := NewGadgetHID()
		if gadgetAudio != nil && gadgetAudio.Mic != nil {
			g.SetMic(gadgetAudio.Mic)
		}
		hid = g
		fmt.Printf("🔌 HID: USB gadget %q (linux-gadget backend)\n", gadgetName)
	} else {
		hid = NewHIDController(*relay)
		if relays := ListRelays(); len(relays) > 1 && *relay == "" {
			fmt.Fprintf(os.Stderr, "\n⚠️  %d relay boards connected — use --relay to pick one:\n", len(relays))
			for _, r := range relays {
				fmt.Fprintf(os.Stderr, "   %s\n", r)
			}
			fmt.Fprintln(os.Stderr, "")
		}
	}
	settled := newSettledHID(hid)
	settled.touchAsPointer = platform.TouchAsPointer()
	settled.SetGlide(*glide)
	hid = settled
	go hid.Run()
	if gadgetAudio != nil {
		gadgetAudio.Start()
	}
	muxCtx, stopMux := context.WithCancel(context.Background())
	defer stopMux()
	if mux != nil {
		go mux.run(muxCtx)
	}

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
		Name:           *name,
		CropFile:       cropFile,
	}
	if mux != nil {
		srv.AudioSource = mux.Source
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
	if gadgetAudio != nil {
		gadgetAudio.Shutdown()
	}

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
