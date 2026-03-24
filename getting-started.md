# Getting Started with Roadie

## Prerequisites

- Go 1.21+
- An HDMI-to-USB capture dongle (any cheap UVC-compatible one from Amazon works)
- macOS (v0.1 targets macOS; Linux support comes for free via pion/mediadevices but isn't the priority)

## Setup

```bash
mkdir roadie && cd roadie
git init
go mod init github.com/vibium/roadie
```

Copy `SPEC.md` into the project root.

## Building with Claude Code

```
Read SPEC.md and build this. Start with main.go — flag parsing and startup.
Then capture.go — device detection and the frame loop. Use pion/mediadevices.
Define a FrameSource interface so the HTTP handlers can be tested without real hardware.
Then server.go — all the HTTP handlers.
Then mdns.go — Bonjour registration with grandcat/zeroconf.
Write tests for the HTTP handlers and frame buffer using httptest and a fake FrameSource.
Get it compiling with `make` and tests passing with `make test` first,
we'll test with a real dongle after.
```

## Testing

1. Plug the HDMI-to-USB dongle into your Mac
2. Connect the target machine's HDMI output to the dongle
3. Run `make && ./roadie`
4. Open the URL printed to the console in a browser

Verify:
- `/view` shows live video from the target machine
- `/snapshot` returns a JPEG
- `/health` returns status JSON
