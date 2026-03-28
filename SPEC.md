# Roadie v0.1 Spec

## The problem

Setting up a new machine requires someone to physically sit in front of it, click through the OS setup assistant, install dependencies, and configure the system. This doesn't scale. If you're managing a fleet of machines, you need zero-touch provisioning.

The catch-22: you can't automate macOS setup *with software on the Mac* because the software isn't installed yet. The OS isn't even fully configured. There's no SSH, no VNC, no remote desktop — just a screen showing "Select Your Language" and a USB keyboard/mouse waiting for input.

## What Roadie does

Roadie solves this with a USB KVM approach. It's a Go binary that grabs video from an HDMI-to-USB capture dongle and serves it over HTTP — turning a remote machine's physical display into a web page. A pair of QT Py RP2040 boards act as a USB HID device, sending keyboard and mouse input to the target. An AI agent (or a human) can view the screen in a browser, grab individual frames for vision analysis, and send keyboard/mouse input back to the machine — all over HTTP/WebSocket.

## Usage

```
roadie
```

That's it. No flags required. Roadie auto-detects the capture device and picks an available port.

```
$ roadie
📺 Found "USB Video" capture device
🎬 Capturing at 1920x1080 @ 30fps
🌐 http://localhost:8080
🌐 http://10.0.1.42:8080
🌐 http://roadie.local:8080
```

### Optional flags

| Flag | Default | Description |
|------|---------|-------------|
| `--device` | auto-detect | Device name substring (e.g. `--device "USB Video"`) |
| `--port` | auto (8080, then 8081, etc.) | HTTP server port |
| `--width` | `1920` | Capture width |
| `--height` | `1080` | Capture height |
| `--fps` | `30` | Capture framerate |
| `--quality` | `80` | JPEG compression quality (1-100) |
| `--name` | `roadie` | Bonjour service name (for multiple Roadies on one network) |

### Device auto-detection

Roadie uses `pion/mediadevices` to enumerate available capture devices via AVFoundation on macOS. At startup it lists all video devices, skips the built-in camera, and picks the first external/USB capture device.

Heuristic for identifying the dongle vs the built-in camera:
- Skip anything named "FaceTime" or "iPhone"
- Prefer anything named "USB", "HDMI", "Capture", or "Video"
- If ambiguous, list the options and ask the user

If `--device` is provided, match it as a substring against device names.

### Port selection

Roadie tries port 8080 first. If it's taken, it increments (8081, 8082, ...) until it finds an open port. The chosen port is printed to stdout and advertised via Bonjour.

### Bonjour / mDNS discovery

Roadie registers itself on the local network using Bonjour:

- Service type: `_roadie._tcp`
- Service name: value of `--name` flag (default: `roadie`)
- TXT records: `version=0.1`, `resolution=1920x1080`

Clients can discover Roadie without knowing the IP or port:

```bash
# Find all Roadie instances on the network
dns-sd -B _roadie._tcp

# Resolve a specific instance
dns-sd -R roadie _roadie._tcp local
```

From a Vibium script or Node.js, use an mDNS library like `bonjour-service` to discover the Roadie URL automatically:

```javascript
import Bonjour from 'bonjour-service';
const bonjour = new Bonjour();
bonjour.find({ type: 'roadie' }, (service) => {
  const roadieUrl = `http://${service.host}:${service.port}`;
  // now open this in the browser or hit /snapshot
});
```

For Go, use `github.com/grandcat/zeroconf` to register the service.

## Endpoints

### `GET /`

Index page. Minimal, friendly. Lists available endpoints with short descriptions.

```
Roadie

  /view      — watch the live feed in your browser
  /stream    — raw MJPEG stream
  /snapshot  — grab a single frame (JPEG)
  /health    — service status (JSON)
```

Serve as HTML with basic styling. Nothing fancy.

### `GET /view`

HTML page for humans. Embeds the MJPEG stream in an `<img>` tag. Fills the viewport, black background, no scrollbars.

```html
<!DOCTYPE html>
<html>
<head><title>Roadie</title></head>
<body style="margin:0; background:#000; display:flex; justify-content:center; align-items:center; height:100vh;">
  <img src="/stream" style="max-width:100%; max-height:100vh;">
</body>
</html>
```

### `GET /stream`

Raw MJPEG stream. Content type `multipart/x-mixed-replace; boundary=frame`. Each frame is a JPEG image.

```
--frame
Content-Type: image/jpeg
Content-Length: {size}

{jpeg bytes}
--frame
...
```

This is what `/view` embeds. Can also be opened directly in browsers, VLC, or any MJPEG-capable client.

### `GET /snapshot`

Returns a single JPEG frame. Content type `image/jpeg`. This is the primary endpoint for automation — grab one frame, send it to Claude's vision API, get coordinates back.

### `GET /health`

Returns `200 OK` with JSON:

```json
{
  "status": "ok",
  "device": "USB Video",
  "resolution": "1920x1080",
  "fps": 30
}
```

## Architecture

```
HDMI Dongle (AVFoundation) → pion/mediadevices → frame loop → current frame buffer
                                                                      ↓
                                                        HTTP server (net/http)
                                                          ├── /          → index page
                                                          ├── /view      → HTML viewer
                                                          ├── /stream    → MJPEG stream
                                                          ├── /snapshot  → single JPEG
                                                          └── /health    → status JSON
                                                               ↕
                                                        Bonjour/mDNS advertisement
```

### Frame loop

A single goroutine reads frames from the capture device. Each frame is JPEG-encoded and stored in a shared buffer. This is the only goroutine that touches the capture device.

```go
type FrameServer struct {
    mu      sync.RWMutex
    current []byte // latest JPEG frame
    quality int
}
```

When a new frame arrives:
1. Encode to JPEG with the configured quality
2. Lock, replace `current`, unlock

### Stream handler

Each `/stream` connection loops independently: grab the latest frame under a read lock, write it to the response, sleep until the next frame interval. No fan-out, no channels, no client registration. Multiple clients just read from the same buffer.

```go
func (fs *FrameServer) handleStream(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
    for {
        select {
        case <-r.Context().Done():
            return
        default:
            fs.mu.RLock()
            frame := fs.current
            fs.mu.RUnlock()
            // write MJPEG boundary + headers + frame
            time.Sleep(33 * time.Millisecond) // ~30fps
        }
    }
}
```

### Snapshot handler

`/snapshot` grabs `current` under a read lock and writes it to the response. One and done.

## Capture device notes

HDMI-to-USB dongles are UVC devices. macOS handles UVC natively through AVFoundation — no extra drivers needed.

`pion/mediadevices` wraps AVFoundation on macOS (and V4L2 on Linux) and exposes capture devices as Go-native media tracks. It supports device enumeration, format negotiation, and frame capture without cgo wrangling or external dependencies beyond the Go module itself.

Most cheap Amazon HDMI dongles output MJPEG or NV12/YUY2 natively. `pion/mediadevices` handles codec negotiation.

To list devices from the command line for debugging:

```bash
system_profiler SPCameraDataType
```

## Error handling

- If no capture device is found: exit with a clear error message, suggest running `system_profiler SPCameraDataType` to list devices.
- If the only device found is the built-in FaceTime camera: warn the user that no external capture device was detected.
- If the capture device stops producing frames: log a warning, keep trying. Dongles sometimes hiccup when the source device changes resolution (e.g., macOS login screen → desktop).

## Project structure

```
roadie/
├── main.go          # flag parsing, startup, wiring
├── capture.go       # device detection, frame loop, FrameSource interface
├── capture_test.go
├── server.go        # HTTP handlers (index, view, stream, snapshot, health)
├── server_test.go
├── mdns.go          # Bonjour registration
├── Makefile
├── go.mod
├── go.sum
├── README.md
└── SPEC.md
```

One package, `package main`, flat layout. No `cmd/`, no `internal/`, no `pkg/`. Restructure later if needed.

## Build and run

```
make        # build the binary
make test   # run tests
./roadie    # run it
```

### Makefile

```makefile
BINARY = roadie

.PHONY: build test clean

build:
	go build -o $(BINARY) .

test:
	go test -race -v ./...

clean:
	rm -f $(BINARY)
```

Dependencies:
- Go 1.21+
- `github.com/pion/mediadevices` — capture device access
- `github.com/grandcat/zeroconf` — Bonjour/mDNS registration

No opencv. No ffmpeg. No brew install. Just `go build`.

## Testing

The video capture device isn't available inside the VM where Claude Code builds this. All tests must work without real hardware.

### Design for testability

`capture.go` should define an interface that the HTTP handlers depend on, not a concrete capture implementation:

```go
type FrameSource interface {
    Latest() []byte // returns the most recent JPEG frame
}
```

The real implementation reads from `pion/mediadevices`. Tests use a fake that returns a static JPEG or a sequence of generated frames.

### What to test

**server_test.go** — HTTP handler tests using `httptest`:
- `GET /` returns HTML with links to `/view`, `/stream`, `/snapshot`, `/health`
- `GET /view` returns HTML with an `<img src="/stream">`
- `GET /snapshot` returns a valid JPEG (`Content-Type: image/jpeg`, bytes start with `0xFFD8`)
- `GET /stream` returns `Content-Type: multipart/x-mixed-replace` and at least one JPEG frame before the client disconnects
- `GET /health` returns valid JSON with expected fields

**capture_test.go** — frame buffer logic:
- `FrameServer` stores a frame and `Latest()` returns it
- Concurrent reads don't race (run with `-race`)
- Updating the frame while readers are reading doesn't panic

**mdns_test.go** — if practical, verify that the Bonjour registration call doesn't error. Don't test actual network discovery — that needs a real network.

### Test frame helper

Create a helper that generates a minimal valid JPEG for tests:

```go
func testJPEG(width, height int) []byte {
    img := image.NewRGBA(image.Rect(0, 0, width, height))
    // fill with a solid color
    var buf bytes.Buffer
    jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
    return buf.Bytes()
}
```

### Project structure with tests

```
roadie/
├── main.go
├── capture.go
├── capture_test.go
├── server.go
├── server_test.go
├── mdns.go
├── Makefile
├── go.mod
├── go.sum
├── README.md
└── SPEC.md
```

## What v0.1 does NOT include

- ~~Mouse/keyboard input (QT Py integration)~~ — done (Phase 3D)
- WebSocket or WebRTC — MJPEG is fine for now (WebSocket used for audio + HID control)
- Authentication — runs on a trusted local network
- TLS — same reason
- Recording or frame storage

## Success criteria

1. `roadie` starts and opens the capture device without error
2. `http://localhost:8080/view` shows live video from the dongle in a browser
3. `http://localhost:8080/snapshot` returns a JPEG that can be sent to Claude's vision API
4. Latency from dongle to browser is under 200ms
5. Multiple browser tabs can view the stream simultaneously
6. Process stays stable for hours without memory leaks or frame buffer growth
