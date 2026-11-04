# Automate Roadie with Vibium

Roadie speaks [WebDriver BiDi](../../API.md#webdriver-bidi), so
[Vibium](https://github.com/VibiumDev/vibium) can drive the phone behind
it the way it drives a remote browser, whether from the command line,
from a script in JavaScript or Python, or from an AI agent such as
Claude Code or Codex. No browser runs: a screenshot is the phone's
screen, and keys and touches become USB input on the phone.

In this tutorial you point Vibium at the phone and open the Calculator
through the phone's own Search with three key presses. You also keep a
recording of what happened. Nothing depends on where an icon sits on the
screen, so the same steps work on any iPhone.

## What you need

- A phone on a Roadie, awake and unlocked, with Roadie started with
  `--auth token` so other machines may connect. Without `--auth` it
  listens on localhost only. See [Run Roadie](../how-to/run-roadie.md).
- The token: `roadie token` prints it on the Pi.
- Node.js 18 or newer, or Python 3.9 or newer, on your machine.
- Vibium: `npm install -g vibium`, or `npx vibium` for one-off use.

## 1. Point Vibium at the phone

Two environment variables tell every Vibium tool where the phone is and
how to get in. On the same network as the Roadie:

```bash
export VIBIUM_CONNECT_URL=ws://roadie.local:8080/session
export VIBIUM_CONNECT_API_KEY=<the token roadie token prints>
```

You can also reach the phone from anywhere through a
[Vibium Station](https://github.com/VibiumDev/station), a server your
Roadie has joined that passes connections through to it from outside
your network. The same two variables then take the station's address
and one of its API tokens:

```bash
export VIBIUM_CONNECT_URL=wss://example.vibium.cloud/session
export VIBIUM_CONNECT_API_KEY=stk_...
```

The station gives `/session` your first online phone and keeps it yours
while the session lasts, so nobody else can drive it meanwhile. That
arrangement, along with waiting in line for a phone someone else has
and the station's tokens, is explained in the station's own tutorial,
[Reach a device remotely through the station](https://github.com/VibiumDev/station/blob/main/docs/tutorials/agent-through-the-station.md).
Everything below is the same either way.

Roadie serves one BiDi session at a time, while the viewer in a browser
keeps working beside it. A session whose connection drops is let go, and
so is one that sends no command for five minutes, so a forgotten `vibium
start` does not keep the phone for good.

## 2. Drive it from the command line

```bash
vibium start                      # connects to $VIBIUM_CONNECT_URL
vibium keys Meta+h                # home screen
vibium record start -o calc.zip   # a screenshot per step from here on
vibium screenshot -o home.jpg
vibium keys Meta+Space            # open Search
vibium type calc                  # into the search field
vibium keys Enter                 # launch the top hit, the Calculator
vibium screenshot -o calc.jpg
vibium keys Meta+h
vibium record stop                # writes calc.zip
vibium stop                       # ends the session
```

`vibium start` prints `Connected to ws://...`. After `Meta+Space` the
phone shows Search, after `calc` the Calculator is the Top Hit, and
after Enter it is open, which is what `calc.jpg` shows. Give screenshots
a `.jpg` name, since Roadie's are JPEG, and note that a screenshot taken
right after input waits for the screen to show it, so no sleeps are
needed.

A few things worth knowing:

- On an iPhone with a keyboard attached, `Meta+h` is Home, `Meta+Space`
  opens Search and `Shift` wakes the screen. `vibium keys` sends one key
  or chord per call.
- `vibium type` with only the text types into whatever has focus. That
  form needs a Vibium newer than 26.8.21, because older ones insist on a
  selector; with those, send the letters one at a time with
  `vibium keys`.
- When only a finger will do, `vibium tap X Y` taps the picture at those
  pixels. Take a screenshot first and read the coordinates off it,
  because they are pixels of the screenshot and move if you change
  Roadie's crop.

## 3. The same from an agent

Vibium's MCP server gives Claude Code, Codex or any MCP client the same
session. Add it with the two variables, so the agent's browser tools
reach the phone instead of a local browser:

```bash
claude mcp add vibium \
  -e VIBIUM_CONNECT_URL=ws://roadie.local:8080/session \
  -e VIBIUM_CONNECT_API_KEY=<token> \
  -- npx -y vibium mcp
```

Then ask for what you want in words. A phone has no DOM, so only a few
of the tools matter:

- `browser_start` and `browser_screenshot`.
- `browser_keys` for a key or a chord, and `browser_type` with `text`
  alone to type into whatever has focus.
- `browser_tap` and `browser_long_press` for what only a finger reaches.
- `browser_record_start` and `browser_record_stop`.

A prompt like this works:

> Start a recording to calc.zip. Take a screenshot of the phone. Open
> Search with Meta+Space, type calc and press Enter to launch the
> Calculator. Take another screenshot. Press Meta+h to go home and stop
> the recording.

The agent sees each screenshot, so it can check that the Calculator came
up before it moves on.

## 4. The same from a script

JavaScript (`npm install vibium`), as `calc.mjs`:

```js
import { browser } from 'vibium';
import { writeFileSync } from 'node:fs';

const bro = await browser.start(process.env.VIBIUM_CONNECT_URL, {
  headers: { Authorization: 'Bearer ' + process.env.VIBIUM_CONNECT_API_KEY },
});
try {
  const page = await bro.page();
  await page.keyboard.press('Meta+h');
  await page.context.recording.start({ path: 'calc.zip' });
  writeFileSync('home.jpg', await page.screenshot());
  await page.keyboard.press('Meta+Space');
  await page.keyboard.type('calc');
  await page.keyboard.press('Enter');
  await new Promise((r) => setTimeout(r, 2000));
  writeFileSync('calc.jpg', await page.screenshot());
  await page.keyboard.press('Meta+h');
  console.log(await page.context.recording.stop());
} finally {
  await bro.stop();
}
```

Python (`pip install vibium`), as `calc.py`:

```python
import os, time
from vibium import browser

bro = browser.start(os.environ["VIBIUM_CONNECT_URL"],
                    headers={"Authorization": "Bearer " + os.environ["VIBIUM_CONNECT_API_KEY"]})
try:
    page = bro.page()
    page.keyboard.press("Meta+h")
    page.context.recording.start(path="calc.zip")
    open("home.jpg", "wb").write(page.screenshot())
    page.keyboard.press("Meta+Space")
    page.keyboard.type("calc")
    page.keyboard.press("Enter")
    time.sleep(2)
    open("calc.jpg", "wb").write(page.screenshot())
    page.keyboard.press("Meta+h")
    print(page.context.recording.stop())
finally:
    bro.stop()
```

Run them with `node calc.mjs` and `python calc.py`. Both read the URL and
the token from the environment and pass them to `start()`, and both run
the Vibium engine on your machine, where it holds the socket to the
phone. The two-second pause gives the Calculator time to open before the
second screenshot, and `keyboard.type` sends the word as key presses,
one character at a time.

## The recording

`calc.zip` is a Vibium recording. Open it in the
[Vibium Record Player](https://player.vibium.dev) by dropping the file
on the page, and step through it there; the format is also compatible
with Playwright traces, so `npx playwright show-trace calc.zip` opens it
too.

Inside, `trace.trace` lists each action with its timing, and `resources/`
holds a JPEG of the phone's screen for every step. `trace.network` is
empty, since a phone makes no page requests the engine can see. There is
no video, because the engine records video on the machine the browser
runs on, and here that is the Roadie, so the screenshots are the record.

The scripts above give a recording of six steps, and the CLI gives
eleven, because it records its page lookup before every command as a
step of its own. All of them hold the same two pictures: the home screen
before the search and the Calculator after it.

## Driving the viewer

Vibium can also launch a browser of its own, open Roadie's `/view` page
and work it like any web page, with the viewer passing the mouse and
keyboard on to the phone and screenshots showing the page. This is the
way to reach several phones from one script or to test the viewer
itself; for one phone, the direct connection above is simpler.

The picture sits at the page's top-left at full size, so coordinates are
the same as in a Roadie screenshot:

```javascript
import { browser } from 'vibium'

const bro = await browser.start()                      // launches a browser
const page = await bro.page()
await page.setViewport({ width: 1000, height: 1100 })  // room for the whole picture
await page.go('http://roadie.local:8080/view')

// AC 1 + 2 = on the Calculator, already open
for (const [x, y] of [[174, 454], [70, 756], [380, 756], [174, 756], [380, 860]]) {
  await page.mouse.click(x, y)
}
await bro.stop()
```

The page's screenshots do not wait for the phone to show your last
click, the way a screenshot over the direct connection does.

To drive several phones from one script, open the
[grid](../how-to/multiple-targets.md#automating-several-targets) instead
of `/view`: the mouse and keyboard reach whichever phone is under the
pointer.

## When something is off

- `failed to connect`: the laptop cannot reach Roadie. Check the address
  and the port, and check that Roadie runs with `--auth token`, because
  without it only the Pi itself may connect.
- `session not created: a session is already active`: another BiDi
  client has the phone. Wait for it, or stop it.
- `session not created: held by token:ci, place 1 in line` or `this
  token may only view`: a station's answer; see
  [its tutorial](https://github.com/VibiumDev/station/blob/main/docs/tutorials/agent-through-the-station.md).
- The socket closes while a script runs: five minutes passed without a
  command, so Roadie ended the session. Through a station it can also be
  a person taking the phone at the viewer, or the eight-hour maximum on
  a hold. Start again.
- `Meta+Space` opens nothing: the phone is asleep or locked. `Shift`
  wakes it, but unlocking is something you do once by hand, and `Meta+h`
  first puts it on the home screen, which is where Search opens.
- Enter opened the wrong thing: the top hit follows what the phone was
  used for lately. Type the whole name, `calculator`.
- Taps land somewhere else: coordinates are pixels of the screenshot
  rather than of the phone's screen, and they move when Roadie's crop
  changes, so take a fresh screenshot and read them off it.

## Next steps

- [Roadie API Reference](../../API.md): the BiDi methods Roadie answers,
  HID keycodes and the REST endpoints.
- [Reach a device remotely through the station](https://github.com/VibiumDev/station/blob/main/docs/tutorials/agent-through-the-station.md):
  tokens, holds and the queue when the phone is behind a station.
- [Vibium's documentation](https://github.com/VibiumDev/vibium/tree/main/docs):
  the CLI, the MCP server and the clients in full.
