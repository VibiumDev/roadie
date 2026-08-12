# Troubleshooting

## Board Flashing

**"CIRCUITPY not mounted"**
Make sure you're using a USB data cable, not a charge-only cable. On Linux, ensure the volume auto-mounts (install `udisks2` if needed).

**"No serial port found"**
The board may not have CircuitPython installed yet. The script will fall back to manual bootloader entry. On Linux, make sure your user is in the `dialout` group:
```bash
sudo usermod -a -G dialout $USER
# Log out and back in
```

**"Buffer incorrect size"**
Unplug and re-plug after flashing. The board needs a full USB re-enumeration for the custom HID descriptor in `boot.py` to take effect.

**NeoPixel doesn't glow after flashing**
Connect to the serial REPL to check for errors. Press `Ctrl-C` to interrupt, then `Ctrl-D` to soft-reboot.

**Read-only filesystem errors during flash**
The board's FAT filesystem went read-only (usually from a crash). Unmount, unplug, re-plug, and retry.

## Serial Connection

**"Permission denied" on /dev/ttyACM***
```bash
sudo usermod -a -G dialout $USER
# Log out and back in
```

**screen says "[screen is terminating]" immediately**
The device might be in use by another process (like the roadie server):
```bash
fuser /dev/ttyACM0
```

**REPL shows garbled text**
Make sure you're connecting at 115200 baud (the CircuitPython console default), not the UART baud rate (921600).

## UART Communication

**HID board printing "unknown cmd: 0x00" repeatedly**
The UART RX pin is reading noise. Most common causes:
- GND wire not connected between the boards
- Boards powered from different USB hosts without common ground (e.g., relay on Pi, HID on Mac)
- Jumper wires disconnected or loose

Fix: ensure TX, RX, **and GND** wires are connected. When boards are on different hosts, the GND wire is essential.

**HID board LED blinks but commands don't work on target**
- Check the serial console for error messages
- Verify the HID board is plugged into the target (not the host)
- Try a REPL test: interrupt `code.py` with Ctrl-C, then manually run `mouse.move(16383, 16383)` to confirm HID works

## Relay Detection

**"HID relay not found: no relay data port found"**
The Go server can't find the relay board's serial port.

On Linux:
```bash
ls /dev/serial/by-id/ | grep Roadie
```

On macOS:
```bash
ioreg -n Roadie-Relay -r -l | grep IOCalloutDevice
```

If no results, the relay board isn't plugged in or isn't running CircuitPython with the correct `boot.py`.

**"N relay boards connected, use --relay to pick one"**
More than one relay board is plugged in. Roadie won't guess, because two instances
sharing one board would interleave their input. List the boards and pin each
instance to one:
```bash
./roadie --list-devices
./roadie --relay A1B2C3D4E5F60001 ...
```

**"relay selector ... is ambiguous"**
The `--relay` substring matches more than one board. Serials from the same batch
share a long prefix — use more characters, or a distinctive suffix such as
`--relay 0002`.

**"no relay board matching ..."**
The `--relay` substring matches nothing. The error lists the connected boards; check
for a typo. Video capture keeps running meanwhile — only HID is unavailable.

## Wall Page

**Typing does nothing on `/wall`**
Keyboard input reaches the panel that holds focus, and the outlined panel shows
which one that is. Move the pointer over a panel to give it focus, then type.
If no panel is outlined, the browser window itself may not be focused — click
the page once.

## Stuck Keys

**Everything types as the wrong character (`1234` comes out as `¡™£¢`)**
A modifier key is held down on the target. Those characters are Option+1234, so
something is holding Option; Shift stuck instead would give `!@#$`.

The viewer releases held keys when it loses focus, so this should not accumulate,
but a target can also latch a modifier on its own side. To clear it, tap that
modifier once on your keyboard with the viewer focused — a press *and* release,
which a bare release cannot substitute for, since a release that does not change
the report is dropped before it reaches the target.

Resetting the HID board also clears it, by re-enumerating the keyboard:
```bash
curl -X POST http://localhost:8080/api/hid/reset
```
That is the heavier option; the modifier tap is instant and does not interrupt
the session.

## Device Reset

**Glitchy HDMI signal (wrong colors, static on first connection)**
Use the Video Reset button in the settings panel, or:
```bash
curl -X POST http://localhost:8080/api/capture/reset
```
This performs a USB unbind/rebind on the capture dongle, forcing HDMI re-negotiation. Requires `make setup` to install the udev rule.

**HID or relay board unresponsive**
Reset the boards from the settings panel, or:
```bash
curl -X POST http://localhost:8080/api/hid/reset    # reset HID board
curl -X POST http://localhost:8080/api/relay/reset   # reset relay board
```

**"permission denied" on capture reset**
Run `make setup` to install the udev rule that grants access to USB unbind/rebind. You may need to unplug/replug the capture dongle once after installing the rule.

## Video Capture

**"No capture device found"**
Plug in an HDMI-to-USB capture dongle. Roadie will detect it automatically in the background.

**Stream shows black screen**
The target device may not be outputting video. Check the HDMI connection and ensure the target is powered on. The `/health` endpoint will show `"status": "no_signal"` when connected but receiving no signal.

**Stream shows wrong resolution or stretched image**
Adjust the capture resolution via the settings panel in `/view`, or use CLI flags:
```bash
./roadie --width 1280 --height 720
```
