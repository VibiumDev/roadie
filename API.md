# Roadie API Reference

Base URL: `http://<host>:<port>` (default port auto-assigned starting at 8080)

Service discovery: `dns-sd -B _roadie._tcp` (mDNS/Bonjour)

---

## Pages

### `GET /`
Index page with links to all endpoints.

### `GET /view`
Live video feed with full HID control. The browser tab is titled
`Roadie — <name>` when `--name` is set, so several targets open at once stay
tellable apart. Click/touch the stream to send mouse or touch events to the target. Keyboard events are captured automatically (except when interacting with the toolbar). Includes audio toggle, microphone toggle (voice input to the target; needs https or localhost), quality/FPS/resolution settings, and mouse/touch input mode toggle. Input mode persists across page refreshes via `localStorage`.

| Param | Description |
|-------|-------------|
| `minimal` | `1` hides the toolbar, for embedding in an iframe (see `/grid`). |
| `input` | `mouse` or `touch` sets the pointer mode for this page load, overriding the server's `--input`. |

Pointer mode resolves in this order: `?input=` for this page load, then the
server's `--input`, then the default implied by `--platform`, then a mode this
viewer chose from the toolbar, then mouse.
`--input` is the usual place to set it, since the mode is a property of the
target device rather than of the browser viewing it.

Neither `?input=` nor `--input` is written to the browser's stored preference,
so the toolbar toggle still works and still holds for the rest of the session —
a reload returns to the configured mode.

### `GET /grid`
Grid of several Roadie instances, one iframe per target. Carries no video itself —
each panel loads its stream straight from its own Roadie, so frames travel
point-to-point from each capture host to the browser.

| Param | Description |
|-------|-------------|
| `targets` | **Required.** Comma-separated `host:port` (or full `http(s)://host:port`) list. |
| `labels` | Optional captions, comma-separated, positional. Defaults to each hostname. |
| `cols` | Optional grid column count. Defaults to one column per target (a single row). |
| `minimal` | `1` drops captions and padding for a bare grid. |
| `input` | `mouse` or `touch` for every panel, or a positional list (`touch,mouse`). Only needed for targets not already launched with `--input`. |

```
/grid?targets=roadie-a.local:8080,roadie-b.local:8081&labels=Pixel,iPhone
```

Targets must be reachable from the **browser**, not from the server — use LAN
hostnames or IPs rather than `localhost` when viewing from another machine.
Only the scheme and host of each target are used; the iframe path is always
`/view?minimal=1` plus any `input` mode. Returns `400` with usage help if `targets` is missing or invalid.

### `GET /settings`
Device info, capture settings, and the [saved crop](#get-put-post-delete-apicrop).

### `GET /soundcheck`

A microphone check page. Opens the browser's microphone, converts it to
16 kHz mono PCM, and streams it to `/api/hid/audio` — the same path `/view`'s mic
button uses, with a level meter and a status line.

Served by roadie so that it is same-origin with `/api/hid/audio`, which refuses
connections from other origins. `make test-audio-browser` drives this page;
it is also useful on its own for checking a microphone by hand.

Optional `?report=<url>` makes the page GET that URL once it is streaming,
which is how the test knows audio is actually flowing before it listens.

### `GET /test`
Interactive HID test page with mouse/touch trackpad, keyboard input, and key combo controls. Supports Mouse mode (pointer + scroll wheel) and Touch mode (multi-touch digitizer with pinch-to-zoom). Trackpad overlays the auto-cropped MJPEG stream and auto-adjusts aspect ratio to match the target's video signal. Coordinates are remapped to account for crop offset so touches align with visible content. Input mode persists via `localStorage` (shared with `/view`). Communicates with the target via WebSocket (`/api/hid/ws`).

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

Audio *from* the target. Voice input *into* the target is
[`WS /api/hid/audio`](#ws-apihidaudio), with the other endpoints that drive
the target.

### `GET /audio`
WebSocket endpoint for live PCM audio.

**Protocol:**
1. Server sends audio parameters as the first text message:
   ```json
   {"sampleRate": 48000, "channels": 2, "format": "f32-interleaved"}
   ```
2. Server streams PCM audio as binary messages: little-endian float32,
   channels interleaved (left, right, left, right, ...).

Returns `503` if no audio device is active.

The audio is the capture dongle's (the target's HDMI audio). With
`--hid-backend linux-gadget` and the gadget's speaker enabled, it follows the
phone: the USB speaker whenever the phone plays to it, HDMI otherwise. If a
switch changes the format, the server closes the socket so clients reconnect
and read the new parameters.

## Settings

### `GET /api/settings`
Get current settings.

**Response:**
```json
{"quality": 80, "fps": 30, "width": 1920, "height": 1080, "autocrop": true, "glide": false}
```

### `PUT /api/settings`
Update settings. All fields are optional — only provided fields are changed. Changing `fps`, `width`, or `height` triggers a capture restart.

**Request:**
```json
{"quality": 60, "fps": 15, "width": 1280, "height": 720}
```

| Field | Range | Description |
|-------|-------|-------------|
| `quality` | 30-95 | JPEG compression quality |
| `fps` | 10-30 | Capture framerate |
| `width` | | Capture width (must be set with `height`) |
| `height` | | Capture height (must be set with `width`) |
| `autocrop` | `true`/`false` | Choose auto-crop (trim constant black bars) or no crop: the two-way form of [`/api/crop`](#get-put-post-delete-apicrop)'s choice, which also offers a saved crop. Kept across restarts. `GET` answers `true` only while auto-crop is chosen |
| `glide` | `true`/`false` | Glide the pointer to each new spot instead of jumping (see [`/api/hid/mouse/move`](#post-apihidmousemove)). Off unless `--glide` was given; a change lasts until restart. `409` from an input that can't |

**Response:**
```json
{"quality": 60, "fps": 15, "width": 1280, "height": 720}
```

### `GET`, `PUT`, `POST`, `DELETE /api/crop`
The crop: **auto**, **off**, or **saved**. Auto-crop trims whatever is black
at the frame's edges, so a dark app on the target shrinks the picture, and
with it the screenshot's size and where taps land. A crop saved while the
target shows something bright to its edges, such as
`https://soundcheck.vibium.dev/white`, doesn't move. Portrait and landscape
each keep their own, picked by the shape of the picture; where there is
none, saved means auto. A crop saved at another capture resolution is
ignored. The choice and the saved crops survive restarts (in
`~/.config/roadie/crop-<name>.json`, by `--name`). The viewer's settings
panel and [`/settings`](#get-settings) have the same controls.

- `GET` returns the state below.
- `PUT {"use": "auto"}` (or `"off"`, `"saved"`) chooses. Choosing auto or off
  keeps the saved crops; `saved` needs one.
- `POST` with no body saves the crop detected now, for the way the target is
  turned, and chooses saved. With `{"x": 734, "y": 54, "width": 452,
  "height": 972}` (frame pixels) it saves that rectangle instead, as portrait
  or landscape by its shape; add `"orientation"` to say which.
- `DELETE` clears both saved crops; `?orientation=portrait` or `landscape`
  clears one. Clearing the last one while saved is chosen goes back to auto.

**Response:**
```json
{
  "use": "saved",
  "mode": "saved",
  "orientation": "portrait",
  "saved": {
    "portrait": {"x": 734, "y": 54, "width": 452, "height": 972, "frame_width": 1920, "frame_height": 1080},
    "landscape": null
  },
  "detected": {"x": 750, "y": 76, "width": 418, "height": 931, "frame_width": 1920, "frame_height": 1080}
}
```

| Field | Description |
|-------|-------------|
| `use` | The crop chosen: `auto`, `off` or `saved` |
| `mode` | What decides the crop now: `use`, except `auto` when `saved` is chosen and there is none for the way the target is turned |
| `orientation` | `portrait` or `landscape`: the picture's shape as detected, empty until something bright was seen |
| `detected` | The picture's edges as last detected: what `POST` saves |

`POST` answers `409` when nothing is detected, because the screen is black at
its edges, and `400` for a rectangle outside the frame or one too square to
call portrait or landscape. `PUT {"use": "saved"}` answers `409` when no crop
is saved.

-------|-------------|
| `mode` | What decides the crop now: `saved`, `auto` or `off` |
| `orientation` | `portrait` or `landscape`: the picture's shape as detected, empty until something bright was seen |
| `detected` | The picture's edges as last detected: what `POST` saves |

`POST` answers `409` when nothing is detected, because the screen is black at
its edges, and `400` for a rectangle outside the frame or one too square to
call portrait or landscape.

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
  "crop_mode": "auto",
  "audio": {"sampleRate": 48000, "channels": 2},
  "platform": "ios",
  "pointer_space": "screen",
  "input_mode": "touch"
}
```

| Field | Description |
|-------|-------------|
| `status` | `ok`, `disconnected`, `connecting`, or `no_signal` |
| `source_type` | `hardware` or `http` |
| `crop` | Present only when active crop differs from full frame |
| `crop_mode` | `saved`, `auto` or `off`: what decides the crop (see [`/api/crop`](#get-put-post-delete-apicrop)) |
| `audio` | Present only when audio capture is active. With `--hid-backend linux-gadget`, `source` says which of the phone's outputs `/audio` is following, `usb` (the gadget's speaker) or `hdmi`, while the phone is playing |
| `hid_board` | `not_answering` when the far end is not taking input: the HID board is silent (RP2040), or no phone has enumerated the gadget (linux-gadget) |
| `platform` | Present only when `--platform` is set |
| `pointer_space` | `screen` or `frame` — which space the target reads absolute coordinates in (see `--platform`) |
| `input_mode` | The mode viewers start in: `--input`, else the platform default |

---

## Device Reset

### `POST /api/capture/reset`
Reset the USB capture device (unbind/rebind). Forces HDMI re-negotiation — useful when the signal is glitchy after initial connection. The stream will briefly disconnect and auto-recover.

Only the dongle this instance is capturing from is reset, so it is safe to call when another Roadie instance is using a second dongle on the same host.

**Response:**
```json
{"status": "ok"}
```

Requires Linux and a udev rule (installed by `make setup`). Returns `503` if capture is not available.

### `POST /api/hid/reset`
Reset the HID board. The relay sends a reset command over UART; the HID board reboots.

With `--hid-backend linux-gadget` the target instead sees the whole USB gadget
unplugged and plugged back in (keyboard, mouse, touch, audio and Ethernet):
exceptional recovery, never done automatically. Needs roadie's user in the
`roadie` group.

**Response:**
```json
{"status": "ok"}
```

### `POST /api/relay/reset`
Reset the relay board. The board reboots immediately, dropping the USB serial connection. The server auto-reconnects within a few seconds.

With `--hid-backend linux-gadget`, roadie closes and reopens its HID device
nodes and forgets what was held; the target sees nothing.

**Response:**
```json
{"status": "ok"}
```

---

## HID Control

All HID endpoints require the relay board to be connected via USB serial, or
with `--hid-backend linux-gadget`, the gadget's device nodes to be open.
Returns `503` if HID is not available.

The two backends accept the same commands with the same meaning. With the
gadget, a command that cannot reach the phone returns `500` with the reason
(for instance, no phone attached), text with characters a US keyboard cannot
type is rejected before anything is typed, and a release that fails is still
delivered once the phone is reading again.

### `GET /api/hid/status`
Connection status of the relay board.

**Response:**
```json
{
  "status": "connected",
  "backend": "rp2040",
  "serial": "A1B2C3D4E5F60001",
  "port": "/dev/serial/by-id/usb-Adafruit_Roadie-Relay_A1B2C3D4E5F60001-if02"
}
```

With `--hid-backend linux-gadget`:

```json
{
  "status": "connected",
  "backend": "linux-gadget",
  "usb_state": "configured",
  "port": "fe980000.usb",
  "serial": "10000000ff2137f1"
}
```

Status values: `connected`, `disconnected`, `connecting`, `unavailable`.
`backend` is `rp2040` or `linux-gadget`. `usb_state` is the USB device
controller's state: `configured` once a phone has enumerated the gadget;
`not attached`, `suspended` and others otherwise. For the gadget, `port` is
the controller and `serial` the USB serial number the phone sees.

`serial` and `port` identify the relay board this instance is bound to, and
are present only while connected. They are the way to tell two Roadie
instances on the same host apart — see [Running Multiple
Targets](docs/how-to/multiple-targets.md). `serial` is omitted on macOS,
where the port path does not embed the board's serial number.

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

The pointer jumps there. With glide on (`--glide`, or `{"glide": true}` to
[`/api/settings`](#put-apisettings)), a jump from rest glides instead, as a
hand would move a mouse: along a slight arc, slow at both ends, in 100 to 300
ms by distance, and the call returns when the pointer arrives. It's off by
default, because clients expect a move to be instant, and on a computer a
glide can open hover menus on its way. Even with it on, taps, short moves
(under about 3% of the screen), moves with a button held, and moves sent less
than 100 ms apart go straight there, so a drag or a client's own animation
keeps its path. It applies to WebDriver BiDi moves too.

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

An iPhone takes no touchscreen, so with `--platform ios` touch arrives as the
pointer instead: a finger down presses the button there, moving it drags, and
lifting it releases. Taps, held touches and one-finger swipes work as on
Android; two fingers at once are refused (`500`, "one finger at a time").

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

### `WS /api/hid/audio`
WebSocket endpoint for voice input: audio sent here plays out of the HID board's USB microphone (or, with `--hid-backend linux-gadget`, the Pi's USB gadget microphone, upsampled to 48 kHz), so the target hears it as mic input.

**Protocol:**
1. Client streams PCM audio as binary messages: 16 kHz, mono, signed 16-bit little-endian.
2. Messages should be small (~20 ms / 640 bytes) and sent at real-time pace. The HID board plays from a 400 ms ring buffer and starts once 100 ms has arrived; a burst longer than the ring overwrites audio that has not been played yet.

The stream on the target stops 200 ms after the last message arrives. Returns `503` if the HID relay is not connected.

The viewer's microphone button uses this endpoint. It needs a secure context (https or localhost) for `getUserMedia`, and a browser that supports opening an `AudioContext` at 16 kHz (Chrome, Edge, Safari — not Firefox yet).

---

## WebDriver BiDi

### `WS /session`

A [WebDriver BiDi](https://w3c.github.io/webdriver-bidi/)-flavored WebSocket endpoint. This allows BiDi-speaking automation tools (like [Vibium](https://github.com/VibiumDev/vibium)) to control a physical device through Roadie using the same protocol they use for browsers.

Roadie implements a minimal subset of BiDi — only the methods that map to hardware KVM capabilities.

**Protocol:** JSON over WebSocket. Client sends `{"id": <int>, "method": "<method>", "params": {...}}`. Server responds with `{"type": "success", "id": <int>, "result": {...}}` or `{"type": "error", "id": <int>, "error": "<code>", "message": "<detail>"}`.

**Supported methods:**

| Method | Description |
|--------|-------------|
| `session.status` | Check if Roadie is ready (capture + HID connected) |
| `session.new` | Create a session (one at a time) |
| `session.end` | End the session, release all held input |
| `browsingContext.getTree` | Returns a single context (`"screen"`) representing the target display |
| `browsingContext.captureScreenshot` | Returns a base64-encoded JPEG of the current frame. Right after input, it first waits for the screen to show it: at least 300 ms, then until the picture stops changing (at most 2 s after the input) |
| `roadie:screen.getViewport` | Returns the current viewport (cropped image) width and height |
| `input.performActions` | Execute keyboard, mouse, and touch actions. On an iPhone touch arrives as the pointer, one finger at a time; a gesture with more fingers is refused before any of it runs (see [`/api/hid/touch`](#post-apihidtouch)) |
| `input.releaseActions` | Release all held keys, buttons, and touches |

**Example session (using [websocat](https://github.com/vi/websocat)):**

```bash
websocat ws://localhost:8080/session
```

```json
{"id": 1, "method": "session.new", "params": {"capabilities": {}}}
{"id": 2, "method": "roadie:screen.getViewport", "params": {}}
{"id": 3, "method": "browsingContext.captureScreenshot", "params": {"context": "screen"}}
{"id": 4, "method": "input.performActions", "params": {"context": "screen", "actions": [{"type": "pointer", "id": "mouse", "actions": [{"type": "pointerMove", "x": 243, "y": 530}, {"type": "pointerDown", "button": 0}, {"type": "pointerUp", "button": 0}]}]}}
{"id": 5, "method": "input.performActions", "params": {"context": "screen", "actions": [{"type": "pointer", "id": "finger0", "parameters": {"pointerType": "touch"}, "actions": [{"type": "pointerMove", "x": 243, "y": 530}, {"type": "pointerDown", "button": 0}, {"type": "pointerUp", "button": 0}]}]}}
{"id": 6, "method": "session.end", "params": {}}
```

**Example: draw a checkbox with touch (using [websocat](https://github.com/vi/websocat)):**

Draws a square then a checkmark whose final stroke extends past the box.

```bash
websocat ws://localhost:8080/session
```

```json
{"id": 1, "method": "session.new", "params": {"capabilities": {}}}
{"id": 2, "method": "input.performActions", "params": {"context": "screen", "actions": [{"type": "pointer", "id": "finger0", "parameters": {"pointerType": "touch"}, "actions": [{"type": "pointerMove", "x": 143, "y": 430}, {"type": "pointerDown", "button": 0}, {"type": "pointerMove", "x": 343, "y": 430, "duration": 250}, {"type": "pointerMove", "x": 343, "y": 630, "duration": 250}, {"type": "pointerMove", "x": 143, "y": 630, "duration": 250}, {"type": "pointerMove", "x": 143, "y": 430, "duration": 250}, {"type": "pointerUp", "button": 0}, {"type": "pointerMove", "x": 170, "y": 510}, {"type": "pointerDown", "button": 0}, {"type": "pointerMove", "x": 230, "y": 580, "duration": 200}, {"type": "pointerMove", "x": 370, "y": 380, "duration": 350}, {"type": "pointerUp", "button": 0}]}]}}
{"id": 3, "method": "session.end", "params": {}}
```

**Viewport size:** `session.new` returns `roadie:viewport: {width, height}` in capabilities, and `browsingContext.captureScreenshot` includes it in the result. This tells you the coordinate space for `input.performActions` — pixel coordinates are relative to this viewport (the cropped screenshot). Roadie translates them to the 0–32767 HID absolute coordinate range internally.

**Touch input:** Use pointer actions with `"parameters": {"pointerType": "touch"}`. Up to 2 simultaneous touch contacts are supported.

**Key input:** Key values follow the [WebDriver key mapping](https://w3c.github.io/webdriver/#keyboard-actions) — printable characters as literals, special keys as Unicode Private Use Area values (e.g., `"\uE007"` for Enter, `"\uE008"` for Shift).

**Session limit:** Only one BiDi session can be active at a time. The session is automatically cleaned up when the WebSocket disconnects.

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
| `--video` | (auto-detect) | Video device name substring |
| `--audio` | (`--video`) | Audio device name substring |
| `--source` | | HTTP MJPEG source URL (mutually exclusive with --video) |
| `--port` | (auto, from 8080) | HTTP server port |
| `--width` | 1920 | Capture width |
| `--height` | 1080 | Capture height |
| `--fps` | 30 | Capture framerate |
| `--quality` | 80 | JPEG quality (30-95) |
| `--name` | roadie | mDNS service name |
| `--relay` | (auto-detect) | Relay board serial number substring, for picking between boards |
| `--platform` | | Target OS: `android`, `ios`, `mac`, `windows`, `linux`. Tunes pointer mapping and the default input mode |
| `--input` | (viewer decides) | Default pointer mode for viewers: `mouse` or `touch` |
| `--hid-backend` | rp2040 | Input hardware: `rp2040` (relay and HID boards) or `linux-gadget` (this Pi's USB-C port; see [docs/how-to/linux-gadget.md](docs/how-to/linux-gadget.md)) |
| `--list-devices` | | List video and audio devices, then exit |
