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

## Requirements

- macOS or Linux
- Go 1.21+, Python 3.10+
- UVC-compatible HDMI-to-USB capture dongle

### macOS

```bash
brew install go python@3
```

### Linux (Debian/Ubuntu)

```bash
sudo apt install golang python3 python3-venv python3-pip libv4l-dev libasound2-dev
```

## Build

```bash
make          # build binary
make test     # run tests with -race
make clean    # remove binary
```
