# Roadie API Reference

Base URL: `http://<host>:<port>` (default port auto-assigned starting at 8080)

Service discovery: `dns-sd -B _roadie._tcp` (mDNS/Bonjour)

---

## Pages

### `GET /`
Index page with links to all endpoints.

### `GET /view`
Live video feed with audio toggle and quality slider.

### `GET /settings`
Device info and JPEG quality adjustment UI.

### `GET /test`
Interactive HID test page with mouse/touch trackpad, keyboard input, and key combo controls. Supports Mouse mode (pointer + scroll wheel) and Touch mode (multi-touch digitizer with pinch-to-zoom). Trackpad aspect ratio auto-adjusts to match the target's video signal. Communicates with the target via WebSocket (`/api/hid/ws`).

---

## Video

### `GET /stream`
MJPEG stream (auto-cropped to detected content area).

**Response:** `multipart/x-mixed-replace` with `image/jpeg` frames.

### `GET /snapshot`
Single JPEG frame (auto-cropped).

**Response:** `image/jpeg`

### `GET /raw-stream`
MJPEG stream (uncropped, full capture resolution).

**Response:** `multipart/x-mixed-replace` with `image/jpeg` frames.

### `GET /raw-snapshot`
Single JPEG frame (uncropped).

**Response:** `image/jpeg`

---

## Audio

### `GET /audio`
WebSocket endpoint for live PCM audio.

**Protocol:**
1. Server sends audio parameters as the first text message:
   ```json
   {"sampleRate": 48000, "channels": 2, "format": "f32-planar"}
   ```
2. Server streams PCM audio as binary messages (little-endian float32).

Returns `503` if no audio device is active.

---

## Settings

### `GET /api/settings`
Get current settings.

**Response:**
```json
{"quality": 80}
```

### `PUT /api/settings`
Update settings.

**Request:**
```json
{"quality": 60}
```
Quality is clamped to 30-95.

**Response:**
```json
{"quality": 60}
```

---

## Health

### `GET /health`
Service status and device info.

**Response:**
```json
{
  "status": "ok",
  "source_type": "hardware",
  "device": "USB Video",
  "resolution": "1920x1080",
  "fps": 30,
  "quality": 80,
  "crop": {"x": 0, "y": 140, "width": 1920, "height": 800},
  "audio": {"sampleRate": 48000, "channels": 2}
}
```

| Field | Description |
|-------|-------------|
| `status` | `ok`, `disconnected`, `connecting`, or `no_signal` |
| `source_type` | `hardware` or `http` |
| `crop` | Present only when active crop differs from full frame |
| `audio` | Present only when audio capture is active |

---

## HID Control

All HID endpoints require the relay board to be connected via USB serial. Returns `503` if HID is not available.

### `GET /api/hid/status`
Connection status of the relay board.

**Response:**
```json
{"status": "connected"}
```

Status values: `connected`, `disconnected`, `connecting`, `unavailable`.

### `POST /api/hid/type`
Type text on the target device. Text longer than 29 characters is automatically chunked.

**Request:**
```json
{"text": "hello world"}
```

**Response:**
```json
{"status": "ok"}
```

### `POST /api/hid/key`
Send a keyboard key event.

**Request:**
```json
{"keycode": 4, "action": "press"}
```

| Field | Description |
|-------|-------------|
| `keycode` | USB HID keycode (see table below) |
| `action` | `press`, `release`, or `click` (press + release) |

**Response:**
```json
{"status": "ok"}
```

### `POST /api/hid/mouse/move`
Move mouse to absolute position.

**Request:**
```json
{"x": 16383, "y": 16383}
```

Coordinates are absolute in the range 0-32767:
- `(0, 0)` = top-left
- `(16383, 16383)` = center
- `(32767, 32767)` = bottom-right

**Response:**
```json
{"status": "ok"}
```

### `POST /api/hid/mouse/click`
Mouse button click, press, or release.

**Request:**
```json
{"buttons": 1, "action": "click"}
```

| Field | Description |
|-------|-------------|
| `buttons` | Button mask: `1` = left, `2` = right, `4` = middle. Default: `1` |
| `action` | `click` (default), `press`, or `release` |

**Response:**
```json
{"status": "ok"}
```

### `POST /api/hid/mouse/scroll`
Scroll the mouse wheel.

**Request:**
```json
{"amount": 5}
```

| Field | Description |
|-------|-------------|
| `amount` | Scroll amount: positive = down, negative = up. Range: -127 to 127 |

**Response:**
```json
{"status": "ok"}
```

### `POST /api/hid/touch`
Send a multi-touch digitizer report (up to 2 simultaneous contacts). The HID board presents as a touchscreen to the target device, enabling native touch gestures like tap, drag, scroll, and pinch-to-zoom.

**Request:**
```json
{"contacts": [
  {"id": 0, "tip": true, "x": 16383, "y": 16383},
  {"id": 1, "tip": true, "x": 20000, "y": 20000}
]}
```

| Field | Description |
|-------|-------------|
| `contacts` | Array of 0-2 touch contacts |
| `contacts[].id` | Contact identifier: `0` or `1` |
| `contacts[].tip` | `true` = finger down, `false` = finger lifted |
| `contacts[].x` | Absolute X position (0-32767) |
| `contacts[].y` | Absolute Y position (0-32767) |

Send an empty contacts array `[]` to lift all fingers.

**Response:**
```json
{"status": "ok"}
```

**Gesture examples:**

| Gesture | Sequence |
|---------|----------|
| Tap | Send contact tip=true, then tip=false |
| Drag | Send contact tip=true, update x/y over time, then tip=false |
| Scroll | Two contacts (id 0+1), move both in same vertical direction |
| Pinch zoom | Two contacts, move apart (zoom in) or together (zoom out) |

### `WS /api/hid/ws`
WebSocket for real-time HID control. Accepts the same JSON command format used by the relay board protocol. Preferred for mouse movement and touch (lower latency than REST).

**Messages (client to server):**
```json
{"cmd": "mouse_move", "x": 16383, "y": 16383}
{"cmd": "mouse_click", "buttons": 1}
{"cmd": "mouse_press", "buttons": 1}
{"cmd": "mouse_release", "buttons": 1}
{"cmd": "mouse_scroll", "amount": 5}
{"cmd": "key_press", "keycode": 4}
{"cmd": "key_release", "keycode": 4}
{"cmd": "type", "text": "hello"}
{"cmd": "touch", "contacts": [{"id": 0, "tip": true, "x": 16383, "y": 16383}]}
```

No server-to-client messages. Connection auto-reconnects on the `/test` page.

---

## USB HID Keycodes

Common keycodes for use with `/api/hid/key` and the WebSocket `key_press`/`key_release` commands.

### Letters

| Key | Code | Key | Code | Key | Code |
|-----|------|-----|------|-----|------|
| A | 4 | J | 13 | S | 22 |
| B | 5 | K | 14 | T | 23 |
| C | 6 | L | 15 | U | 24 |
| D | 7 | M | 16 | V | 25 |
| E | 8 | N | 17 | W | 26 |
| F | 9 | O | 18 | X | 27 |
| G | 10 | P | 19 | Y | 28 |
| H | 11 | Q | 20 | Z | 29 |
| I | 12 | R | 21 | | |

### Numbers

| Key | Code | Key | Code |
|-----|------|-----|------|
| 1 | 30 | 6 | 35 |
| 2 | 31 | 7 | 36 |
| 3 | 32 | 8 | 37 |
| 4 | 33 | 9 | 38 |
| 5 | 34 | 0 | 39 |

### Special Keys

| Key | Code | Key | Code |
|-----|------|-----|------|
| Enter | 40 | Delete | 76 |
| Escape | 41 | End | 77 |
| Backspace | 42 | Page Down | 78 |
| Tab | 43 | Right Arrow | 79 |
| Space | 44 | Left Arrow | 80 |
| Caps Lock | 57 | Down Arrow | 81 |
| Print Screen | 70 | Up Arrow | 82 |
| Insert | 73 | Num Lock | 83 |
| Home | 74 | | |
| Page Up | 75 | | |

### Function Keys

| Key | Code | Key | Code | Key | Code |
|-----|------|-----|------|-----|------|
| F1 | 58 | F5 | 62 | F9 | 66 |
| F2 | 59 | F6 | 63 | F10 | 67 |
| F3 | 60 | F7 | 64 | F11 | 68 |
| F4 | 61 | F8 | 65 | F12 | 69 |

### Modifiers

| Key | Code | Key | Code |
|-----|------|-----|------|
| Left Ctrl | 224 | Right Ctrl | 228 |
| Left Shift | 225 | Right Shift | 229 |
| Left Alt | 226 | Right Alt | 230 |
| Left GUI (Win/Cmd) | 227 | Right GUI | 231 |

### Key Combos (examples)

Send modifier key presses before the target key, then release in reverse order:

```
Ctrl+C:       press 224, press 6, release 6, release 224
Ctrl+V:       press 224, press 25, release 25, release 224
Alt+Tab:      press 226, press 43, release 43, release 226
Ctrl+Alt+Del: press 224, press 226, press 76, release 76, release 226, release 224
```

---

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--device` | (auto-detect) | Video device name substring |
| `--source` | | HTTP MJPEG source URL (mutually exclusive with --device) |
| `--port` | (auto, from 8080) | HTTP server port |
| `--width` | 1920 | Capture width |
| `--height` | 1080 | Capture height |
| `--fps` | 30 | Capture framerate |
| `--quality` | 80 | JPEG quality (30-95) |
| `--name` | roadie | mDNS service name |
| `--list-devices` | | List video and audio devices, then exit |
