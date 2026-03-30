# Protocol Reference

Binary protocol used between the relay and HID boards over UART.

## Transport

| Parameter | Value |
|-----------|-------|
| Interface | UART (TX/RX + GND) |
| Baud rate | 921600 |
| Message size | 32 bytes (fixed) |
| Response size | 2 bytes |

The Go server communicates with the relay board over USB serial using newline-delimited JSON. The relay translates JSON commands into the binary protocol described here and forwards them over UART to the HID board.

## Message Format

```
Byte 0:     command type (uint8)
Byte 1:     sequence number (uint8, 0-255, wraps)
Byte 2:     payload length (uint8, 0-29)
Bytes 3-31: payload (zero-padded to 29 bytes)
```

## Response Format

```
Byte 0: status code
Byte 1: echo of sequence number
```

### Status Codes

| Code | Name | Description |
|------|------|-------------|
| 0x00 | OK | Command executed successfully |
| 0x01 | ERR | Command failed |
| 0x02 | BUSY | Board is busy (not currently used) |

## Commands

### System

| Code | Name | Payload | Wait |
|------|------|---------|------|
| 0x00 | NOOP | (none) | — |
| 0x01 | PING | (none) | yes |

### Keyboard

| Code | Name | Payload | Wait |
|------|------|---------|------|
| 0x10 | KEY_PRESS | keycode (1 byte) | no |
| 0x11 | KEY_RELEASE | keycode (1 byte) | no |
| 0x12 | KEY_TYPE | ASCII text (up to 29 bytes) | yes |

KEY_TYPE sends text through `KeyboardLayoutUS.write()`, handling shift and modifier keys automatically. Strings longer than 29 characters are chunked by the relay with 50ms inter-chunk delay.

### Mouse

| Code | Name | Payload | Wait |
|------|------|---------|------|
| 0x20 | MOUSE_MOVE | x_hi, x_lo, y_hi, y_lo | no |
| 0x21 | MOUSE_CLICK | button_mask (1 byte) | yes |
| 0x22 | MOUSE_PRESS | button_mask (1 byte) | yes |
| 0x23 | MOUSE_RELEASE | button_mask (1 byte) | yes |
| 0x24 | MOUSE_SCROLL | amount (1 byte, signed) | no |

Coordinates are big-endian unsigned 16-bit, range 0-32767. The mouse uses absolute positioning (not relative).

Button mask: `1` = left, `2` = right, `4` = middle (can be OR'd together).

Scroll amount is an unsigned byte interpreted as signed: values > 127 are negative (e.g., 254 = -2). Positive = scroll down, negative = scroll up.

### Touch

| Code | Name | Payload | Wait |
|------|------|---------|------|
| 0x30 | TOUCH | count(1) + contacts | no |

Each contact is 6 bytes: `id(1) + tip(1) + x_hi(1) + x_lo(1) + y_hi(1) + y_lo(1)`.

- `id`: contact identifier (0 or 1)
- `tip`: 1 = finger down, 0 = finger lifted
- `x`, `y`: big-endian 0-32767

Maximum 2 simultaneous contacts. Send an empty contact list (count=0) to lift all fingers.

## Wait vs No-Wait

Commands marked **no** in the Wait column use fire-and-forget mode: the relay sends the binary message but does not wait for the HID board's response. This avoids blocking on high-frequency commands (mouse movement, touch, scroll, key press/release).

Commands marked **yes** block until the 2-byte response is received (or UART timeout of 1 second).

The HID board always sends a response regardless of wait mode. Stale responses from no-wait commands are flushed before the next send.

## JSON Command Format

The relay board accepts newline-delimited JSON on its USB CDC data port. The Go server sends these commands at 115200 baud (though USB CDC ignores baud rate).

```json
{"cmd":"ping"}
{"cmd":"type","text":"hello world"}
{"cmd":"key_press","keycode":4}
{"cmd":"key_release","keycode":4}
{"cmd":"mouse_move","x":16383,"y":16383}
{"cmd":"mouse_click","buttons":1}
{"cmd":"mouse_press","buttons":1}
{"cmd":"mouse_release","buttons":1}
{"cmd":"mouse_scroll","amount":5}
{"cmd":"touch","contacts":[{"id":0,"tip":true,"x":16383,"y":16383}]}
```

## Heartbeat

The relay sends a PING command every 5 seconds when idle (no USB serial data incoming). The HID board's LED blinks on non-high-frequency commands (PING, KEY_TYPE, MOUSE_CLICK, MOUSE_PRESS, MOUSE_RELEASE) to confirm the link is alive.

## Source Files

| File | Description |
|------|-------------|
| `board/shared/protocol.py` | Constants, pack/unpack helpers (shared by both boards) |
| `board/relay/code.py` | JSON parsing, binary translation, UART send |
| `board/hid/code.py` | UART receive, command dispatch, HID execution |
