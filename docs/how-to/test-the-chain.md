# How to Test the Input Chain

Two kinds of test, split by *what* they prove:

- **`make test-hid`** asks whether the boards inject correct input, by reading
  this machine's own `/dev/input`. No display, no browser, no network.
- **`make test-e2e-<target>`** asks whether that input survives the whole
  stack and arrives in a real browser on a given target device.

| Command | Proves | Needs |
|---------|--------|-------|
| `make test-hid-quick` | ping, keyboard, absolute mouse | both boards, HID board plugged into this host |
| `make test-hid` | the above + click, scroll, touch, multi-touch | both boards, HID board plugged into this host |
| `make test-e2e-pi` | the full stack into a browser on this machine | the above; a display is used if attached, otherwise one is created |
| `make test-e2e-pi-vnc` | the same, but watchable over VNC while it runs | the above |
| `make test-e2e-android` | the full stack into Chrome on an Android phone | HID board plugged into the phone, ADB over WiFi |
| `make test-e2e-iphone` | the full stack into Safari on an iPhone | HID board plugged into the iPhone |
| `make test-e2e-pc` | the full stack into a browser on another computer | HID board plugged into that computer |

Every one of them verifies against input that was **actually received**,
rather than trusting the boards' serial output. A test that only reads the
REPL can report success while nothing was injected at all.

## Prerequisites

The HID board must be plugged into the machine running the test, so the input
it injects comes back to the same host. Your user also needs to be in the
`input` group to read `/dev/input/*`:

```bash
sudo usermod -aG input "$USER"   # log out and back in afterwards
```

`make setup` covers the Python side.

## make test-hid

The board-level test. Pings the relay, types a string, moves the absolute
mouse to both corners and the middle, clicks, scrolls, and touches — checking
each against the decoded input events the kernel actually delivered.

```bash
make test-hid                                # everything
make test-hid-quick                          # ping + keyboard + mouse only
python3 board/test_hid.py --dump             # print every captured event
python3 board/test_hid.py --skip touch       # skip a leg (repeatable)
python3 board/test_hid.py --text "custom"
```

This needs **no display at all** — it reads `/dev/input` directly, so it works
over ssh on a headless Pi.

A healthy relay is **silent** on a successful ping; it only blinks its
NeoPixel. The test looks for error output rather than a confirmation string
that is never printed.

### Why this doesn't type into your terminal

The HID board is a real keyboard to this host, so anything it types would
otherwise land in whichever window has focus. Both board-level tests take
exclusive ownership of the input devices (`EVIOCGRAB`), so the injected input
is consumed by the test and never reaches your desktop. Pass `--no-grab` to
disable that.

## make test-e2e-pi

The full stack. Drives input through roadie's HTTP API and verifies it arrives
in a real browser:

```
test_e2e.py → roadie HTTP API → relay → UART → HID board
            → USB HID → kernel → display server → browser
            → test page → POST back → assertions
```

```bash
make test-e2e-pi
python3 board/test_e2e.py --target pi --text "custom text"
python3 board/test_e2e.py --target pi --keep-open   # leave the browser up
python3 board/test_e2e.py --target pi --skip touch  # skip a leg (repeatable)
```

It starts roadie itself if nothing is already serving, opens the desktop's
default browser in kiosk mode, waits for the page to report in, then drives
each input type and checks what the page received. Coordinates are compared in
screen pixels, so the 0–32767 HID range is validated against the real display
geometry.

The test page lives in `board/e2e/page.html` and draws what it receives — a
cursor ring, a touch blob, typed text, and a running event log — so you can
also just watch it work.

### Why it clicks before typing

Keyboard input goes to whatever owns focus. The test moves the mouse to the
centre and clicks once to give the page focus before typing, otherwise the
keyboard leg would fail for reasons that have nothing to do with the boards.

## Dev machines with different displays attached

Roadie dev Pis differ — one has the official 7" touchscreen, the next has no
display at all — so `make test-e2e-pi` adapts instead of making you choose:

- a reachable display (`$DISPLAY`, or `:0`) is used if there is one
- otherwise a throwaway Xorg on the dummy video driver is started, used, and
  torn down

The same command therefore works on every dev machine. `make setup` installs
the dependencies for the second case; if you set a machine up by hand:

```bash
sudo apt install xserver-xorg-video-dummy matchbox-window-manager
```

To watch it happen instead of just reading the result, see
[Watch the Tests Run](watch-tests.md):

```bash
make test-e2e-pi-vnc      # serves the display over VNC, waits for your viewer
```

Starting a virtual display needs root, so passwordless `sudo` must be
available; with a display attached, nothing needs root.

Two things about the virtual display are easy to get wrong, and both were
measured rather than assumed:

**Xvfb does not work.** It is the obvious choice and it fails every input
check. Xvfb has no evdev input devices — `xinput list` on an Xvfb display
shows only its own synthetic `Xvfb mouse`/`Xvfb keyboard` and the XTEST
devices — so input injected by the HID board never reaches it. A real Xorg on
the **dummy video driver** loads libinput and sees the actual USB devices:

```
↳ Adafruit Roadie-HID Mouse         id=7   [slave  pointer  (2)]
↳ Adafruit Roadie-HID Touchscreen   id=8   [slave  pointer  (2)]
↳ Adafruit Roadie-HID Keyboard      id=6   [slave  keyboard (3)]
```

**A window manager is required.** Without one, `--kiosk` is never resized to
fill the screen, so the window stays smaller than the display and input aimed
at the far corners lands outside it — the corner check fails while the middle
passes. `matchbox-window-manager` is enough.

Pick the virtual display's resolution with `ROADIE_E2E_RESOLUTION`:

```bash
ROADIE_E2E_RESOLUTION=1920x1080 make test-e2e-pi
```

This matters more than it looks: the test maps HID's 0–32767 range onto the
real screen geometry, so running at a few different resolutions exercises that
mapping against different pixel dimensions.

## Testing against a phone or another computer

The relay board stays plugged into this host. What changes is where the **HID
board** is plugged in, and therefore which device runs the test page.

```bash
make test-e2e-android     # ADB over WiFi opens the page for you
make test-e2e-iphone      # prints a URL to open in Safari
make test-e2e-pc          # prints a URL to open in any browser
```

For Android, ADB has to be over WiFi rather than USB — the phone's port is
busy acting as a USB host for the HID board:

```bash
adb tcpip 5555            # once, with the phone on USB
adb connect <phone-ip>:5555
```

Setup is deliberately done out of band rather than by driving HID to open the
browser. If keyboard input is the thing that is broken, you cannot use
keyboard input to prepare the test meant to detect that.

## Multi-touch and the udev rule

`board/99-roadie-hid.rules` deliberately rebinds the HID board from
`hid-multitouch` to `hid-generic`, so the mouse and touchscreen appear as
separate input devices. The trade-off is that the host then sees touch as a
**single contact**.

Both tests detect this and report the multi-touch leg as skipped rather than
failed — it is a configuration choice, not a defect. The firmware itself is
fine: with the rule removed, the kernel exposes full multi-touch protocol B
(`ABS_MT_SLOT`, `ABS_MT_POSITION_X/Y`, `ABS_MT_TRACKING_ID`) and two
simultaneous contacts arrive at their exact coordinates.

This rule only exists on a Linux host running roadie's `make setup`. A target
device such as a phone has no such rule, so it is unaffected.

## When something fails

| Symptom | Likely cause |
|---------|--------------|
| `Relay data port not found` | relay board unplugged, or `boot.py` never ran — unplug and re-plug it |
| `Could not open any HID input device` | user not in the `input` group, or the HID board is plugged into a different machine |
| every input check fails on a virtual display | Xvfb was used instead of Xorg with the dummy driver — Xvfb receives no real input |
| corner checks fail, middle ones pass | no window manager on the display, so the browser window is smaller than the screen |
| `No ADB device` | connect over WiFi first; USB ADB can't be used while the phone hosts the HID board |
| keyboard leg fails, others pass | focus went elsewhere; for `test-e2e` check the display is awake and unlocked |
| every leg fails | check the TX/RX/GND jumpers between the boards |
| `The page never loaded` | the browser could not reach the harness |
| `Starting a virtual display needs root` | no display attached and no passwordless sudo — run `sudo -v` first |

See [Troubleshooting](troubleshooting.md) for board-level problems and
[REPL Tutorial](../tutorials/repl.md) for talking to a board directly.
