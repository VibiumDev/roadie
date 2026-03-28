# Roadie Phase 2: Board Provisioning ✅

## Overview

Roadie uses two Adafruit QT Py RP2040 boards connected via UART:
- **relay** (labeled 📥 IN, red NeoPixel): plugged into the host, receives JSON commands over USB serial, forwards them over UART to the HID board
- **hid** (labeled 📤 OUT, green NeoPixel): plugged into a target device, receives UART commands, sends USB HID mouse/keyboard input

Both boards run CircuitPython 10.1.3 and are provisioned via `board/install.py`.

---

## 2.1 Board Files

### board/shared/protocol.py
Shared protocol constants and helpers used by both boards. Defines:
- 32-byte binary message format (cmd, seq, payload length, payload)
- Command types (PING, KEY_PRESS, KEY_TYPE, MOUSE_MOVE, etc.)
- Status codes (OK, ERR, BUSY)
- `pack_msg()`, `unpack_msg()`, `pack_resp()` helpers

### board/hid/boot.py
```python
import storage
storage.getmount("/").label = "ROADIE_HID"

import usb_hid
from absolute_mouse.descriptor import device
usb_hid.enable((usb_hid.Device.KEYBOARD, device))
```

### board/hid/code.py
- UART receiver on TX/RX pins (D6/D7) at 115200 baud
- Buffers incoming bytes until a full 32-byte message is received
- Sends 2-byte response (status + echo seq) immediately after processing
- Blinks green NeoPixel on each ping received

### board/hid/requirements.txt
```
absolute_mouse
adafruit_hid
```

### board/relay/boot.py
```python
# no custom HID descriptors needed for relay board
import storage
storage.getmount("/").label = "ROADIE_RLY"

import usb_cdc
usb_cdc.enable(console=True, data=True)
```
The `usb_cdc` data port provides a separate serial channel for commands from the host, keeping the REPL available for debugging.

### board/relay/code.py
- UART sender on TX/RX pins (D6/D7) at 115200 baud
- Sends PING every 1 second, reads 2-byte response
- Flushes stale UART bytes before each send
- Blinks red NeoPixel on successful pong

### board/relay/requirements.txt
```
adafruit_hid
```

### Visual confirmation

After flashing and connecting UART wires, each board blinks its NeoPixel once per second:
- **hid** (📤 OUT): green blink on each ping received
- **relay** (📥 IN): red blink on each successful pong

Connect to the serial REPL to see messages:
- Linux: `screen /dev/ttyACM0 115200`
- macOS: `screen /dev/tty.usbmodem* 115200`

---

## 2.2 install.py (Cross-Platform)

### Platform-specific constants

| | macOS | Linux |
|---|---|---|
| **CIRCUITPY mount** | `/Volumes/CIRCUITPY` | `/media/$USER/CIRCUITPY` |
| **Bootloader mount** | `/Volumes/RPI-RP2` | `/media/$USER/RPI-RP2` |
| **Serial port glob** | `/dev/tty.usbmodem*` | `/dev/ttyACM*` |
| **Eject command** | `diskutil eject <path>` | `udisksctl unmount -b <device>` |

### Key behaviors
- Accepts `relay` or `hid` as the board argument
- `--setup-only` flag: only creates venv + installs deps, then exits
- `--skip-firmware` flag: skips CircuitPython firmware install
- `--cp-version VERSION` flag: override CircuitPython version (default: 10.1.3)
- Creates `.venv/` at the repo root
- Downloads CircuitPython UF2 to `~/.cache/roadie/` (cached across runs)
- Detects board state: RPI-RP2 (bootloader), CIRCUITPY/ROADIE_RLY/ROADIE_HID (has CP), or neither (raw)
- Board-aware volume detection: when both boards are plugged in, prefers the target board's named volume
- Cleans all user files from the volume before copying (preserves `boot_out.txt`, `sd/`, `settings.toml`, `lib/` directory)
- 1-second settle delay after volume mounts (prevents FAT filesystem permission errors on Linux)
- Ejects the drive when done, shows expected volume name and label

### Makefile targets
```makefile
make setup              # python3 board/install.py --setup-only
make flash-relay        # Full flash of relay board
make flash-hid          # Full flash of HID board
make flash-relay-quick  # Code-only update (skip firmware)
make flash-hid-quick    # Code-only update (skip firmware)
```

---

## 2.3 Wiring

### Parts needed
- 2x Adafruit QT Py RP2040
- 2x USB-C data cables (not charge-only)
- 3x jumper wires (for UART + GND between boards)
- 1x breadboard (optional, for development)

### UART connections

```
Relay TX (D6)  →  HID RX (D7)
Relay RX (D7)  ←  HID TX (D6)
Relay GND      ─  HID GND
```

### Why UART instead of I2C/STEMMA QT
The QT Py RP2040 does not have on-board pull-up resistors on the STEMMA QT I2C pins (Adafruit puts pull-ups on breakout boards, not MCU boards). UART requires no pull-ups and only 3 wires. See Phase 3 for the protocol details.

---

## 2.4 Volume Rename ✅

Each board's `boot.py` renames its CIRCUITPY volume on boot:
- **relay** → `ROADIE_RLY` (11-char FAT label limit)
- **hid** → `ROADIE_HID`

This enables both boards to be plugged into the same host simultaneously.

---

## 2.5 Troubleshooting

- **"CIRCUITPY not mounted"**: make sure you're using a USB data cable, not a charge-only cable. On Linux, ensure the volume auto-mounts (install `udisks2` if needed).
- **"No serial port found"**: the board may not have CircuitPython installed yet. The script will fall back to manual bootloader entry. On Linux, make sure your user is in the `dialout` group.
- **"Buffer incorrect size"**: unplug and re-plug after flashing. The board needs a full USB re-enumeration for the custom HID descriptor in `boot.py` to take effect.
- **NeoPixel doesn't blink after flashing**: connect to the serial REPL to check for errors. Press Ctrl-C to interrupt, then Ctrl-D to soft-reboot.
- **Read-only filesystem errors during flash**: the board's FAT filesystem went read-only (usually from a crash). Unmount, unplug, re-plug, and retry.
