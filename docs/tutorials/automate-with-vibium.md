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

## Next Steps

- [Roadie API Reference](../../API.md) — endpoints, HID keycodes, and the direct WebDriver BiDi endpoint
- [Vibium Getting Started](https://github.com/VibiumDev/vibium/blob/main/docs/tutorials/getting-started-js.md) — Vibium basics
