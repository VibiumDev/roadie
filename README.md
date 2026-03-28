# 🚐 Roadie

Roadie sets up your equipment.

## The Problem

Setting up a new machine requires someone to physically sit in front of it, click through the OS setup assistant, install dependencies, and configure the system. This doesn't scale. If you're managing a fleet of machines, you need zero-touch provisioning.

The problem: you can't automate device setup *with software on the device* because the software isn't installed yet. The OS isn't even fully configured. There's no SSH, no VNC, no remote desktop. All you have is a screen showing "Select Your Language" and a USB keyboard/mouse waiting for input.

## What Roadie Does

Roadie uses one device to set up another. It grabs video from an HDMI-to-USB capture dongle and serves it over HTTP, turning a remote device's physical display into a web page. An AI agent (or a human) can view the screen in a browser, grab individual frames for vision analysis, and eventually send keyboard/mouse input back to the device.

### Why not MDM?

Mobile Device Management (MDM) tools let you configure and control devices remotely over the network. They're great for ongoing management, but complex to set up and limited to specific platforms. Roadie tackles the initial setup part: use AI to bootstrap *any* device that supports KVM (video output + keyboard/mouse input): Macs, PCs, mobile devices, servers, embedded devices, anything with an HDMI port.

## Quick Start

```bash
make
./roadie
```

Open `http://localhost:8080/view` or `http://roadie.local:8080/view` (Bonjour).

## Features

- **Auto-detection**: finds external capture devices, skips built-in cameras
- **Hot-swap**: plug/unplug devices without restarting
- **Auto-crop**: detects and removes black bars from HDMI capture (pillarbox/letterbox)
- **Audio streaming**: optional PCM audio over WebSocket
- **Bonjour/mDNS**: discoverable as `roadie.local` on your network
- **Resilient**: automatic reconnection with exponential backoff, signal loss detection

## Endpoints

| Endpoint | Description |
|---|---|
| `/view` | Live feed in your browser (with audio toggle) |
| `/stream` | MJPEG stream (auto-cropped) |
| `/snapshot` | Single JPEG frame (auto-cropped) |
| `/raw-stream` | MJPEG stream (uncropped) |
| `/raw-snapshot` | Single JPEG frame (uncropped) |
| `/health` | JSON status (device, resolution, crop rect, audio) |
| `/audio` | WebSocket PCM audio stream |

## CLI Flags

```
--device    Device name filter (default: auto-detect)
--port      HTTP port (default: auto, starting at 8080)
--width     Capture width (default: 1920)
--height    Capture height (default: 1080)
--fps       Capture framerate (default: 30)
--quality   JPEG quality 1-100 (default: 80)
--name      Bonjour service name (default: roadie)
```

## Parts Needed

- 2x Adafruit QT Py RP2040
- 2x USB-C data cables (not charge-only)
- 1x STEMMA QT / Qwiic I2C cable (for connecting the two boards)
- 1x UVC-compatible HDMI-to-USB capture dongle
- (Optional) 3D-printed enclosure

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

5. The NeoPixel should blink **blue**. Label it **📥 IN**.

## Connect and Run

1. Connect the two boards with the STEMMA QT cable
2. Plug **📥 IN** into your host machine
3. Plug **📤 OUT** into the target device
4. Build and run:
   ```bash
   make
   make run
   ```

## Re-flashing

If you only need to update the Python code (not the CircuitPython firmware):
```bash
make flash-hid-quick
make flash-relay-quick
```

## Troubleshooting

- **"CIRCUITPY not mounted"**: make sure you're using a USB data cable, not a charge-only cable. On Linux, ensure the volume auto-mounts (install `udisks2` if needed).
- **"No serial port found"**: the board may not have CircuitPython installed yet. The script will fall back to manual bootloader entry. On Linux, make sure your user is in the `dialout` group.
- **"Buffer incorrect size"**: you forgot to unplug and re-plug after flashing. The board needs a full USB re-enumeration for the custom HID descriptor in boot.py to take effect.
- **NeoPixel doesn't blink after flashing**: connect to the serial REPL to check for errors. Press Ctrl-C to interrupt, then Ctrl-D to soft-reboot.

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
make          # build binary
make test     # run tests with -race
make clean    # remove binary
```
