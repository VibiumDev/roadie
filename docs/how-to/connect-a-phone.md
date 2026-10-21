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

Turn on **Perform Touch Gestures** there as well, so that the pointer acts as
a finger: a click taps, a drag swipes, and a held click is a long press.
Without it the pointer moves but nothing it lands on reacts.

iOS does not register an external touchscreen, so with `--platform ios`
roadie delivers touch through the pointer: taps, held touches and one-finger
swipes work as they do on Android, and two-finger gestures can't. The same
flag starts the viewer in mouse mode.

### Recommended settings

For a phone that scripts, agents and people drive through roadie, under
**Settings → Accessibility**:

| Setting | Where | Set to | Why |
|---|---|---|---|
| AssistiveTouch | Touch | On | The only way iOS takes a mouse |
| Perform Touch Gestures | Touch → AssistiveTouch | On | The pointer acts as a finger |
| Dwell Control | Touch → AssistiveTouch | Off | It clicks wherever the pointer rests, and scripts leave it resting |
| Hot Corners | Touch → AssistiveTouch → Dwell Control | None | A pointer parked in a corner shouldn't do anything |
| Mouse Keys | Touch → AssistiveTouch | Off | It turns keys into pointer moves, which fights roadie's typing |
| Sound on Click | Touch → AssistiveTouch | Off | roadie records the phone's audio |
| Show Onscreen Keyboard | Touch → AssistiveTouch | Off | roadie is the keyboard; the onscreen one covers half of every screenshot |
| Full Keyboard Access | Keyboards & Typing | Off | It outlines whatever has keyboard focus, and the outline shows in screenshots; roadie's shortcuts (⌘H, ⌘Space) work without it |
| Double-Tap, Long Press | Touch → AssistiveTouch → Custom Actions | None | A stray click on the floating button does nothing |
| Additional buttons | Touch → AssistiveTouch → Devices → Roadie | None | See the right button, below |
| Always Show Menu | Touch → AssistiveTouch | On, the button at an edge, Idle Opacity low | Its menu is the only way to Rotate Screen, open Control Center, or leave the Today view; where it sits it takes clicks, and it shows in screenshots |
| Pointer Size | Pointer Control | Small | The pointer shows in screenshots: Automatically Hide Pointer only hides it 15 s after it stops |
| Reachability | Touch | Off | A swipe along the bottom edge pulls the screen down |
| Auto-Lock | Display & Brightness | Never | Waking a locked phone over USB is untested |

Tracking Sensitivity doesn't matter: roadie places the pointer at exact
positions. Pointer Animations, which would stop the pointer snapping onto
buttons, exists only on iPad.

Don't use the right button. On an iPhone a right-click starts a touch that
never ends: pages stop taking taps until AssistiveTouch is turned off and on.

Safari's microphone prompt ("Would Like to Access the Microphone") sometimes
ignores a click. If it's still up, click again without moving.

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
