# How to Flash the Boards

## Prerequisites

- Python 3.10+
- Both QT Py RP2040 boards and USB-C data cables

Run the one-time setup first:

```bash
make setup
```

This creates a Python virtualenv and installs dependencies. On Linux, it also installs udev rules for serial port access.

## Flash the HID Board

Only one board can be flashed at a time.

1. Plug in the QT Py that will be the HID board (📤 OUT).
2. Run:
   ```bash
   make flash-hid
   ```
3. The script will guide you through any manual steps (holding BOOT button if needed).
4. When done, the NeoPixel should glow **green**.
5. Unplug it.

## Flash the Relay Board

1. Plug in the second QT Py.
2. Run:
   ```bash
   make flash-relay
   ```
3. When done, the NeoPixel should glow **red**.

## Re-Flashing (Code Only)

To update just the Python code without re-installing CircuitPython firmware:

```bash
make flash-hid-quick
make flash-relay-quick
```

## Syncing Files

To copy updated code to already-flashed boards without any firmware steps:

```bash
make sync            # sync both boards
make sync-hid        # sync HID board only
make sync-relay      # sync relay board only
```

## What install.py Does

The `board/install.py` script handles the full provisioning workflow:

1. Downloads CircuitPython UF2 firmware (cached in `~/.cache/roadie/`)
2. Detects the board state (bootloader, CircuitPython, or raw)
3. Installs CircuitPython firmware if needed
4. Cleans user files from the volume (preserves `boot_out.txt`, `settings.toml`, `lib/`)
5. Copies the appropriate board files (`boot.py`, `code.py`, libraries)
6. Copies shared protocol file to `lib/`
7. Ejects the drive

### Platform-Specific Paths

| | macOS | Linux |
|---|---|---|
| CIRCUITPY mount | `/Volumes/CIRCUITPY` | `/media/$USER/CIRCUITPY` |
| Bootloader mount | `/Volumes/RPI-RP2` | `/media/$USER/RPI-RP2` |
| Serial port glob | `/dev/tty.usbmodem*` | `/dev/ttyACM*` |

## After Flashing

After flashing, always **unplug and re-plug** the board. The custom USB HID descriptors in `boot.py` only take effect after a full USB re-enumeration.

## Verifying the Flash

Connect to the board's serial console to check for errors:

```bash
# Linux
screen /dev/ttyACM0 115200

# macOS
screen /dev/tty.usbmodem* 115200
```

You should see the startup message:
- HID board: `roadie hid board ready (uart + hid)`
- Relay board: `roadie relay board ready (uart + serial)`

Press `Ctrl-A` then `K` to exit screen.
