package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/pprof"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fiatjaf.com/nostr"

	"github.com/VibiumDev/roadie/internal/audio"
	"github.com/VibiumDev/roadie/internal/hid"
	"github.com/VibiumDev/roadie/internal/platform"
	"github.com/VibiumDev/roadie/rtc"
	"github.com/VibiumDev/station/rig"
	"github.com/VibiumDev/station/sdnotify"
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
func suggestCommand(w io.Writer, cands []string, relays []hid.RelayInfo) {
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
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		os.Exit(runCommand(os.Args[1], os.Args[2:]))
	}
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
	label := flag.String("label", "", "the device's name for people on a station, such as \"Dev iPhone 15\" (default: shown by its kind, \"phone\")")
	deviceNameFlag := flag.String("device-name", defaultDeviceName, "the device's name in its station address, rig/<name>: a lowercase word of letters, digits, _ or -, such as iphone_1")
	model := flag.String("model", "", "the device's model name, such as \"iPhone 15 Pro Max\", shown on a station and told to agents")
	osVersion := flag.String("os-version", "", "the version of what the device runs, such as \"26.6\", shown on a station and told to agents")
	glide := flag.Bool("glide", false, "glide the pointer to each new spot along an arc, as a hand would, instead of jumping (a switch in /settings too)")
	hidBackendName := flag.String("hid-backend", hid.BackendRP2040, "input hardware: rp2040 (relay and HID boards) or linux-gadget (this machine's USB device port, set up by gadget/roadie-gadget)")
	encoderSpec := flag.String("encoder", "auto", "H.264 encoder for WebRTC: auto, v4l2m2m (Pi 4 hardware), ffmpeg, ffmpeg:<codec>, or none")
	rtcUDP := flag.Int("rtc-udp", 0, "UDP port for WebRTC media (default: the HTTP port's number)")
	relayFPS := flag.Int("relay-fps", 5, "frame rate cap for the MJPEG stream when it goes through a station")
	bindAddr := flag.String("bind", "", "address to listen on (default: 127.0.0.1 without --auth, otherwise every address)")
	authMode := flag.String("auth", "", "who may use the pages and API: token (the local admin token, see roadie token), tailscale (whoever tailscaled says), none (no checks); unset means localhost only")
	allow := flag.String("allow", "", "with --auth tailscale: comma-separated logins and tags allowed (default: anyone on the tailnet)")
	tlsCert := flag.String("tls-cert", "", "serve HTTPS with this certificate (PEM), reloaded when the file changes")
	tlsKey := flag.String("tls-key", "", "the certificate's private key (PEM)")
	httpRedirect := flag.String("http-redirect", "", "with TLS: also listen here (e.g. :80) and redirect to HTTPS")
	cpuProfile := flag.String("cpuprofile", "", "write a CPU profile here until exit (for go tool pprof)")
	flag.Parse()
	if err := checkDeviceName(*deviceNameFlag); err != nil {
		fmt.Fprintln(os.Stderr, "roadie:", err)
		os.Exit(2)
	}

	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			log.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal(err)
		}
		defer pprof.StopCPUProfile()
	}

	// Local access control, and where to listen: without --auth only this
	// machine may connect, so Roadie binds to localhost.
	auth, err := NewAuth(*authMode, *allow)
	if err != nil {
		log.Fatal(err)
	}
	bind := *bindAddr
	if auth.Mode == AuthUnset {
		if bind == "" {
			bind = "127.0.0.1"
		} else if !isLoopbackAddr(bind) {
			log.Fatalf("--bind %s needs --auth token, --auth tailscale or --auth none: without --auth Roadie listens on localhost only", bind)
		}
	}
	auth.Loopback = isLoopbackAddr(bind)
	if (*tlsCert == "") != (*tlsKey == "") {
		log.Fatal("--tls-cert and --tls-key go together")
	}
	if *httpRedirect != "" && *tlsCert == "" {
		log.Fatal("--http-redirect needs --tls-cert and --tls-key")
	}
	var certs *certReloader
	if *tlsCert != "" {
		if certs, err = newCertReloader(*tlsCert, *tlsKey); err != nil {
			log.Fatalf("TLS: %v", err)
		}
		auth.Secure = true
	}

	hidBackend, err := hid.ParseHIDBackend(*hidBackendName)
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
		for _, d := range audio.ListAudioDevices() {
			fmt.Printf("  - %s\n", d)
		}
		fmt.Println()
		fmt.Println("Relay boards:")
		relays := hid.ListRelays()
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

	if hidBackend == hid.BackendLinuxGadget && *relay != "" {
		fmt.Fprintln(os.Stderr, "⚠️  --relay is ignored with --hid-backend linux-gadget")
	}

	plat, err := platform.ParsePlatform(*platformName)
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
	var ab *audio.AudioBroadcaster
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
		ab = audio.NewAudioBroadcaster()
	}

	// With the gadget's speaker the phone can send its sound over USB or
	// HDMI -- Android picks USB, iOS either, and it can switch -- so /audio
	// follows whichever is live (audioMux). Without a speaker, the capture
	// dongle's audio is used as it always was.
	captureAudio := ab
	var mux *audio.AudioMux
	if hidBackend == hid.BackendLinuxGadget {
		cfg, err := readGadgetAudioConfig(defaultGadgetPaths())
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  USB audio: %v (no USB microphone or speaker until roadie restarts)\n", err)
		} else {
			var usbAudio *audio.AudioBroadcaster
			if cfg.hasSpeaker() {
				if ab == nil {
					ab = audio.NewAudioBroadcaster()
				}
				usbAudio = audio.NewAudioBroadcaster()
				var hdmiAudio *audio.AudioBroadcaster
				if captureAudio != nil {
					hdmiAudio = audio.NewAudioBroadcaster()
					captureAudio = hdmiAudio
				}
				mux = audio.NewAudioMux(ab, usbAudio, hdmiAudio)
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
		listenPort = findAvailablePort(bind, 8080)
	}

	// WebRTC: the picture as H.264 over a peer connection, for viewers that
	// ask for it. The encoder runs only while one is connected.
	var broadcaster *rtc.Broadcaster
	if *source == "" && *encoderSpec != "none" {
		udp := *rtcUDP
		if udp == 0 {
			udp = listenPort
		}
		opts := rtc.Options{UDPPort: udp, Encoder: *encoderSpec, FPS: *fps}
		if ab != nil {
			opts.Audio = rtcAudio{ab}
		}
		b, err := rtc.New(buf, opts)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "⚠️  WebRTC off: %v\n", err)
		case !b.Available():
			fmt.Fprintln(os.Stderr, "⚠️  WebRTC off: no H.264 encoder on this host (--encoder); MJPEG only")
			b.Close()
		default:
			broadcaster = b
			fmt.Printf("📹 WebRTC: H.264 via %s, UDP port %d\n", b.Status().Encoder, udp)
		}
	}

	// Register mDNS (for service discovery via dns-sd -B _roadie._tcp),
	// unless nothing off this machine could reach the port anyway.
	var mdnsShutdown func()
	if !auth.Loopback {
		resolution := fmt.Sprintf("%dx%d", *width, *height)
		mdnsShutdown, err = RegisterMDNS(*name, listenPort, resolution)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  mDNS registration failed: %v\n", err)
		}
	}

	// Start the HID backend: the relay boards, or this machine's USB gadget.
	var input hid.HIDBackend
	if hidBackend == hid.BackendLinuxGadget {
		g := NewGadgetHID()
		if gadgetAudio != nil && gadgetAudio.Mic != nil {
			g.SetMic(gadgetAudio.Mic)
		}
		input = g
		fmt.Printf("🔌 HID: USB gadget %q (linux-gadget backend)\n", gadgetName)
	} else {
		input = hid.NewHIDController(*relay)
		if relays := hid.ListRelays(); len(relays) > 1 && *relay == "" {
			fmt.Fprintf(os.Stderr, "\n⚠️  %d relay boards connected — use --relay to pick one:\n", len(relays))
			for _, r := range relays {
				fmt.Fprintf(os.Stderr, "   %s\n", r)
			}
			fmt.Fprintln(os.Stderr, "")
		}
	}
	settled := hid.NewSettledHID(input)
	settled.TouchAsPointer = plat.TouchAsPointer()
	settled.WheelIOS = plat == platform.PlatformIOS
	settled.SetGlide(*glide)
	input = settled
	go input.Run()
	if gadgetAudio != nil {
		gadgetAudio.Start()
	}
	muxCtx, stopMux := context.WithCancel(context.Background())
	defer stopMux()
	if mux != nil {
		go mux.Run(muxCtx)
	}

	// Print startup banner.
	fmt.Println()
	scheme := "http"
	if certs != nil {
		scheme = "https"
	}
	fmt.Printf("🌐 %s://localhost:%d\n", scheme, listenPort)
	if !auth.Loopback {
		if ip := getLANIP(); ip != "" {
			fmt.Printf("🌐 %s://%s:%d\n", scheme, ip, listenPort)
		}
		fmt.Printf("🌐 %s://%s.local:%d\n", scheme, MDNSHostname(*name), listenPort)
	}
	switch auth.Mode {
	case AuthUnset:
		fmt.Println("🔒 Listening on localhost only. To serve the network: --auth token, --auth tailscale or --auth none")
	case AuthToken:
		fmt.Println("🔐 Local auth: token. Browsers sign in at /login; agents send Authorization: Bearer. See: roadie token")
	case AuthTailscale:
		who := "anyone on the tailnet"
		if len(auth.Allow) > 0 {
			who = strings.Join(auth.Allow, ", ")
		}
		fmt.Printf("🔐 Local auth: tailscale (%s)\n", who)
	case AuthNone:
		fmt.Fprintln(os.Stderr, "⚠️  --auth none: no login. Anyone who can reach this port controls the target.")
	}
	if certs != nil {
		if leaf := certs.Leaf(); leaf != nil {
			fmt.Printf("🔏 TLS: %v, until %s\n", leaf.DNSNames, leaf.NotAfter.Local().Format("2006-01-02"))
		}
	}

	// Start HTTP server.
	srv := &Server{
		Source:         buf,
		Buf:            buf,
		RelayFPS:       *relayFPS,
		Device:         deviceName,
		AudioBroadcast: ab,
		SourceType:     sourceType,
		HID:            input,
		Capture:        cm,
		InputMode:      *inputMode,
		Platform:       plat,
		Name:           *name,
		Label:          *label,
		Model:          *model,
		OSVersion:      *osVersion,
		CropFile:       cropFile,
		RTC:            broadcaster,
		Auth:           auth,
	}
	if mux != nil {
		srv.AudioSource = mux.Source
	}
	if broadcaster != nil {
		var mic func([]byte) error
		if input != nil {
			mic = input.SendAudio
		}
		broadcaster.SetInput(srv.hidCommand, mic)
	}
	settled.Turned = srv.pointerTurned
	handler := NewMux(srv)
	httpServer := &http.Server{
		Addr:    net.JoinHostPort(bind, strconv.Itoa(listenPort)),
		Handler: auth.Wrap(handler),
	}
	// Enrolled with a station: a tunnel to it, serving the same mux behind
	// the rig's gate instead of the local login.
	// A hold the policy says to record is recorded (see record.go): the
	// recorder hears every input and learns the hold from the rig.
	var rigRun *rigRunner
	recDir, _ := rigPath("recordings")
	recorder := newRecorder(buf, func() (*rig.StationEntry, string, string, nostr.SecretKey, bool) {
		if rigRun == nil {
			return nil, "", "", nostr.SecretKey{}, false
		}
		return rigRun.current()
	}, recDir)
	settled.OnInput = recorder.input
	onHoldEnd := func(_, holdID string) {
		if broadcaster != nil {
			broadcaster.EndHold(holdID)
		}
		recorder.end(holdID)
	}
	rigRun, err = startRig(handler, deviceOffer(*deviceNameFlag, plat, input != nil, ab != nil, srv.effectiveInputMode(), *label, *model, *osVersion), onHoldEnd)
	if err != nil {
		log.Fatalf("station: %v", err)
	}
	srv.Station = rigRun
	if rigRun != nil {
		go recorder.retry(rigRun.rig.Policy, rigRun.rig.Key)
	}
	if certs != nil {
		httpServer.TLSConfig = &tls.Config{GetCertificate: certs.GetCertificate, MinVersion: tls.VersionTLS12}
	}
	var redirectServer *http.Server
	if *httpRedirect != "" {
		redirectServer = &http.Server{Addr: *httpRedirect, Handler: httpsRedirect(listenPort)}
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		var err error
		if certs != nil {
			err = httpServer.ListenAndServeTLS("", "")
		} else {
			err = httpServer.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()
	if redirectServer != nil {
		go func() {
			if err := redirectServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTP redirect server error: %v", err)
			}
		}()
	}

	// Under systemd (Type=notify): say so once the port answers, then keep
	// answering the watchdog only while it still does, so a wedged roadie
	// is restarted rather than kept.
	answers := func() bool { return portAnswers(certs != nil, bind, listenPort) }
	for i := 0; i < 50 && !answers(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	sdnotify.Ready()
	watchCtx, stopWatch := context.WithCancel(context.Background())
	go sdnotify.Keep(watchCtx, answers)

	<-done
	stopWatch()
	sdnotify.Stopping()
	fmt.Println("\nShutting down...")

	// Recordings in progress are closed and uploaded; one the exit cuts
	// off stays on disk for the next start.
	recorder.endAll()
	if rigRun != nil {
		rigRun.shutdown()
	}
	if broadcaster != nil {
		broadcaster.Close()
	}
	shutdownFunc()
	input.Shutdown()
	if gadgetAudio != nil {
		gadgetAudio.Shutdown()
	}

	if mdnsShutdown != nil {
		mdnsShutdown()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)
	if redirectServer != nil {
		redirectServer.Shutdown(ctx)
	}
}

// httpsRedirect sends plain HTTP to the HTTPS port.
func httpsRedirect(port int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if port != 443 {
			host = net.JoinHostPort(host, strconv.Itoa(port))
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}

// findAvailablePort tries ports starting from start and returns the first one available.
// portAnswers reports whether roadie's own port replies at all. The status
// does not matter, so a refusal from --auth counts as an answer.
func portAnswers(tlsOn bool, bind string, port int) bool {
	host := bind
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "http"
	client := &http.Client{Timeout: 5 * time.Second}
	if tlsOn {
		scheme = "https"
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // our own listener
	}
	resp, err := client.Get(scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func findAvailablePort(bind string, start int) int {
	for p := start; p < start+100; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(p)))
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

// rtcAudio is the capture's audio as the WebRTC broadcaster wants it.
type rtcAudio struct{ *audio.AudioBroadcaster }

func (a rtcAudio) AudioFormat() (int, int) {
	p := a.Params()
	return p.SampleRate, p.Channels
}

func (a rtcAudio) AudioActive() bool { return a.IsActive() }
