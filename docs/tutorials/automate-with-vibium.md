# Automate Roadie with Vibium

Control a physical device through Roadie using JavaScript.

## Prerequisites

- Roadie running and connected to a target device (`./roadie`)
- Node.js 18+

## Setup

```bash
mkdir roadie-bot
cd roadie-bot
npm init -y
npm install vibium
```

## Example

Create `automate.mjs`:

```javascript
import { browser } from 'vibium'
import { writeFileSync } from 'fs'

const bro = await browser.start()
const screen = await bro.page()

// Open the Roadie viewer — the feed is top-left aligned by default,
// so mouse coordinates map directly to target screen pixels
await screen.go('http://roadie.local:8080/view')

// Move the mouse on the target device
await screen.mouse.move(500, 400)

// Press the Tab key
await screen.keyboard.press('Tab')

// Take a screenshot
const png = await screen.screenshot()
writeFileSync('screenshot.png', png)

await bro.stop()
```

```bash
node automate.mjs
```

## On a Raspberry Pi (arm64)

`browser.start()` with no arguments downloads a browser, and Vibium's
download currently asks for a linux64 build on arm64 too, so every launch
fails with `exec format error`
([vibium#638](https://github.com/VibiumDev/vibium/issues/638)). That bites on
a Pi — which is usually the machine roadie itself is running on.

Use the system browser instead and attach to it:

```bash
sudo apt install chromium chromium-driver
chromedriver --port=9515 --allowed-ips="" &
```

```javascript
// Create the session yourself, then hand Vibium its BiDi socket.
const res = await fetch('http://127.0.0.1:9515/session', {
  method: 'POST',
  headers: {'Content-Type': 'application/json'},
  body: JSON.stringify({capabilities: {alwaysMatch: {
    webSocketUrl: true,
    'goog:chromeOptions': {binary: '/usr/bin/chromium'},
  }}}),
})
const {value} = await res.json()
const bro = await browser.start(value.capabilities.webSocketUrl)
```

Everything after that is the same. `make test-view` drives roadie's own
viewer this way.

## Next Steps

- [Roadie API Reference](../../API.md) — endpoints, HID keycodes, and the direct WebDriver BiDi endpoint
- [Vibium Getting Started](https://github.com/VibiumDev/vibium/blob/main/docs/tutorials/getting-started-js.md) — Vibium basics
