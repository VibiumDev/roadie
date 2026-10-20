# Automate Roadie with Vibium

[Vibium](https://github.com/VibiumDev/vibium) can drive Roadie two ways:

- **Connect to Roadie directly.** Roadie speaks
  [WebDriver BiDi](../../API.md#webdriver-bidi) at
  `ws://roadie.local:8080/session`, so Vibium treats it as a remote browser.
  No browser runs: a screenshot is the device's screen, and mouse and
  keyboard actions become USB input. Page commands like `go`, `find` and
  `title` don't apply.
- **Drive the viewer.** Vibium launches a browser, opens Roadie's `/view`
  page and works it like any web page; the viewer passes the mouse and
  keyboard on to the device. Screenshots are of the page.

Connecting directly is simpler, and it is what most of this tutorial does.
[Drive the viewer](#driving-the-viewer) to reach several devices from one
script, or to test the viewer itself.

Coordinates are pixels in Roadie's screenshot. Each example adds 1 + 2 in an
iPhone's Calculator, already open; take a screenshot first and adjust the
coordinates for your device. Save a crop first, too: otherwise a dark app like
Calculator can shrink the screenshot, and the coordinates move with it (see
[`/api/crop`](../../API.md#get-put-post-delete-apicrop)).

## Prerequisites

- Roadie running and connected to a target device (`./roadie`)
- Node.js 18+ for the CLI and JavaScript, or Python 3.9+ for Python

## CLI

```bash
npm install -g vibium
vibium start ws://roadie.local:8080/session
vibium mouse click 174 454   # AC
vibium mouse click 70 756    # 1
vibium mouse click 380 756   # +
vibium mouse click 174 756   # 2
vibium mouse click 380 860   # =
vibium screenshot -o screen.jpg
vibium stop
```

Roadie's screenshots are JPEG, so give them a `.jpg` name. A screenshot
taken right after input waits for the screen to show it. The CLI has no touch
command yet ([vibium#682](https://github.com/VibiumDev/vibium/issues/682)),
but on a phone a click is a tap.

## JavaScript

```bash
npm install vibium
```

Create `automate.mjs`:

```javascript
import { browser } from 'vibium'
import { writeFileSync } from 'fs'

const bro = await browser.start('ws://roadie.local:8080/session')
const screen = await bro.page()

// AC 1 + 2 =
for (const [x, y] of [[174, 454], [70, 756], [380, 756], [174, 756], [380, 860]]) {
  await screen.touch.tap(x, y)
}

writeFileSync('screen.jpg', await screen.screenshot())
await bro.stop()
```

```bash
node automate.mjs
```

An iPhone takes no touchscreen, so there Roadie touches with its pointer: a
tap, a held touch and a one-finger swipe work as they do on Android, and a
two-finger gesture is refused. `screen.mouse.click(x, y)` works too.
`screen.keyboard.type('text')` and `screen.keyboard.press('Enter')` send keys
to whatever has focus on the device.

## Python

```bash
pip install vibium
```

Create `automate.py`:

```python
from vibium import browser

bro = browser.start("ws://roadie.local:8080/session")
screen = bro.page()

# AC 1 + 2 =
for x, y in [(174, 454), (70, 756), (380, 756), (174, 756), (380, 860)]:
    screen.touch.tap(x, y)

with open("screen.jpg", "wb") as f:
    f.write(screen.screenshot())
bro.stop()
```

```bash
python automate.py
```

## With an AI agent (MCP)

Vibium's MCP server gives an agent the same session: it sees the device
through screenshots and clicks by coordinates (it has no tap tool yet, but on a
phone a click is a tap). For Claude Code:

```bash
claude mcp add vibium -e VIBIUM_CONNECT_URL=ws://roadie.local:8080/session -- npx -y vibium mcp
```

Then ask it, for example, "Open Calculator on the phone and work out 1 + 2."

## Driving the viewer

Here Vibium starts a browser of its own and opens `/view`. The picture sits
at the page's top-left at full size, so the coordinates are the same as above:

```javascript
import { browser } from 'vibium'

const bro = await browser.start()                      // launches a browser
const page = await bro.page()
await page.setViewport({ width: 1000, height: 1100 })  // room for the whole picture
await page.go('http://roadie.local:8080/view')

// AC 1 + 2 =
for (const [x, y] of [[174, 454], [70, 756], [380, 756], [174, 756], [380, 860]]) {
  await page.mouse.click(x, y)
}
await bro.stop()
```

The page's screenshots don't wait for the device to show your last click,
the way a screenshot over the direct connection does.

Roadie allows one direct connection at a time, so to drive several devices
from one script, open the
[grid](../how-to/multiple-targets.md#automating-several-targets) instead of
`/view`: the mouse and keyboard reach whichever device is under the pointer.

## Next Steps

- [Roadie API Reference](../../API.md) — endpoints, HID keycodes, and the WebDriver BiDi endpoint
- [Vibium Getting Started](https://github.com/VibiumDev/vibium/blob/main/docs/tutorials/getting-started-js.md) — Vibium basics
