# Demo GIF Plan

Hero image/gif for the README to show Roadie working at a glance.

## What to capture

A short screen recording of the `/view` page in a browser, with the physical phone visible next to the screen. The browser shows the phone's display mirrored, and the user interacts with it — moving the cursor, tapping, maybe swiping — and the phone responds in real time.

## The "oh, I get it" moment

The phone screen and the browser showing the **same thing**, with input going through the browser and the phone responding. That's the whole pitch in one image.

## Shot options

### Option A: Photo (simplest)

- Browser window with `/view` open, showing the phone's screen
- Phone physically next to the computer, plugged in via USB-C dongle, showing the same screen
- The two QT Py boards and HDMI capture dongle visible in the cable run (if it doesn't look cluttered)
- Slightly overhead/angled so you can see phone, cables, boards, and browser in one frame
- Clean desk background

### Option B: GIF (more impressive)

- 5–10 second loop
- Show a cursor moving in the browser → phone responding
- Or a swipe gesture in the browser → phone scrolling
- Keep it tight — crop to just the browser window + phone, no desktop chrome

### Option C: Both

- Photo as the hero (loads instantly, works everywhere)
- GIF below it or in a "Demo" section

## What to show on the phone screen

Something recognizable:
- Home screen (universally understood)
- Setup wizard / "Select Your Language" (ties to provisioning use case)
- A settings menu being scrolled (shows touch working)

## Filming tips

- Use a clean/dark desk background
- Make sure the browser and phone are close together in frame so the viewer's eye connects them
- If recording a GIF, use a tool like `gifski` or `ffmpeg` to keep file size reasonable (<5MB for GitHub)
- Record at 1x or 2x, not faster — people need to see the cause-and-effect between browser input and phone response

## README placement

```markdown
# 🚐 Roadie

USB KVM (Keyboard, Video, Mouse + Multi-touch) controllable over HTTP.

<!-- hero image/gif here -->
![Roadie controlling a phone from a browser](docs/assets/demo.gif)

Roadie turns a cheap HDMI capture dongle...
```
