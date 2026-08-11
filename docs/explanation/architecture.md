# Architecture

How Roadie's components fit together and the design decisions behind them.

## Pipeline

```
Browser / AI agent
       ↕ HTTP / WebSocket
Go server (roadie binary)
       ↕ USB serial (JSON)
Relay board (QT Py RP2040)
       ↕ UART 921600 baud (binary protocol)
HID board (QT Py RP2040)
       ↕ USB HID
Target device
```

In parallel, HDMI video flows in the opposite direction:

```
Target device
       ↕ HDMI
Capture dongle (UVC)
       ↕ USB video
Go server
       ↕ MJPEG / JPEG
Browser / AI agent
```

The Go server bridges both paths: it serves the target's screen as an MJPEG stream and accepts HID commands over HTTP, forwarding them to the relay board.

## Two-Board Design

Roadie uses two microcontroller boards instead of one because they serve different roles with different USB hosts:

- **Relay board** — plugged into the host machine (Pi, Mac, etc.). Receives JSON commands from the Go server over USB serial. Translates to binary and forwards over UART.
- **HID board** — plugged into the target device. Presents as a USB keyboard, absolute mouse, and multi-touch digitizer. Executes commands received over UART.

The boards communicate over a 3-wire UART connection (TX, RX, GND). A common ground wire is essential when the boards are powered from different USB hosts — without it, the UART voltage levels are undefined and the HID board reads noise.

## Why Not One Board?

A single board can't be simultaneously a USB serial device (receiving commands from the host) and a USB HID device (sending input to the target). These are different USB hosts. The relay/HID split keeps each board's USB role simple and avoids USB OTG complexity.

## Video Capture

The Go server captures video from a UVC-compatible HDMI-to-USB dongle:
- **macOS**: AVFoundation via `pion/mediadevices`
- **Linux**: V4L2 via `pion/mediadevices`

Frames are JPEG-encoded and stored in a shared buffer. The server maintains two versions of each frame:
- **Cropped**: black bars (letterbox/pillarbox) detected and removed
- **Raw**: full capture resolution (for diagnostics)

### Auto-Crop

The crop detection algorithm scans inward from each edge of the frame to find non-black content:
- Threshold: pixel brightness > 30 (handles limited-range YCbCr where Y=16 is black)
- Hysteresis: crop rectangle only updates on >20% area change or aspect ratio flip (prevents frame-to-frame jitter)
- Runs on first frame and then every ~1 second

Crop metadata is exposed via `/health` and used by the viewer to remap touch coordinates.

### Coordinate Remapping

When the HDMI capture has black borders (e.g., a phone outputting a different aspect ratio), the displayed stream shows only the content area. Touch coordinates from the viewer are normalized (0-1) across the visible content, then remapped into the full capture frame:

```
absolute_x = (crop_x + normalized_x * crop_width) / full_width * 32767
absolute_y = (crop_y + normalized_y * crop_height) / full_height * 32767
```

This ensures touches land on the correct position on the target device, not shifted by the crop offset.

## HID Input

The HID board presents three USB devices to the target:

1. **Keyboard** — standard USB HID keyboard with US layout. Supports individual key press/release and bulk text typing.
2. **Absolute Mouse** — custom HID descriptor with 16-bit absolute X/Y positioning (0-32767), 8 buttons, vertical wheel, and horizontal pan. Absolute positioning means the cursor jumps directly to the specified location (like a graphics tablet), rather than moving relative to current position.
3. **Multi-Touch Digitizer** — custom HID descriptor supporting 2 simultaneous contact points. Enables native touch gestures (tap, drag, scroll, pinch-to-zoom) on touch-enabled targets like phones and tablets.

## Fire-and-Forget Commands

High-frequency commands (mouse_move, key_press, key_release, mouse_scroll, touch) use "nowait" mode: the relay sends the binary message over UART but does not wait for the HID board's 2-byte response. This prevents the relay from blocking during rapid input sequences.

The HID board still sends responses for nowait commands. Stale responses are flushed from the UART buffer before each new send.

The HID board also skips its LED blink for high-frequency commands to avoid 100ms of back-pressure per command.

## Rate Limiting

The web UI coalesces input events before sending to avoid flooding the HID pipeline:

| Input | Interval | Description |
|-------|----------|-------------|
| Mouse move | 100ms | Only latest position sent |
| Touch contacts | 50ms | Only latest contacts sent |
| Scroll wheel | 50ms | Accumulated scroll amount sent |

## Cross-Platform Relay Detection

The Go server enumerates connected relay boards and their USB serial numbers:
- **Linux**: globs `/dev/serial/by-id/usb-Adafruit_Roadie-Relay_*-if02` (the CDC data interface). The serial number is embedded in the symlink name.
- **macOS**: runs `ioreg -n Roadie-Relay` to find every device by USB product name, grouping each board's `USB Serial Number` with its data port (the last IOCalloutDevice under that device)

With one board connected it binds automatically. With several, `--relay <substring>`
selects one by serial number; an absent, ambiguous, or unmatched selector is an
error rather than an arbitrary pick, because two instances silently sharing one
board would interleave input. See [Multiple Targets](#multiple-targets).

## Multiple Targets

A `Server` owns exactly one capture source and one relay board, so driving several
targets from one host means running one `roadie` process per target — each pinned
with `--device`, `--relay`, `--port`, and `--name`. See
[Running Multiple Targets](../how-to/multiple-targets.md) for the how-to.

This keeps the blast radius small: a wedged capture dongle, a board reset, or a
crash affects only its own target, and the OS schedules each capture and JPEG
encode pipeline independently. The trade-off is that nothing is shared — each
instance has its own frame buffer, its own BiDi session, and its own mDNS name.

Aggregation belongs above the servers, not inside them. A page that embeds each
instance's `/view` in an iframe gets a combined display with full interactivity:
each iframe is same-origin with its own server, so streams, the HID WebSocket, and
audio all work untouched. Because the viewer normalizes pointer coordinates
against the displayed image (see [Coordinate Remapping](#coordinate-remapping))
rather than assuming a 1:1 pixel mapping, the embedded views can be scaled to fit
without breaking input.

## Mobile Targets

For phones and tablets, the target device needs a USB-C hub/dongle that provides:
- HDMI output (for video capture)
- USB-A port (for the HID board)
- USB-C power delivery (to charge the phone while providing peripherals)

The HID board's multi-touch digitizer enables native touch interaction, so the phone can be driven through setup without needing mouse emulation of touch events.

## Source Files

| File | Description |
|------|-------------|
| `main.go` | Startup, flag parsing, component wiring |
| `server.go` | HTTP handlers, /view page, /test page, HID API |
| `hid.go` | Relay board discovery and selection, serial connection, command methods |
| `capture.go` | Frame capture, crop detection, frame buffer |
| `bidi.go` | WebDriver BiDi endpoint (`/session`) |
| `audio.go` | Audio capture and WebSocket broadcast |
| `httpsource.go` | MJPEG ingest from another Roadie's `/raw-stream` |
| `mdns.go` | Bonjour/mDNS service registration |
| `board/` | CircuitPython code for both boards |
