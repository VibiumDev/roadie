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
./roadie --name android --port 8080 --device "Capture A" --relay A1B2C3D4E5F60001 --input touch &
./roadie --name iphone  --port 8081 --device "Capture B" --relay A1B2C3D4E5F60002 --input touch &
```

`--input touch` tells viewers to drive the target by touch rather than mouse,
which is what phones and tablets want. It belongs on the command line because
the right mode depends on the device on the other end of the cable, which the
browser has no way to know — without it, every browser has to pick the mode
itself, and each one starts on mouse.

It takes precedence over the mode a browser has stored for that instance. Only
`?input=` on the URL overrides it.

`--name` sets the Bonjour name, so each instance is reachable independently at
`http://android.local:8080` and `http://iphone.local:8081`, and controls only its
own target. Naming instances after the device they drive keeps them
straightforward to tell apart once there are several.

## Viewing several at once

`/wall` shows the instances side by side, one panel per target:

```
http://android.local:8080/wall?targets=android.local:8080,iphone.local:8081&labels=Android,iPhone
```

Any instance can serve the page — it just embeds each target's `/view`, so the
panels stay fully interactive (touch, keyboard, audio, settings). Video is never
relayed through the wall: each panel streams point-to-point from its own Roadie
to the browser.

Targets launched with `--input` need nothing further here. For one that wasn't,
`&input=touch` sets the mode from the wall — one mode for every panel, or a
positional list (`&input=touch,mouse`) to mix a phone with a laptop. It matters
on the wall specifically: each panel embeds a view with its toolbar hidden, and
the toolbar is where the mouse/touch toggle lives, so a panel with neither
`--input` nor `&input=` falls back to whatever that instance's origin last
stored — defaulting to mouse.

Panels are captioned with each target's hostname unless `&labels=` overrides
them, as above. Add `&minimal=1` for a bare wall with no captions or padding, and
`&cols=` to control the grid. See the [API reference](../../API.md#get-wall) for
all parameters.

The targets must be reachable from the **browser**, not from the server, so use
LAN hostnames or IPs rather than `localhost` when viewing from another machine.

## Automating several targets

Because a single browser page now contains every target, one automation session
driving that page drives all of them — no aggregating server needed. Point
[Vibium](../tutorials/automate-with-vibium.md) at the wall URL instead of
`/view` and the same script reaches any of the devices.

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
- **`POST /api/capture/reset` is scoped** to the dongle that instance is
  capturing from, so it won't disturb the other instance.
- **Give each instance its own `--name`.** Both would otherwise advertise
  `roadie.local` over mDNS and collide. The name is lowercased to form the
  hostname, so `--name Android` is reachable at `android.local`.
- **Flashing boards is still one at a time.** Both relay boards mount their
  CircuitPython drive as `ROADIE_RLY`, so `make sync-relay` can't tell them
  apart. Flash and sync with one relay board plugged in at a time. (They run
  identical firmware, so there's nothing per-board to configure.)

## See also

- [Architecture](../explanation/architecture.md#multiple-targets) — why targets stay in separate processes
- [Board Reference](../reference/boards.md#serial-numbers) — how board serial numbers work
- [Troubleshooting](troubleshooting.md#relay-detection) — relay selector errors
