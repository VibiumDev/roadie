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
tellable apart. Click/touch the stream to send mouse or touch events to the target. Keyboard events are captured automatically (except when interacting with the toolbar). Includes audio toggle, microphone toggle (voice input to the target; needs https or localhost), quality/FPS/resolution settings, and mouse/touch input mode toggle. On a touchscreen in mouse mode, two fingers moving together turn the wheel, and two fingers moving apart or towards each other become Apple Maps' one-finger zoom, since a device whose pointer stands in for touch never sees two fingers. That zoom is a double tap at their centre with the second tap held and dragged, up to zoom in and down to zoom out (Google Maps zooms the same way; other apps get a double tap and a drag). In touch mode two fingers go to the target as two contacts. A Screenshot button saves the picture as it is, sending nothing to the target.

With `--platform ios` or `android` the toolbar also has the phone's own buttons, sent as the shortcuts a phone takes from a hardware keyboard, so no consumer-control device is needed:

- Home is GUI+H (Cmd+H on iOS, Meta+H on Android).
- Lock, like the phone's side button, locks (Ctrl+Cmd+Q on iOS, the keyboard Power key on Android) and, while the screen is dark (`no_signal`), wakes it instead (Shift on iOS, Power on Android).
- Android also gets Back (Escape), Recents (Alt+Tab) and volume up and down (the keyboard volume keys).

iOS has no system-wide Back or Recents shortcut, and its volume is fixed while Roadie's audio is its output. On iOS, Rotate turns the screen through [`/api/rotate`](#post-apirotate). Input mode persists across page refreshes via `localStorage`.

| Param | Description |
|-------|-------------|
| `toolbar` | `show`, `auto` (auto-hide: tucked off its edge, with a grip, a tap at the edge or the pointer at the edge bringing it back) or `off`. Sets the toolbar, and this browser remembers it, like the choice in the toolbar's own menu or on `/settings`; `?toolbar=show` brings back a toolbar turned off. |
| `minimal` | `1` hides the toolbar for this page load only, leaving the remembered choice alone, for embedding in an iframe (see `/grid`). |
| `input` | `mouse` or `touch` sets the pointer mode for this page load, overriding the server's `--input`. |
| `align` | `top-left` or `center`: where the picture sits, for this page load; the menu's choice is what this browser remembers. |
| `zoom` | `50` to `400`, the picture's size as a percentage of fitting the window, for this page load; the menu's Zoom slider is what this browser remembers. With `toolbar=off`, `align=top-left` and `zoom=100`, a window the shape of the picture shows the picture and nothing else, for recordings. |
| `transport` | `webrtc` or `mjpeg` for this page load. WebRTC, the default when the host has an encoder (`webrtc` in [`/health`](#get-health)), shows the picture as H.264 over a peer connection, carries the phone's sound to the speaker button as G.722, takes the microphone button's audio back the same way, and sends input over the connection's `hid` data channel; the menu's Video switch does the same at any time and this browser remembers the choice. The page goes back to MJPEG, `/audio`, `/api/mic` and `/api/hid/ws` by itself if the connection cannot be made or drops for good. The menu's Quality slider is the MJPEG stream's JPEG quality and shows in MJPEG mode; in WebRTC mode a Bitrate slider caps the H.264 stream instead (300 kbps to 4 Mbps, the top meaning automatic), remembered by this browser. Screenshots have their own Screenshot quality slider lower in the menu (default 90), also remembered. |
| `stats` | `1` shows a box over the picture for this page load: which transport shows it (MJPEG with its JPEG quality, or WebRTC with H.264), the picture's size, frame rate and bitrate, the sound (over WebRTC the G.722 bitrate and, while the mic button is on, the mic's; over MJPEG `off`, or the PCM format and bitrate on `/audio` and, while talking, on `/api/mic`), and over WebRTC the delays (round trip between browser and host, the browser's jitter buffer, decoding) and the encoder's own frame rate, bitrate and target. The menu's Stats box does the same and is remembered. The WebRTC values are always on `window.__roadieRTCStats`. |

Pointer mode resolves in this order:

1. `?input=` for this page load
2. the server's `--input`
3. the default implied by `--platform`
4. a mode this viewer chose from the toolbar
5. mouse

`--input` is the usual place to set it, since the mode is a property of the
target device rather than of the browser viewing it.

Neither `?input=` nor `--input` is written to the browser's stored preference,
so the toolbar toggle still works and still holds for the rest of the session —
a reload returns to the configured mode.

### `GET /login`, `POST /login`, `GET /logout`
With `--auth token`, the one-field login page. The form posts `token` and
`next` (a path on this site); a right token sets an `HttpOnly`,
`SameSite=Lax` cookie for 30 days (`Secure` over HTTPS) and goes on to
`next`. Without a cookie, a browser asking for any page is sent here; an
API call gets `401` with `WWW-Authenticate: Bearer` and sends
`Authorization: Bearer <token>` instead. Twenty wrong tokens from one
address in ten minutes are refused with `429`. `/logout` clears the cookie.
`roadie token` prints the token; `roadie token --rotate` replaces it and
signs every browser out. With the other `--auth` modes `/login` just goes to
`/`.

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
Shows the capture settings (JPEG quality, frames per second, capture size), the [crop](#get-put-post-delete-apicrop), this browser's viewer choices (zoom, with Fit, and the toolbar) and the device's details in the front page's look. Each section has a "?" that explains it and its settings.

### `GET /soundcheck`

A microphone check page. Opens the browser's microphone, converts it to
16 kHz mono PCM, and streams it to `/api/mic` — the same path `/view`'s mic
button uses, with a level meter and a status line.

Served by roadie so that it is same-origin with `/api/mic`, which refuses
connections from other origins. `make test-audio-browser` drives this page;
it is also useful on its own for checking a microphone by hand.

Optional `?report=<url>` makes the page GET that URL once it is streaming,
which is how the test knows audio is actually flowing before it listens.

### `GET /test`
Interactive HID test page with mouse/touch trackpad, keyboard input, and key combo controls. Supports Mouse mode (pointer + scroll wheel) and Touch mode (multi-touch digitizer with pinch-to-zoom). Trackpad overlays the auto-cropped MJPEG stream and auto-adjusts aspect ratio to match the target's video signal. Coordinates are remapped to account for crop offset so touches align with visible content. Input mode persists via `localStorage` (shared with `/view`). Communicates with the target via WebSocket (`/api/hid/ws`).

---

## Video

### `GET /stream`

`?fps=N` asks for fewer frames a second than the capture's rate.
MJPEG stream (auto-cropped to detected content area).

**Response:** `multipart/x-mixed-replace` with `image/jpeg` frames.

### `GET /snapshot`
Single JPEG frame (auto-cropped).

**Response:** `image/jpeg`

`?quality=30..95` makes this picture at that JPEG quality instead of the stream's (the viewer's Screenshot button uses its own Screenshot quality this way). Without it, or when frames come from an HTTP source, the stream's quality applies.


### `GET /raw-stream`
MJPEG stream (uncropped, full capture resolution).

**Response:** `multipart/x-mixed-replace` with `image/jpeg` frames.

### `GET /raw-snapshot`
Single JPEG frame (uncropped).

**Response:** `image/jpeg`

`?quality=30..95` works here too.

---

## Audio

Audio *from* the target. Voice input *into* the target is
[`WS /api/mic`](#ws-apimic), with the other endpoints that drive
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
phone: the USB speaker whenever the phone plays to it, and HDMI otherwise. If
a switch changes the format, the server closes the socket so clients reconnect
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
each keep their own, picked by the shape of the picture; where there is none,
saved means auto. A crop saved at one capture resolution is scaled to another,
as the dongle scales the picture. The choice and the saved crops survive
restarts (in `crop-<name>.json` in Roadie's config directory, by `--name`:
`~/.config/roadie` for a build run by hand, `/var/lib/roadie/.config/roadie`
for the service). The viewer's
settings panel and [`/settings`](#get-settings) have the same controls.

- `GET` returns the state below.
- `PUT {"use": "auto"}` (or `"off"`, `"saved"`) chooses. Choosing auto or off
  keeps the saved crops; `saved` needs one.
- `POST` with no body saves the crop detected now, for the way the target is
  turned, and chooses saved. It leaves out up to two lines along each edge
  that stand out from the picture: one half-lit where the screen's edge falls
  partway across a pixel, and one the capture's sharpening brightened. With
  `{"x": 737, "y": 54, "width": 446, "height": 972}` (frame pixels) it saves
  that rectangle instead, as portrait or landscape by its shape; add
  `"orientation"` to say which.
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
  "input_mode": "touch",
  "host": {"cpu_percent": 68, "load1": 1.71, "temp_c": 80.3, "mem_total_mb": 7809, "mem_used_percent": 25},
  "webrtc": {"encoder": "v4l2m2m", "sessions": 1, "bitrate": 1500000, "width": 444, "height": 972, "fps": 30}
}
```

| Field | Description |
|-------|-------------|
| `status` | `ok`, `disconnected`, `connecting`, or `no_signal` (the target sends no picture: its screen is off or locked, or the HDMI cable is out; the stream is dark meanwhile) |
| `source_type` | `hardware` or `http` |
| `crop` | Present only when active crop differs from full frame |
| `crop_mode` | `saved`, `auto` or `off`: what decides the crop (see [`/api/crop`](#get-put-post-delete-apicrop)) |
| `audio` | Present only when audio capture is active. With `--hid-backend linux-gadget`, `source` says which of the phone's outputs `/audio` is following, `usb` (the gadget's speaker) or `hdmi`, while the phone is playing |
| `hid_board` | `not_answering` when the far end is not taking input: the HID board is silent (RP2040), or no phone has enumerated the gadget (linux-gadget) |
| `platform` | Present only when `--platform` is set |
| `pointer_space` | `screen` or `frame` — which space the target reads absolute coordinates in (see `--platform`) |
| `rotating` | `true` while [`/api/rotate`](#post-apirotate) is turning the screen, and absent otherwise. `/view` holds back its input meanwhile |
| `input_mode` | The mode viewers start in: `--input`, else the platform default |
| `host` | The machine Roadie runs on: Roadie's own CPU use (`cpu_percent`, 100 is one core), the 1-minute load average, the CPU temperature in °C, total memory in MB, and the percentage of memory in use (not available to new programs). Whatever the machine doesn't tell is left out |
| `webrtc` | Present when the host can encode H.264 (see `--encoder`): the encoder in use or available, how many viewers are connected over WebRTC, the target bitrate in bits per second, and while encoding the picture's size, the frames encoded in the last second and the frames skipped (`dropped`); `audio` (`G722`) and `audio_kbps` while the phone's sound is being sent, `mic` and `mic_kbps` while a viewer's microphone is coming in. Absent means `/view?transport=webrtc` falls back to MJPEG |

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

### `POST /api/usb/reset`
Replug every device Roadie presents to the target, which sees them unplugged
and plugged back in. With the boards, the relay tells the HID board over UART
to reboot. With `--hid-backend linux-gadget`, the Pi's whole USB gadget is
disconnected and reconnected: its keyboard, mouse, touch, audio and network
are one USB device, so they go together. It ends a touch the phone has got
stuck on. Exceptional recovery, never done automatically; with the gadget it
needs roadie's user in the `roadie` group.

**Response:** what the target saw replugged.
```json
{"status": "ok", "replugged": ["keyboard", "mouse", "touch", "microphone", "speaker", "network"]}
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

## Rotation

### `POST /api/rotate`
Turns an iPhone's screen (`--platform ios`) to portrait or landscape.

**Request:**
```json
{"to": "landscape"}
```

**Response:**
```json
{"status": "ok", "orientation": "landscape"}
```

No key says which way an iPhone's screen faces, but AssistiveTouch's menu
does. Roadie opens it with mouse button 5, which opens it around the
pointer, clicks Device, Rotate Screen, and Portrait or Left (landscape), and
then closes it. That takes 4 to 5 s in all, most of which is the phone
showing each click (about 6 s on a screen that can't turn, where Roadie
waits 2 s for a turn that doesn't come). The phone needs button 5 set up
once: see [Connect a Phone](docs/how-to/connect-a-phone.md#turning-the-screen).

Roadie looks before every click. It finds the menu near the middle of the
picture by comparing it with stored images of each page (no AI, about 40 ms),
and clicks an item where it shows, but only once the page holding it has
shown in two frames in a row, without waiting for the menu to stop
animating. So a menu that was already open, opened off the middle, or on
another page is dealt with, and when there's no menu nothing lands on the
app. If a click misses its item (the phone reads the pointer turned, as
after a restart of Roadie while the phone took itself to be on its side),
Roadie tries the other way once.

While a turn is under way, [`/health`](#get-health) says `"rotating": true`:
the pointer moving under Roadie's clicks would spoil them. `/view` greys the
picture, says "Rotating the screen…", and holds back the viewer's mouse,
touch and keys, letting go of anything held first. Roadie also drops input
arriving over [`WS /api/hid/ws`](#ws-apihidws) meanwhile, all but releases.
Other clients of the HID API aren't held back, so they should wait too.

`orientation` is the picture's. The app on screen turns with the phone if it
can; the home screen and Settings don't. The phone still takes itself to be
on its side there, and reads the pointer that way; Roadie turns the pointer
to match, so taps land where they're aimed, until `{"to": "portrait"}` turns
it upright.

| Status | When |
|---|---|
| `409` | A turn is already under way. |
| `422` | The menu didn't open (button 5 isn't set to Open Menu), or didn't show the page expected; Roadie stopped clicking. |
| `501` | `--platform` isn't `ios`. |
| `503` | No picture from the phone, or no HID. |

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
hand would move a mouse. The pointer travels along a slight arc, slow at both
ends, in 100 to 300 ms by distance, and the call returns when it arrives.
It's off by default, because clients expect a move to be instant, and on a
computer a glide can open hover menus on its way. Even with it on, taps,
short moves (under about 3% of the screen), moves with a button held, and
moves sent less than 100 ms apart go straight there, so a drag or a client's
own animation keeps its path. It applies to WebDriver BiDi moves too.

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
| `buttons` | Button mask: `1` = left, `2` = right, `4` = middle, `8` = button 4, `16` = button 5. Default: `1` |
| `action` | `click` (default), `press`, or `release` |

**Response:**
```json
{"status": "ok"}
```

### `POST /api/hid/mouse/scroll`
Turn the mouse wheel, scrolling what is under the pointer.

**Request:**
```json
{"amount": 5}
```

| Field | Description |
|-------|-------------|
| `amount` | Notches: positive scrolls down, negative up, on every platform. Range: -127 to 127 |

An iPhone (`--platform ios`) scrolls only for a run of notches, and speeds
a run up. Roadie sends each scroll as single notches 8 ms apart, so a call
returns after `amount` × 8 ms. Measured in Safari, on a picture 972 px tall:

| Notches | 2 | 3 | 4 | 5 | 7 | 10 |
|---------|---|---|---|---|---|----|
| Scrolled | 0 px | 35 px | 97 px | 168 px | 343 px | 626 px |

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
{"cmd": "tap", "x": 16383, "y": 16383}
```

No server-to-client messages. Connection auto-reconnects on the `/test` page.
While [`/api/rotate`](#post-apirotate) turns the screen, messages are dropped
unless they only let go of something (`key_release`, `mouse_release`, a
`touch` with no finger down). `tap` is a whole tap in one command — a touch down at the point, a 60 ms hold and the lift — for viewers on links where the gap between separate down and up messages would stretch; `mouse_click` is the same for the mouse.

---

### `WS /api/webrtc/signal`
WebRTC signaling for `/view?transport=webrtc` (and for headless clients).
The client sends `{"type":"hello"}` first; Roadie answers with
`{"type":"offer","sdp":"..."}` and then `{"type":"candidate","candidate":{...}}`
messages as it finds its addresses; the client sends
`{"type":"answer","sdp":"..."}` and its own `candidate` messages, and
`{"type":"bye"}` to end. `{"type":"error","error":"..."}` ends it from
Roadie's side. The offer carries one H.264 video track (Constrained
Baseline, `profile-level-id=42e01f`, packetization mode 1; the stream itself
is level 4.0 and SPS and PPS ride every keyframe) and a data channel named
`control` on which the client may send `{"type":"keyframe"}` or
`{"type":"bitrate","bps":N}` (a cap, 0 to lift it), and Roadie sends
`{"type":"stats","status":{...},"kbps":N}` once a second with the `webrtc`
object from `/health` and the bits sent in the last second.

The offer also carries one audio line, both ways. Roadie sends the phone's
sound on it as G.722 (16 kHz, 64 kbit/s; only G.722 and PCMU are offered,
so a browser does not pick Opus) in 20 ms packets, while anyone is
connected and the capture has audio. A client that puts its own G.722 or
PCMU track on the same line, as the viewer's microphone button does with
`replaceTrack`, has it decoded on the host as 16 kHz mono into the path
[`/api/mic`](#ws-apimic) feeds. One client's microphone reaches the target
at a time: the first to send keeps it until its track ends. And a second
data channel, `hid`, takes the JSON commands [`/api/hid/ws`](#ws-apihidws)
accepts, one per message, into the same handler. `status` then also reports
`audio` (`G722` while sound is being sent), `audio_kbps`, and `mic` and
`mic_kbps` while a microphone is coming in.

Media uses one UDP port, `--rtc-udp`, which is the HTTP port's number by
default. `503` when the host has no encoder. The same messages are what a
station relays later.

### `GET /viewer/…`
The viewer's files: `shell.js`, `shell.css` and `webrtc.js` are the shared shell from the [Station viewer package](https://github.com/VibiumDev/station), the same page a station serves for a device, while `extras.js` is Roadie's own addition to it. It adds the phone's buttons (Home, Lock and Wake, Rotate, Android's Back, Recents and volume), the capture settings, the crop and the resets, and is served with what it needs to know about this host (`window.__roadieDevice`: platform, HID backend, USB devices) in front. A station relays `extras.js` to its own page, so the two viewers are one code path with one look.

`/view` renders the shell with its endpoints at the root and these parameters:

- `?minimal=1` hides the toolbar (for a frame).
- `?input=mouse|touch` and `?transport=webrtc|mjpeg` set the pointer mode and the picture's transport for that page load.
- `?toolbar=show|auto|off`, `?align=`, `?zoom=` and `?stats=1` are the page's own.

### `WS /api/mic`
WebSocket endpoint for voice input: audio sent here plays out of the HID board's USB microphone (or, with `--hid-backend linux-gadget`, the Pi's USB gadget microphone, upsampled to 48 kHz), so the target hears it as mic input.

**Protocol:**
1. Client streams PCM audio as binary messages: 16 kHz, mono, signed 16-bit little-endian.
2. Messages should be small (~20 ms / 640 bytes) and sent at real-time pace. The HID board plays from a 400 ms ring buffer and starts once 100 ms has arrived; a burst longer than the ring overwrites audio that has not been played yet.

The stream on the target stops 200 ms after the last message arrives. Returns `503` if the HID relay is not connected.

The viewer's microphone button uses this endpoint when the picture comes over MJPEG; over WebRTC the microphone travels as an audio track on the peer connection instead (see [`/api/webrtc/signal`](#ws-apiwebrtcsignal)), which also works in Firefox. The WebSocket needs a secure context (https or localhost) for `getUserMedia`, and a browser that supports opening an `AudioContext` at 16 kHz (Chrome, Edge, Safari — not Firefox yet). To try the mic over plain http before TLS is set up, Chrome can treat one origin as secure: `chrome://flags/#unsafely-treat-insecure-origin-as-secure` with the host's URL.

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
| `session.new` | Create a session (one at a time). A connection that drops without `session.end` frees its own session; another connection closing does not. A connection that sends no command for 5 minutes is closed (`1001`, "idle for 5m0s") and its session ends |
| `session.end` | End the session, release all held input |
| `browsingContext.getTree` | Returns a single context (`"screen"`) representing the target display |
| `browsingContext.captureScreenshot` | Returns a base64-encoded JPEG of the current frame. Right after input, it first waits for the screen to show it: at least 300 ms, then until the picture stops changing (at most 2 s after the input) |
| `roadie:screen.getViewport` | Returns the current viewport (cropped image) width and height |
| `input.performActions` | Execute keyboard, mouse, touch and wheel actions. On an iPhone touch arrives as the pointer, one finger at a time; a gesture with more fingers is refused before any of it runs (see [`/api/hid/touch`](#post-apihidtouch)). A `wheel` source's `scroll` turns the wheel with the pointer at `x`, `y` (viewport origin only), then puts the pointer back; `deltaY` (vertical only) becomes notches of [the wheel](#post-apihidmousescroll): on an iPhone as many as scroll about that far, elsewhere one per 32nd of the viewport |
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
{"id": 6, "method": "input.performActions", "params": {"context": "screen", "actions": [{"type": "wheel", "id": "wheel", "actions": [{"type": "scroll", "x": 243, "y": 530, "deltaX": 0, "deltaY": 300}]}]}}
{"id": 7, "method": "session.end", "params": {}}
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

**Key input:** Key values follow the [WebDriver key mapping](https://w3c.github.io/webdriver/#keyboard-actions), with printable characters as literals and special keys as Unicode Private Use Area values (e.g., `"\uE007"` for Enter, `"\uE008"` for Shift).

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


## Vibium Station

Roadie can enroll as a rig with a [Vibium Station](https://github.com/VibiumDev/station), which brokers access to it from anywhere: the station holds the rig's outbound tunnel, signs session holds, and serves the shared viewer. Nothing is opened on the Pi; it dials out.

```bash
roadie enroll https://station.example --code ABCD-1234-7F3A [--name NAME] [--platform ios] [--admin]
```

The code comes from the station's Settings page or `station enroll`; it ends with the station key's fingerprint, which `roadie enroll` checks against the key the station presents before it sends anything. Without a URL the station is found on the LAN over mDNS (`_vibium-station._tcp`). On success the policy file is written and `roadie` connects at its next start.

Files, next to the local token in Roadie's config directory (`~/.config/roadie` for a build run by hand; `/var/lib/roadie/.config/roadie` for the service, whose `roadie token` and `roadie enroll` then run as `sudo -u roadie`):

| File | Holds |
|---|---|
| `rig.key` | The rig's secp256k1 key, made on first use; its npub is in `/health` |
| `rig.json` | The policy: the stations accepted, and per station the allowed scopes (`view`, `input`, `bidi`), whether `admin` grants are taken, `max_session` and optional `hours` (`"09:00-18:00"`). Written by `roadie enroll`, edited by hand; nothing remote can change it |
| `rig-state.json` | The hold state: the current hold per device and the highest sequence seen, so a restart cannot be made to accept an older hold |

Through the tunnel the station reaches only an allowlist of endpoints, each with the scope a grant must carry, and every request presents a hold grant, either in `Authorization: Hold <base64url event>` for plain requests or in the first message of a WebSocket. Today that is:

- [`/api/webrtc/signal`](#ws-apiwebrtcsignal) (`view`; the hello carries `"grant"`)
- `GET /health` (`view`)
- [`GET /stream`](#get-stream) (`view`, capped at `--relay-fps`, 5 a second by default, for the station's MJPEG fallback)
- [`GET /snapshot`](#get-snapshot) (`view`)
- [`/api/hid/ws`](#ws-apihidws) (`input`, the fallback's input path)
- [`/audio`](#get-audio) (`view`, sent as 16 kHz mono 16-bit through a station)
- [`/api/mic`](#ws-apimic) (`input`)
- [`/session`](#ws-session) (`bidi`: WebDriver BiDi, relayed byte for byte, so a generic BiDi client through the station sees exactly what it would see here)
- `/api/hid/status` (`view`)
- the HID REST under `/api/hid/` and `/api/rotate` (`input`)
- `/api/settings`, `/api/crop` and the resets (`admin`, which the rig policy grants only with `roadie enroll --admin`)

Each hold is limited to 120 requests and messages a second (burst 240), one stream, one audio and one mic socket, one BiDi socket and two HID sockets; a hold that ends closes every connection admitted on it. A grant through a station with a TURN server names it (`stun` and `turn` tags), and Roadie's peer connection uses those servers, so the WebRTC path works across NATs. The grant is checked on the Pi, which accepts it only when it is:

- signed by a station in the policy
- for this rig and device
- not expired, replayed or revoked
- not older than the hold the device has
- inside the policy

A viewer refreshes its grant every few minutes by sending `{"type":"grant","grant":{...}}` on the control channel; a grant that no longer passes ends the session. A revocation from the station, or a newer hold, ends a hold's sessions within a second.

`/health` gains a `station` block: the rig's npub and, per station, its name, URL, rig id, whether the tunnel is up, and the hold currently held.

Input from a viewer that does not know the capture's crop, such as the station's, sends positions as fractions of the picture scaled to 0..32767 with `"space":"picture"` on `mouse_move` and `touch`; Roadie folds its crop in. Without `space`, coordinates are the absolute units described above.

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
| `--bind` | 127.0.0.1 without `--auth`, else every address | Address to listen on. `127.0.0.1` behind `tailscale serve`, which is when `--auth tailscale` trusts the `Tailscale-User-Login` header serve adds |
| `--auth` | | Who may use the pages and API: `token` (the local admin token; `roadie token` prints it), `tailscale` (whoever the local tailscaled says the connection belongs to), or `none` (no checks, with a warning at every start). Unset, Roadie listens on localhost only |
| `--allow` | anyone on the tailnet | With `--auth tailscale`: comma-separated Tailscale logins and tags (`you@example.com,tag:ci`) allowed |
| `--tls-cert`, `--tls-key` | | Serve HTTPS with this certificate and key (PEM); the files are reloaded when they change, so a renewal needs no restart. `roadie tls status --tls-cert FILE` shows the names and expiry |
| `--http-redirect` | | With TLS: also listen on this address (`:80`) and redirect to HTTPS |
| `--encoder` | auto | H.264 encoder for WebRTC: `auto` (the Pi 4's hardware encoder when present, else ffmpeg with the host's best codec), `v4l2m2m`, `ffmpeg`, `ffmpeg:<codec>` (`libx264`, `h264_videotoolbox`, `h264_vaapi`, `h264_nvenc`, `h264_qsv`, `h264_v4l2m2m`), or `none` |
| `--rtc-udp` | the HTTP port's number | UDP port for WebRTC media (one port for every viewer) |
| `--relay-fps` | 5 | Frame rate cap for the MJPEG stream when it goes through a station, so a remote viewer cannot eat the uplink |
| `--cpuprofile` | | Write a CPU profile to this file until exit, for `go tool pprof` |
| `--list-devices` | | List video and audio devices, then exit |
