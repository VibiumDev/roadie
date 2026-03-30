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
