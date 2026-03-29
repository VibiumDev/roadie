# Gesture Commands as First-Class Protocol

## Context

Currently the protocol only has primitive HID commands (single touch report, mouse move, key press). Gestures like swipe and pinch require timed sequences of touch events. Today these only exist as ad-hoc REPL helpers in the tutorial. They should be proper protocol commands so the Go server can send `{"cmd": "gesture_swipe", ...}` and have it Just Work.

## Approach: Gesture loops run on the HID board (Option C)

The HID board receives a single gesture command and executes the full sequence of touch reports locally. This is the only viable option because the HID board has a **100ms LED flash** (`time.sleep(0.1)`) after every command. Running the loop externally (server or relay) would hit this 100ms bottleneck per step, making gestures take seconds instead of milliseconds.

With the loop on the HID board, it can skip the LED flash for intermediate touch reports and only flash once when the gesture completes.

### Why not the other options?

- **Go server loop**: Each step has USB serial + UART latency, plus the 100ms LED flash on HID board per step.
- **Relay board loop**: Still hits the 100ms LED flash on HID board per step. Fast UART doesn't help.
- **HID board loop**: Tightest timing, no serial overhead per step, can skip LED flash during gesture.

## New Protocol Commands

Three new commands in the `0x3x` touch range:

| Command | Byte | Payload |
|---------|------|---------|
| `CMD_GESTURE_TAP` | `0x31` | x(2) + y(2) + count(1) = **5 bytes** |
| `CMD_GESTURE_SWIPE` | `0x32` | start_x(2) + start_y(2) + end_x(2) + end_y(2) + steps(1) + delay_ms(1) = **10 bytes** |
| `CMD_GESTURE_PINCH` | `0x33` | center_x(2) + center_y(2) + start_dist(2) + end_dist(2) + steps(1) + delay_ms(1) = **10 bytes** |

All fit well within the 29-byte payload limit. Coordinates are big-endian 0-32767. Pinch fingers move along the horizontal axis (no angle parameter for v1).

## JSON API

```
POST /api/hid/gesture/tap     {"x": 16383, "y": 16383, "count": 1}
POST /api/hid/gesture/swipe   {"start_x": 8383, "start_y": 16383, "end_x": 24383, "end_y": 16383, "steps": 20, "delay_ms": 30}
POST /api/hid/gesture/pinch   {"center_x": 16383, "center_y": 16383, "start_dist": 8000, "end_dist": 1000, "steps": 20, "delay_ms": 30}
```

WebSocket: same fields with `"cmd": "gesture_tap"` / `"gesture_swipe"` / `"gesture_pinch"`.

Defaults (applied by Go server): `steps=20`, `delay_ms=30`, `count=1`.

## LED Flash Handling

Gesture commands on the HID board skip the normal post-command LED flash. Instead, the gesture handler flashes once at the end. Implementation: check if cmd is a gesture type in the main loop and skip the LED block.

```python
# in main loop, after handle_command:
is_gesture = cmd in (CMD_GESTURE_TAP, CMD_GESTURE_SWIPE, CMD_GESTURE_PINCH)
if not is_gesture:
    neopixel_write.neopixel_write(pixel_pin, OFF)
    time.sleep(0.1)
    neopixel_write.neopixel_write(pixel_pin, GREEN)
```

Gesture handlers flash the LED themselves when done.

## Gesture Logic (HID board)

**Tap**: finger down, sleep 30ms, finger up. If count=2, sleep 50ms, repeat.

**Swipe**: finger down at start, interpolate position over `steps` with `delay_ms` between each, finger up at end.

**Pinch**: two fingers start at `center +/- start_dist/2` along X axis, interpolate to `center +/- end_dist/2` over `steps`, then lift both. Coordinates clamped to 0-32767.

## Relay Board

Gesture commands use the existing `"nowait"` pattern (same as touch/mouse_move) since gestures take hundreds of ms to execute and we don't want the relay blocking on UART response.

## Implementation Order

1. **`board/shared/protocol.py`** -- new CMD constants + `pack_gesture_tap`, `pack_gesture_swipe`, `pack_gesture_pinch`
2. **`board/hid/code.py`** -- gesture execution functions + main loop LED skip + import new constants
3. **`board/relay/code.py`** -- three new `elif` branches in `translate()` with `"nowait"`
4. **`hid.go`** -- `Tap()`, `Swipe()`, `Pinch()` methods
5. **`server.go`** -- REST endpoints + WebSocket cases
6. **`API.md`** -- document new endpoints

## Files to Modify

- `board/shared/protocol.py`
- `board/hid/code.py`
- `board/relay/code.py`
- `hid.go`
- `server.go`
- `API.md`

## Verification

1. Flash updated code to both boards via `make sync`
2. Test from REPL: on relay board, send gesture JSON manually and confirm HID board executes
3. Test from server: `curl -X POST localhost:8080/api/hid/gesture/swipe -d '{"start_x":8383,"start_y":16383,"end_x":24383,"end_y":16383}'`
4. Test tap, swipe, and pinch on a target device with a touchscreen (not the Pi with the udev rule -- multi-touch needs `hid-multitouch` driver)
5. Verify existing commands still work (keyboard, mouse, single touch)

## Status

**Blocked**: Need to fix basic touch/mouse/keyboard commands first before adding gestures.
