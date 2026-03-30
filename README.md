# 🚐 Roadie

Roadie sets up your equipment. Hardware KVM for AI-driven device provisioning.

## The Problem

Setting up a new machine requires someone to physically sit in front of it, click through the OS setup assistant, install dependencies, and configure the system. This doesn't scale. If you're managing a fleet of machines, you need zero-touch provisioning.

The problem: you can't automate device setup *with software on the device* because the software isn't installed yet. The OS isn't even fully configured. There's no SSH, no VNC, no remote desktop. All you have is a screen showing "Select Your Language" and a USB keyboard/mouse waiting for input.

## What Roadie Does

Roadie uses one device to set up another. It grabs video from an HDMI-to-USB capture dongle and serves it over HTTP, turning a remote device's physical display into a web page. A pair of microcontroller boards act as a USB KVM, sending keyboard and mouse input to the target device. An AI agent (or a human) can view the screen in a browser, grab frames for vision analysis, and send input back to the device — all over HTTP.

### Why not VNC/KVM/MDM?

**VNC (Virtual Network Computing) and remote desktop** require software running on the target. That's a non-starter for initial setup — you can't install VNC on a device that's still showing "Select Your Language."

**Hardware KVM (Keyboard, Video, Mouse) switches** (like PiKVM or TinyPilot) work at the hardware level, but they're designed for servers and desktops. They don't support touch input, so they can't drive phones or tablets through setup. Roadie includes a multi-touch digitizer alongside keyboard and mouse, so it works with mobile devices too.

**MDM (Mobile Device Management)** tools manage devices over the network but require enrollment, platform-specific agents, and an already-configured OS. Roadie tackles the part that comes before all of that: use AI to bootstrap *any* device with video output and USB input — Macs, PCs, phones, tablets, servers, embedded devices.

## Quick Start

1. [Flash both boards](#flash-the-boards) (first time only)
2. Connect the boards with jumper wires (TX-to-RX, RX-to-TX, GND-to-GND)
3. Plug the **relay** board into your host machine
4. Plug the **HID** board into the target device
5. Plug an HDMI capture dongle between the target's video output and the host
6. Build and run:
   ```bash
   make
   ./roadie
   ```
7. Open `http://localhost:8080/view` or `http://roadie.local:8080/view`

## Features

- **HID control**: absolute mouse, multi-touch digitizer, and keyboard input via USB, controllable over HTTP/WebSocket
- **Interactive viewer**: `/view` page with live feed and full HID control (mouse, touch, keyboard) directly on the stream
- **Auto-detection**: finds external capture devices, skips built-in cameras
- **Hot-swap**: plug/unplug devices without restarting
- **Auto-crop**: detects and removes black bars from HDMI capture (pillarbox/letterbox), remaps touch coordinates to match
- **Runtime settings**: adjust quality, FPS, and resolution on the fly from the viewer
- **Audio streaming**: optional PCM audio over WebSocket
- **Bonjour/mDNS**: discoverable as `roadie.local` on your network
- **Cross-platform**: runs on macOS and Linux (Raspberry Pi, Ubuntu, etc.)
- **Resilient**: automatic reconnection with exponential backoff, signal loss detection
- **Test page**: interactive `/test` page with MJPEG overlay, mouse trackpad, keyboard input, and key combos

## Endpoints

| Endpoint | Description |
|---|---|
| `/view` | Live feed with HID control (mouse, touch, keyboard), audio, and settings |
| `/test` | HID test page with MJPEG overlay, mouse/touch trackpad, keyboard, and combos |
| `/stream` | MJPEG stream (auto-cropped) |
| `/snapshot` | Single JPEG frame (auto-cropped) |
| `/raw-stream` | MJPEG stream (uncropped) |
| `/raw-snapshot` | Single JPEG frame (uncropped) |
| `/health` | JSON status (device, resolution, crop rect, audio) |
| `/audio` | WebSocket PCM audio stream |
| `/api/hid/*` | HID control (mouse, keyboard, combos) |
| `/api/hid/ws` | WebSocket for real-time HID control |

See [API.md](API.md) for the full HTTP API reference, including HID endpoints and USB keycode tables.

## Documentation

| Doc | Description |
|-----|-------------|
| [API Reference](API.md) | HTTP/WebSocket endpoints, request/response formats |
| [Protocol Reference](docs/reference/protocol.md) | Binary wire format between relay and HID boards |
| [Board Reference](docs/reference/boards.md) | Hardware specs, wiring, USB identifiers, LED behavior |
| [Architecture](docs/explanation/architecture.md) | System design, pipeline, and design decisions |
| [Flash Boards](docs/how-to/flash-boards.md) | How to flash and re-flash the boards |
| [Troubleshooting](docs/how-to/troubleshooting.md) | Common problems and fixes |
| [REPL Tutorial](docs/tutorials/repl.md) | Interactive debugging via CircuitPython REPL |

## CLI Flags

```
--device         Device name filter (default: auto-detect)
--source         HTTP MJPEG source URL (mutually exclusive with --device)
--port           HTTP port (default: auto, starting at 8080)
--width          Capture width (default: 1920)
--height         Capture height (default: 1080)
--fps            Capture framerate (default: 30)
--quality        JPEG quality 1-100 (default: 80)
--name           Bonjour service name (default: roadie)
--list-devices   List video and audio devices, then exit
```

## Parts Needed

- 2x Adafruit QT Py RP2040
- 2x USB-C data cables (not charge-only)
- 3x jumper wires (TX, RX, GND for UART between the two boards)
- 1x UVC-compatible HDMI-to-USB capture dongle
- (Optional) 3D-printed enclosure
- (Mobile targets) USB-C 3-in-1 dongle with HDMI out, USB-A port (for HID board), and USB-C power delivery (to charge the phone while providing peripherals)

## Prerequisites

- macOS or Linux (Raspberry Pi OS, Ubuntu, etc.)
- Go 1.21+
- Python 3.10+

## Setup

```bash
git clone <repo-url> && cd roadie
make setup
```

## Flash the Boards

Only one board can be flashed at a time.

1. Plug in the first QT Py (this will be the **📤 OUT / HID** board):
   ```bash
   make flash-hid
   ```
   The script will guide you through any manual steps (holding BOOT button, etc.)

2. Once flashing completes, the board's NeoPixel should blink **green**. That means it worked.

3. Unplug it. Label it **📤 OUT**.

4. Plug in the second QT Py:
   ```bash
   make flash-relay
   ```

5. The NeoPixel should glow **red**. Label it **📥 IN**.

## Connect and Run

1. Connect the two boards with jumper wires: TX-to-RX, RX-to-TX, GND-to-GND
2. Plug **📥 IN** (relay) into your host machine (e.g. Raspberry Pi)
3. Plug **📤 OUT** (HID) into the target device
4. Plug in the HDMI capture dongle between the target's video output and the host
5. Build and run:
   ```bash
   make
   ./roadie
   ```
6. Open `http://localhost:8080/view` to see and control the target's screen

## Re-flashing

If you only need to update the Python code (not the CircuitPython firmware):
```bash
make flash-hid-quick
make flash-relay-quick
```

## Troubleshooting

See [docs/how-to/troubleshooting.md](docs/how-to/troubleshooting.md) for common issues with flashing, serial connections, UART communication, and video capture.

## Development

### macOS

```bash
brew install go python@3
```

### Linux (Debian/Ubuntu)

```bash
sudo apt install golang python3 python3-venv python3-pip libv4l-dev libasound2-dev
```

### Build

```bash
make                  # build binary
make test             # run tests
make test-circular    # end-to-end test (type + mouse) through both boards
make clean            # remove binary
```

## Architecture

```
                    Host (Pi, Mac, Linux)
               +--------------------------+
               |   Go server (roadie)     |
Browser  <---->|   HTTP / WebSocket       |
               |   Serial JSON            |
               +------+-------------------+
                      | USB serial
               +------v-------------------+
               |   Relay board (QT Py)    |
               |   JSON -> binary         |
               +------+-------------------+
                      | UART (921600 baud)
               +------v-------------------+
               |   HID board (QT Py)      |
               |   binary -> USB HID      |
               +------+-------------------+
                      | USB HID
               +------v-------------------+
               |   Target device          |
               |   (Mac, PC, Android, iOS)|
               +--------------------------+
```
