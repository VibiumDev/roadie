# Board Reference

Hardware specifications for the two Adafruit QT Py RP2040 boards used by Roadie.

## Board Roles

| Board | Label | USB Product Name | Volume Name | NeoPixel | Connects To |
|-------|-------|-----------------|-------------|----------|-------------|
| Relay | 📥 IN | Roadie-Relay | ROADIE_RLY | Red | Host (Pi, Mac, Linux) |
| HID | 📤 OUT | Roadie-HID | ROADIE_HID | Green | Target device |

## UART Wiring

Three jumper wires between the boards:

```
Relay TX (D6)  →  HID RX (D7)
Relay RX (D7)  ←  HID TX (D6)
Relay GND      ─  HID GND
```

The GND wire is required whenever the boards are powered from different USB hosts (e.g., relay on Pi, HID on Mac). Without common ground, UART signals are meaningless and the HID board reads noise.

### Why UART instead of I2C/STEMMA QT

The QT Py RP2040 does not have on-board pull-up resistors on the STEMMA QT I2C pins. UART requires no pull-ups and only 3 wires.

## USB Serial Ports

### Relay Board

The relay exposes two USB CDC serial ports:

| Port | Interface | Purpose |
|------|-----------|---------|
| Console | CDC 0 | CircuitPython REPL (debugging) |
| Data | CDC 2 | JSON commands from the Go server |

On Linux: `/dev/serial/by-id/usb-Adafruit_Roadie-Relay_*-if00` (console) and `-if02` (data).

On macOS: `/dev/cu.usbmodemXXX1` (console) and `/dev/cu.usbmodemXXX3` (data), identified via `ioreg -n Roadie-Relay`.

### HID Board

The HID board exposes one USB CDC serial port (console only) plus three HID devices:

| Device | Type | Description |
|--------|------|-------------|
| Keyboard | Standard USB HID | `adafruit_hid` Keyboard + KeyboardLayoutUS |
| Mouse | Custom HID descriptor | Absolute positioning (0-32767), 8 buttons, wheel, h-pan |
| Digitizer | Custom HID descriptor | Multi-touch (2 contacts), absolute positioning |

## boot.py Configuration

### Relay

```python
import supervisor
supervisor.set_usb_identification(product="Roadie-Relay")

import storage
storage.getmount("/").label = "ROADIE_RLY"

import usb_cdc
usb_cdc.enable(console=True, data=True)
```

### HID

```python
import supervisor
supervisor.set_usb_identification(product="Roadie-HID")

import storage
storage.getmount("/").label = "ROADIE_HID"

import usb_hid
from absolute_mouse.descriptor import device as mouse_device
from digitizer import device as digitizer_device
usb_hid.enable((usb_hid.Device.KEYBOARD, mouse_device, digitizer_device))
```

## CircuitPython Libraries

### Relay

- `adafruit_hid` (bundled with CircuitPython)

### HID

- `adafruit_hid` — keyboard and keyboard layout
- `absolute_mouse` — custom absolute mouse HID device
- `digitizer.py` — custom multi-touch digitizer (in-repo, `board/hid/digitizer.py`)
- `mouse_device.py` — custom mouse device (in-repo, `board/hid/mouse_device.py`)

## NeoPixel LED Behavior

### Relay (Red)

| State | LED |
|-------|-----|
| Startup | Solid red |
| Successful pong from HID board | Blink (off 100ms, then red) |
| No response from HID board | Off |

### HID (Green)

| State | LED |
|-------|-----|
| Startup | Solid green |
| Command received (low-frequency) | Blink (off 100ms, then green) |
| High-frequency commands (mouse_move, key_press/release, scroll, touch) | No blink (avoids back-pressure) |

## Identifying Boards

### Linux

```bash
ls -la /dev/serial/by-id/ | grep Roadie
```

Or:

```bash
for dev in /dev/ttyACM*; do
  echo "=== $dev ==="
  udevadm info --query=property --name="$dev" | grep ID_MODEL=
done
```

### macOS

```bash
ioreg -n Roadie-Relay -r -l | grep IOCalloutDevice
ioreg -n Roadie-HID -r -l | grep IOCalloutDevice
```

## Source Files

| File | Description |
|------|-------------|
| `board/hid/boot.py` | HID board USB/storage config |
| `board/hid/code.py` | HID board main loop |
| `board/hid/mouse_device.py` | Custom absolute mouse HID descriptor and driver |
| `board/hid/digitizer.py` | Custom multi-touch digitizer HID descriptor and driver |
| `board/relay/boot.py` | Relay board USB/storage config |
| `board/relay/code.py` | Relay board main loop |
| `board/shared/protocol.py` | Shared protocol (copied to both boards) |
| `board/install.py` | Board provisioning script |
