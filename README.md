# 🚐 Roadie

USB KVM (⌨️Keyboard, 🖥️Video, 🖱️Mouse / 👆🏽Multi-touch) controllable over HTTP.

<!-- TODO: hero image or gif showing /view controlling a phone -->

Roadie turns a cheap HDMI capture dongle and a pair of microcontroller boards into a browser-based KVM that works with anything that has a screen and a USB port: laptops, desktops, phones, tablets, servers, embedded devices. View the target's display in a browser, and send keyboard, mouse, and multi-touch input back to it — no software required on the target.

## Use Cases

- **Device provisioning**: automate OS setup assistants with an AI agent before SSH or VNC exist on the target
- **Mobile device testing**: drive phones and tablets from a browser without touching them
- **Remote tech support**: view and control someone's device from another room or across the network 
- **General IP-KVM**: same idea as PiKVM or TinyPilot, but with multi-touch support and a ~$86 BOM

## How It Works

Roadie uses one device to set up or control another. It grabs video from an HDMI-to-USB capture dongle and serves it over HTTP, turning a remote device's physical display into a web page. A pair of microcontroller boards act as a USB KVM, sending keyboard, mouse, and multi-touch input to the target device. An AI agent (or a human) can view the screen in a browser, grab frames for vision analysis, and send input back — all over HTTP.

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

### Why not VNC/KVM/MDM?

**VNC and remote desktop** require software running on the target. If the target is mid-setup, locked out, or doesn't have an OS yet, they can't help.

**Hardware KVM switches** (PiKVM, JetKVM, NanoKVM, TinyPilot) work at the hardware level, but they only emulate a keyboard and absolute mouse — none of them implement a USB multi-touch digitizer. That means they can't drive phones or tablets, which expect touch input. Roadie emulates a multi-touch digitizer alongside keyboard and mouse, so it works with mobile devices too.

**MDM** tools manage devices over the network but require enrollment, platform-specific agents, and an already-configured OS. Roadie works at the hardware level — it doesn't care what OS is running or whether the device is enrolled in anything.

## Parts Needed (~$86)

| Qty | Part | Price | Link |
|-----|------|------:|------|
| 2x | Adafruit QT Py RP2040 | $9.95 ea | [adafruit.com](https://www.adafruit.com/product/4900) |
| 1x | Tiny premium breadboard | $3.95 | [adafruit.com](https://www.adafruit.com/product/65) |
| 1x | Breadboarding wire bundle | $4.95 | [adafruit.com](https://www.adafruit.com/product/153) |
| 1x | USB adapter kit (A↔C) | $6.99 | [amazon.com](https://www.amazon.com/AreMe-Adapter-Female-Converter-Connector/dp/B0BYMRHR86) |
| 1x | HDMI-to-USB-C 1080p capture | $16.99 | [amazon.com](https://www.amazon.com/dp/B091NX27S8) |
| 1x | HDMI male-to-male adapter | $5.99 | [amazon.com](https://www.amazon.com/dp/B09JSFVFF1) |
| 1x | USB-C to HDMI + USB-A + USB-C PD hub | $17.99 | [amazon.com](https://www.amazon.com/dp/B08TWKNV13) |
| 1x | USB-A to USB-C cable | $8.99 | [amazon.com](https://www.amazon.com/dp/B0C3LFTY71) |

You also need a host computer to run the `roadie` server — a Raspberry Pi 4 (2GB+), Mac, or Linux PC.

## Quick Start

**Prerequisites:** macOS or Linux, Go 1.21+, Python 3.10+

```bash
git clone https://github.com/VibiumDev/roadie.git && cd roadie
make setup          # one-time: python venv, dependencies, udev rules
make flash-hid      # flash first board, then unplug it
make flash-relay    # flash second board
```

Connect the two boards with jumper wires (TX-to-RX, RX-to-TX, GND-to-GND), plug the relay board into your host, the HID board into the target, and the HDMI capture dongle between the target's video output and the host. Then:

```bash
make                # build
./roadie            # run
```

Open `http://localhost:8080/view` to see and control the target's screen.

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

## Documentation

| Doc | Description |
|-----|-------------|
| [API Reference](API.md) | HTTP/WebSocket endpoints, HID commands, USB keycodes |
| [Protocol Reference](docs/reference/protocol.md) | Binary wire format between relay and HID boards |
| [Board Reference](docs/reference/boards.md) | Hardware specs, wiring, USB identifiers, LED behavior |
| [Architecture](docs/explanation/architecture.md) | System design, pipeline, and design decisions |
| [Flash Boards](docs/how-to/flash-boards.md) | Flashing, re-flashing, and syncing board code |
| [Troubleshooting](docs/how-to/troubleshooting.md) | Common problems and fixes |
| [REPL Tutorial](docs/tutorials/repl.md) | Interactive debugging via CircuitPython REPL |

## Development

```bash
make                  # build binary
make test             # run tests
make test-circular    # end-to-end test (type + mouse) through both boards
make clean            # remove binary
```

See [Flash Boards](docs/how-to/flash-boards.md) for re-flashing and syncing board code.
