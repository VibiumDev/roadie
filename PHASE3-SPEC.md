# Roadie Phase 3: Board Communication Protocol

## Overview

Phase 3 builds on the provisioned boards from Phase 2, adding the command protocol that enables the full pipeline:

```
Go server → HTTP API → USB serial → Relay board → UART → HID board → USB HID → target device
```

During development, both boards are plugged into the same Raspberry Pi, creating a testable loop:

```
Pi sends serial command → Relay → UART → HID → USB HID keystroke → Pi receives keystroke
```

---

## 3.1 Protocol (Phase A — ✅ Done)

### Wire format: 32-byte binary messages over UART

```
Byte 0:     command type (uint8)
Byte 1:     sequence number (uint8)
Byte 2:     payload length (uint8, 0-29)
Bytes 3-31: payload (29 bytes max, zero-padded)
```

### Response: 2 bytes
```
Byte 0: status code (0x00=OK, 0x01=ERR, 0x02=BUSY)
Byte 1: echo of sequence number
```

### Command types

| Code | Name | Payload |
|------|------|---------|
| 0x00 | NOOP | (none) |
| 0x01 | PING | (none) |
| 0x10 | KEY_PRESS | keycode (1 byte) |
| 0x11 | KEY_RELEASE | keycode (1 byte) |
| 0x12 | KEY_TYPE | ASCII text (up to 29 bytes) |
| 0x20 | MOUSE_MOVE | x_hi, x_lo, y_hi, y_lo (absolute, 0-32767) |
| 0x21 | MOUSE_CLICK | button_mask (1 byte) |
| 0x22 | MOUSE_PRESS | button_mask (1 byte) |
| 0x23 | MOUSE_RELEASE | button_mask (1 byte) |

### Shared code: `board/shared/protocol.py`

Contains all constants, `pack_msg()`, `unpack_msg()`, `pack_resp()`. Copied to `lib/` on both boards by the install script.

### Ping/pong verification

Both boards blink their NeoPixel on each successful ping/pong cycle (1 per second). Relay prints `pong seq=N ok`, HID prints `ping seq=N`.

---

## 3.2 Serial Command Interface (Phase B)

### Host-to-relay: newline-delimited JSON over `usb_cdc` data port

The relay board exposes two USB serial ports:
- **REPL** (console): for debugging, accessible via `screen`
- **Data** (`usb_cdc.data`): for commands from the host

The host sends one JSON command per line:
```json
{"cmd":"ping"}
{"cmd":"type","text":"ok"}
{"cmd":"key_press","keycode":4}
{"cmd":"key_release","keycode":4}
{"cmd":"mouse_move","x":16383,"y":16383}
{"cmd":"mouse_click","buttons":1}
```

### Relay board changes (`board/relay/code.py`)

- Read from `usb_cdc.data` in the main loop
- Line-buffer incoming bytes, parse on `\n`
- `json.loads()` the line, translate to binary message via `pack_msg()`
- Send over UART, read 2-byte response
- Keep periodic ping as optional heartbeat (can be disabled)

### HID board changes (`board/hid/code.py`)

- Import `Keyboard`, `KeyboardLayoutUS` from `adafruit_hid`
- Import `AbsoluteMouse` from `absolute_mouse`
- `handle_command()` dispatcher for each command type:
  - `KEY_TYPE`: `KeyboardLayoutUS(kbd).write(text)` for ASCII strings
  - `KEY_PRESS`/`KEY_RELEASE`: `kbd.press(keycode)` / `kbd.release(keycode)`
  - `MOUSE_MOVE`: `mouse.move(x, y)` absolute positioning
  - `MOUSE_CLICK`/`PRESS`/`RELEASE`: `mouse.click(button)` etc.
- Return `STATUS_OK` or `STATUS_ERR` in response

### Long string chunking

Strings longer than 29 characters must be chunked on the relay side into multiple `KEY_TYPE` commands with a small delay (50ms) between chunks.

---

## 3.3 Circular Test (Phase C)

### `board/test_circular.py` — runs on the Pi, not on a board

1. Opens the relay board's `usb_cdc` data serial port
2. Sends `{"cmd":"type","text":"ok"}\n`
3. Verifies "ok" was typed by the HID board

### Serial port disambiguation

With `usb_cdc` enabled, the relay exposes 2 serial ports. The HID board exposes 1. Total: 3 `/dev/ttyACM*` devices. Detection options:
- Probe each port for the "relay" startup banner on the REPL port
- Use `/dev/serial/by-id/` symlinks (USB serial number)
- Hardcode for now, improve later

### Verification methods

- **Simple**: send `{"cmd":"type","text":"ok"}`, visually confirm "ok" appears in another terminal
- **Automated**: use `evdev` to listen for HID keyboard events from the CircuitPython device

### Makefile target
```makefile
test-circular:
	python3 board/test_circular.py
```

---

## 3.4 Files to Modify

| File | Phase | Change |
|------|-------|--------|
| `board/shared/protocol.py` | A ✅ | Protocol constants, pack/unpack helpers |
| `board/relay/code.py` | A ✅, B | UART ping + serial JSON listener |
| `board/hid/code.py` | A ✅, B | UART receiver + HID command execution |
| `board/test_circular.py` | C | New file — circular test script |
| `Makefile` | C | Add test-circular target |

No requirements.txt changes needed — `busio`, `usb_cdc`, `json`, `sys` are CircuitPython built-ins.

---

## What NOT to implement in Phase 3

- Go server HTTP API or serial communication code (Phase 4)
- Go-side serial port detection or command serialization
- The 3D-printed enclosure
- Production error handling / retry logic beyond basic STATUS_ERR
