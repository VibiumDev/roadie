# Running Multiple Targets

Drive several devices from one host by running one `roadie` process per target.
Each process needs its own capture dongle and its own relay board, plus a
distinct HTTP port and Bonjour name.

## Pin each instance to its hardware

Start by listing what's attached:

```bash
./roadie --list-devices
```

```
Video devices:
  - HDMI Capture A: HDMI Capture A;usb-0000:01:00.0-1
  - HDMI Capture B: HDMI Capture B;usb-0000:01:00.0-1.2

Relay boards:
  - A1B2C3D4E5F60001 (/dev/serial/by-id/usb-Adafruit_Roadie-Relay_A1B2C3D4E5F60001-if02)
  - A1B2C3D4E5F60002 (/dev/serial/by-id/usb-Adafruit_Roadie-Relay_A1B2C3D4E5F60002-if02)
```

Then give each instance one dongle and one board. `--device` matches a substring
of the video device name; `--relay` matches a substring of the relay board's USB
serial number (or of its port path):

```bash
./roadie --name roadie-a --port 8080 --device "Capture A" --relay A1B2C3D4E5F60001 &
./roadie --name roadie-b --port 8081 --device "Capture B" --relay A1B2C3D4E5F60002 &
```

Each instance is then reachable independently — `http://roadie-a.local:8080` and
`http://roadie-b.local:8081` — and controls only its own target.

## Notes and caveats

- **Relay boards need no reflashing.** Every board reports a unique USB serial
  number, which is what `--relay` selects on. Serials from the same batch share a
  long prefix, so use enough characters to be unambiguous — Roadie refuses an
  ambiguous selector rather than guessing.
- **`--relay` is required once two boards are attached.** With several connected
  and no selector, picking one arbitrarily would let two instances write to the
  same board, so Roadie reports the ambiguity and waits instead.
- **`--device` is strongly recommended.** Auto-detect picks the first external
  capture device it finds, which is not stable across reboots. Roadie warns at
  startup when several dongles are attached and no `--device` was given.
- **Give each instance its own `--name`.** Both would otherwise advertise
  `roadie.local` over mDNS and collide.
- **Flashing boards is still one at a time.** Both relay boards mount their
  CircuitPython drive as `ROADIE_RLY`, so `make sync-relay` can't tell them
  apart. Flash and sync with one relay board plugged in at a time. (They run
  identical firmware, so there's nothing per-board to configure.)

## See also

- [Architecture](../explanation/architecture.md#multiple-targets) — why targets stay in separate processes
- [Board Reference](../reference/boards.md#serial-numbers) — how board serial numbers work
- [Troubleshooting](troubleshooting.md#relay-detection) — relay selector errors
