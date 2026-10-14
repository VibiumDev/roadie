# How to Test the Boards

Three commands. Each checks input that was *actually received*, not just that
the boards accepted a command.

| Command | Checks |
|---------|--------|
| `make test-hid` | keyboard, mouse, click, scroll, touch — read back from `/dev/input` |
| `make test-e2e-pi` | the same, driven through roadie's HTTP API into a real browser |
| `make test-audio` | voice input: a tone through the relay, out the HID board's USB mic |

Run `make setup` once first. The HID board must be plugged into this machine,
so the input it injects comes back here.

None of them need a monitor: `make test-hid` uses no display at all, and
`make test-e2e-pi` creates its own.

## Watching a run

Set a password once per machine:

```bash
make set-vnc-password
```

Then:

```bash
make test-e2e-pi-vnc
```

It prints an address and waits for you to connect before running anything:

```
📺 Watch the test at:  10.1.10.105:5934
```

| Your machine | Connect with |
|--------------|--------------|
| macOS | `open vnc://10.1.10.105:5934` |
| Linux | `remmina`, or `xtightvncviewer 10.1.10.105:5934` |
| Windows | RealVNC Viewer or TigerVNC |

The port is always 5934, so save the address and password in your viewer once.
The connection is view-only — a stray click would steal focus and break the
run.

Worth watching: the page draws what it receives, so you can see *why* a check
failed rather than just that it did.

## Testing a phone instead

Move the HID board to the phone, leave the relay here, and the test page runs
on the phone:

```bash
make test-e2e-android    # opens the page over ADB
make test-e2e-iphone     # prints a URL to open in Safari
make test-e2e-pc         # prints a URL for another computer
```

Android needs ADB over WiFi, because the phone's USB port is busy hosting the
HID board:

```bash
adb tcpip 5555           # once, with the phone on USB
adb connect <phone-ip>:5555
```

## Useful flags

```bash
make test-hid-quick                      # skip the slower checks
python3 board/test_hid.py --dump         # print every input event
python3 board/test_hid.py --skip touch   # skip a check
python3 board/test_e2e.py --target pi --keep-open   # leave the browser up
ROADIE_E2E_RESOLUTION=1920x1080 make test-e2e-pi    # bigger virtual screen
```

## When something fails

| Symptom | Fix |
|---------|-----|
| `Relay data port not found` | relay unplugged, or needs a re-plug after flashing |
| `Could not open any HID input device` | run `make setup`, then log out and back in |
| multi-touch reported as skipped | expected: roadie's udev rule makes this host single-touch |
| every check fails | check the TX/RX/GND jumpers between the boards |
| `make test-audio` finds no signal | the HID board needs a physical re-plug after flashing |
| viewer shows black | the run finished and tore the display down |

For board-level problems see [Troubleshooting](troubleshooting.md), and
[REPL Tutorial](../tutorials/repl.md) to talk to a board directly.
