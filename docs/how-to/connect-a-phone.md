# How to Connect a Phone

A phone needs one more piece than a laptop does: a hub, because a single
USB-C port has to carry video out, the HID board in, and power.

## What you need

- USB-C hub with HDMI out, a USB-A port, and USB-C power delivery
- HDMI-to-USB capture dongle
- The HID board (📤 OUT). The relay (📥 IN) stays on the host.

## Wiring

```
phone ── USB-C hub ─┬─ HDMI ──> capture dongle ──> host USB
                    ├─ USB-A ─> HID board
                    └─ USB-C ─> power
```

Power matters: the phone is acting as a USB host for the HID board, so it is
supplying power rather than taking it.

## iPhone

A mouse only works on iOS through AssistiveTouch, and it is off by default:

```
Settings → Accessibility → Touch → AssistiveTouch → on
```

Turn on **Perform Touch Gestures** there as well, so that a click sends a
touch to whatever is under the pointer instead of only moving it. Without it
the pointer moves but nothing it lands on reacts.

With AssistiveTouch on you get a grey circular pointer that follows the
mouse. Individual buttons can be reassigned under `Devices → <device> →
<button>`.

iOS does not register an external touch digitizer, so touch and multi-touch
go nowhere there — use the mouse. `--platform ios` picks mouse input for you,
and `make test-e2e-iphone` skips the touch checks for the same reason.

## Android

Nothing to turn on. The HID board registers as a touchscreen, so taps,
drags and two-finger gestures work directly, and `--platform android`
defaults to touch input.

## Check it

```bash
make test-e2e-iphone
make test-e2e-android
```

Each opens the test page on the phone using the keyboard, then drives input
through roadie and checks what the page received. See
[Testing](testing.md#testing-a-phone-instead) for what it covers and what to
do if the shortcuts miss.
