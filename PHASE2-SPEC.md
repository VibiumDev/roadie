# Roadie Phase 2: Board Provisioning

## Overview

Roadie uses two Adafruit QT Py RP2040 boards connected via I2C:
- **relay** (labeled 📥 IN): plugged into the host, receives JSON commands over serial, forwards them over I2C
- **hid** (labeled 📤 OUT): plugged into a target device, receives I2C commands, sends USB HID mouse/keyboard input

This phase adds the provisioning script (`board/install.py`) and all board-specific files. It assumes the directory structure from Phase 1 is already in place (the `board/relay/`, `board/hid/`, and `board/shared/` directories exist but are empty).

---

## 2.1 Board Files

### board/hid/boot.py
```python
import usb_hid
from absolute_mouse.descriptor import device
usb_hid.enable((usb_hid.Device.KEYBOARD, device))
```

### board/hid/requirements.txt
```
absolute_mouse
adafruit_hid
```

### board/hid/code.py
```python
# roadie hid board — receives I2C commands, sends USB HID
# TODO: implement I2C target + HID output
import time
import board
import digitalio
import neopixel_write

# QT Py RP2040 requires powering the NeoPixel
power = digitalio.DigitalInOut(board.NEOPIXEL_POWER)
power.direction = digitalio.Direction.OUTPUT
power.value = True

pixel_pin = digitalio.DigitalInOut(board.NEOPIXEL)
pixel_pin.direction = digitalio.Direction.OUTPUT

GREEN = bytearray([255, 0, 0])  # GRB order
OFF = bytearray([0, 0, 0])

print("roadie hid board ready")
while True:
    neopixel_write.neopixel_write(pixel_pin, GREEN)
    print("hid: alive")
    time.sleep(1)
    neopixel_write.neopixel_write(pixel_pin, OFF)
    time.sleep(1)
```

### board/relay/boot.py
```python
# no custom HID descriptors needed for relay board
```

### board/relay/requirements.txt
```
adafruit_hid
```

### board/relay/code.py
```python
# roadie relay board — receives serial JSON, forwards over I2C
# TODO: implement serial listener + I2C controller
import time
import board
import digitalio
import neopixel_write

# QT Py RP2040 requires powering the NeoPixel
power = digitalio.DigitalInOut(board.NEOPIXEL_POWER)
power.direction = digitalio.Direction.OUTPUT
power.value = True

pixel_pin = digitalio.DigitalInOut(board.NEOPIXEL)
pixel_pin.direction = digitalio.Direction.OUTPUT

BLUE = bytearray([0, 0, 255])  # GRB order
OFF = bytearray([0, 0, 0])

print("roadie relay board ready")
while True:
    neopixel_write.neopixel_write(pixel_pin, BLUE)
    print("relay: alive")
    time.sleep(1)
    neopixel_write.neopixel_write(pixel_pin, OFF)
    time.sleep(1)
```

### board/shared/i2c_protocol.py
```python
# shared I2C protocol constants and helpers
# TODO: define message format
```

### Visual confirmation

After flashing, each board blinks its NeoPixel:
- **hid**: green blink
- **relay**: blue blink

Connect to the serial REPL to see the "alive" messages:
- macOS: `screen /dev/tty.usbmodem* 115200`
- Linux: `screen /dev/ttyACM0 115200`

---

## 2.2 install.py (Cross-Platform)

Place at `board/install.py`. The script is provided as a separate file in this handoff. Make it executable (`chmod +x`).

The script must detect the host platform and use the correct values:

### Platform-specific constants

| | macOS | Linux |
|---|---|---|
| **CIRCUITPY mount** | `/Volumes/CIRCUITPY` | `/media/$USER/CIRCUITPY` |
| **Bootloader mount** | `/Volumes/RPI-RP2` | `/media/$USER/RPI-RP2` |
| **Serial port glob** | `/dev/tty.usbmodem*` | `/dev/ttyACM*` |
| **Eject command** | `diskutil eject <path>` | `udisksctl unmount -b <device>` (or `sync` + `umount`) |

The script detects the platform at startup (`sys.platform`) and sets these values accordingly.

### Key behaviors
- Accepts `relay` or `hid` as the board argument
- `--setup-only` flag: only creates venv + installs deps, then exits
- `--skip-firmware` flag: skips CircuitPython firmware install
- `--cp-version VERSION` flag: override CircuitPython version (default: 10.1.3)
- Creates `.venv/` at the repo root (two levels up from `board/install.py`)
- Downloads CircuitPython UF2 to `~/.cache/roadie/` (cached across runs)
- Detects board state: RPI-RP2 (bootloader), CIRCUITPY (has CP), or neither (raw)
- If CIRCUITPY is mounted, attempts to reboot into bootloader via serial REPL
- If neither is mounted, prints manual instructions (hold BOOT + plug in) and waits
- Uses `circup` (with `--path` flag) to install libraries from each board's `requirements.txt`
- Copies `*.py` files from the board directory to CIRCUITPY root
- Copies `shared/*.py` to CIRCUITPY `lib/`
- Ejects the drive when done

### Linux-specific notes
- The user may need to be in the `dialout` group for serial access: `sudo usermod -aG dialout $USER`
- CircuitPython volumes auto-mount via udisks on most desktop Linux setups. On a headless or minimal Linux (including some VMs), you may need to install `udisks2` or mount manually.
- On a Linux VM on an Apple Silicon Mac, USB passthrough must be available. This works with UTM/QEMU but not with Apple's Virtualization.framework.

---

## 2.3 README.md Updates

After Phase 2, add the board flashing workflow to `README.md`:

### Parts needed
- 2x Adafruit QT Py RP2040
- 2x USB-C data cables (not charge-only)
- 1x STEMMA QT / Qwiic I2C cable (for connecting the two boards)
- (Optional) 3D-printed enclosure

### Prerequisites
- macOS or Linux (Raspberry Pi OS, Ubuntu, etc.)
- Go 1.21+
- Python 3.10+

### Setup

```bash
git clone <repo-url> && cd roadie
make setup
```

### Flash the boards

Only one board can be flashed at a time.

1. Plug in the first QT Py (this will be the **📤 OUT / HID** board):
   ```bash
   make flash-hid
   ```
   The script will guide you through any manual steps (holding BOOT button, etc.)

2. Once flashing completes, the board's NeoPixel should blink **green**. That means it worked.

3. Unplug it. Label it **📤 OUT**.

4. Plug in the second QT Py:
   ```bash
   make flash-relay
   ```

5. The NeoPixel should blink **blue**. Label it **📥 IN**.

### Connect and run

1. Connect the two boards with the STEMMA QT cable
2. Plug **📥 IN** into your host machine
3. Plug **📤 OUT** into the target device
4. Build and run:
   ```bash
   make
   make run
   ```

### Re-flashing

If you only need to update the Python code (not the CircuitPython firmware):
```bash
make flash-hid-quick
make flash-relay-quick
```

### Troubleshooting

- **"CIRCUITPY not mounted"**: make sure you're using a USB data cable, not a charge-only cable. On Linux, ensure the volume auto-mounts (install `udisks2` if needed).
- **"No serial port found"**: the board may not have CircuitPython installed yet. The script will fall back to manual bootloader entry. On Linux, make sure your user is in the `dialout` group.
- **"Buffer incorrect size"**: you forgot to unplug and re-plug after flashing. The board needs a full USB re-enumeration for the custom HID descriptor in boot.py to take effect.
- **NeoPixel doesn't blink after flashing**: connect to the serial REPL to check for errors. Press Ctrl-C to interrupt, then Ctrl-D to soft-reboot.

---

## 2.4 Future: Volume Rename & Integration Testing

**Not to be implemented in this handoff**, but noted here for context.

Each board's `boot.py` will eventually rename its CIRCUITPY volume (via `storage.getmount("/").label`) to `ROADIE_RELAY` or `ROADIE_HID`. This enables:

1. **Both boards plugged into the same host simultaneously** — distinct mount names avoid conflicts.
2. **End-to-end integration testing** — with both boards on the same host, the Go test suite can send a command through the full pipeline (serial → relay → I2C → hid → USB HID keystroke) and verify the HID event arrives back on the host, completing the loop.

The install script will need to be updated to look for the renamed volumes in addition to `CIRCUITPY`.

---

## What NOT to implement

- The actual I2C protocol, serial JSON parsing, or HID command logic. Those are future work. Board `code.py` files are hello-world placeholders (NeoPixel blink + serial print).
- The volume rename described in 2.4.
- The end-to-end integration test.
- The Go `make run` target. The Go app already has its own entry point.
- The 3D-printed enclosure files.
- Any changes to the Go source code.
