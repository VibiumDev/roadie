# Connecting to Roadie Board REPLs

Quick reference for interactive debugging with `screen` and the CircuitPython REPL.

## Identifying which device is which

Each board sets a custom USB product name in `boot.py`:

- **Relay board** &rarr; `Roadie-Relay`
- **HID board** &rarr; `Roadie-HID`

### Option 1: Check with udevadm

```bash
# Run this for each device
for dev in /dev/ttyACM0 /dev/ttyACM1 /dev/ttyACM2; do
  echo "=== $dev ==="
  udevadm info --query=property --name="$dev" 2>/dev/null | grep -E 'ID_MODEL=|ID_SERIAL='
done
```

Look for `ID_MODEL=Roadie-Relay` or `ID_MODEL=Roadie-HID`.

### Option 2: Check with dmesg

```bash
dmesg | grep -i "roadie\|ttyACM"
```

### Option 3: Check by-id symlinks

```bash
ls -la /dev/serial/by-id/
```

Look for symlinks containing `Roadie-Relay` or `Roadie-HID`.

### Which port is the REPL?

The **HID board** exposes only **one** serial port (its REPL console). The **relay board** exposes **two** -- a REPL console and a data port (where the Go server sends JSON commands). The REPL is always the first port for a given board.

Typical layout:

| Device | Board | Port |
|---|---|---|
| `/dev/ttyACM0` | HID | REPL console |
| `/dev/ttyACM1` | Relay | REPL console |
| `/dev/ttyACM2` | Relay | Data port (JSON commands) |

The exact numbering depends on plug order -- always verify with the `udevadm` command above.

## Connecting with screen

```bash
# Connect to a board REPL (115200 is the default CircuitPython console baud)
screen /dev/ttyACM1 115200
```

**Key shortcuts inside screen:**

| Action | Keys |
|---|---|
| Detach (leave session running) | `Ctrl-A` then `D` |
| Reattach to detached session | `screen -r` |
| Quit/kill session | `Ctrl-A` then `K` |


**Getting to the REPL prompt:**

Once connected, press `Ctrl-C` to interrupt the running `code.py` and drop into the `>>>` Python REPL. Press `Ctrl-D` to soft-reload and restart `code.py`.

> **Warning:** While you're in the REPL (after Ctrl-C), the board's main loop is stopped. The relay won't forward commands and the HID board won't process them. Press Ctrl-D to resume normal operation when done.

## Connecting to both boards at once

Open two terminals (or use tmux/screen split):

```bash
# Terminal 1 - HID board REPL
screen /dev/ttyACM0 115200

# Terminal 2 - Relay board REPL
screen /dev/ttyACM1 115200
```

Adjust device numbers based on what you found in the identification step (they depend on plug order).

## Things to try on the HID board

Once at the `>>>` prompt (after pressing Ctrl-C), paste this setup block:

```python
import usb_hid
from adafruit_hid.keyboard import Keyboard
from adafruit_hid.keyboard_layout_us import KeyboardLayoutUS
from adafruit_hid.keycode import Keycode
from absolute_mouse import Mouse as AbsoluteMouse
from digitizer import Digitizer
import board, digitalio, neopixel_write, time

kbd = Keyboard(usb_hid.devices)
layout = KeyboardLayoutUS(kbd)
mouse = AbsoluteMouse(usb_hid.devices)
digi = Digitizer(usb_hid.devices)

def type_text(text):
    layout.write(text)

def key(*keycodes):
    """Press + release in one call. e.g. key(Keycode.CONTROL, Keycode.A)"""
    kbd.send(*keycodes)

def mouse_move(x, y):
    """Move mouse to absolute coords (0-32767)."""
    mouse.move(x, y)

def mouse_click(buttons=1):
    """Click. 1=left, 2=right, 4=middle."""
    mouse.click(buttons)

def scroll(amount):
    """Scroll wheel. Positive=down, negative=up."""
    mouse.move(wheel=amount)

def touch(x, y):
    """Tap at absolute coords (0-32767)."""
    digi.touch([(0, 1, x, y)])   # finger down
    digi.touch([(0, 0, x, y)])   # finger up

def swipe(x1, y1, x2, y2, steps=10):
    """Swipe from (x1,y1) to (x2,y2)."""
    for i in range(steps + 1):
        t = i / steps
        x = int(x1 + (x2 - x1) * t)
        y = int(y1 + (y2 - y1) * t)
        digi.touch([(0, 1, x, y)])
        time.sleep(0.02)
    digi.touch([(0, 0, x2, y2)])  # finger up

def swipe_left():
    swipe(24383, 16383, 8383, 16383)

def swipe_right():
    swipe(8383, 16383, 24383, 16383)

def swipe_up():
    swipe(16383, 24383, 16383, 8383)

def swipe_down():
    swipe(16383, 8383, 16383, 24383)

def pinch(cx, cy, start_dist, end_dist, steps=20):
    """Pinch/zoom centered at (cx,cy). start_dist > end_dist = pinch in."""
    for i in range(steps + 1):
        t = i / steps
        d = int(start_dist + (end_dist - start_dist) * t)
        digi.touch([
            (0, 1, cx - d, cy),
            (1, 1, cx + d, cy),
        ])
        time.sleep(0.04)
    digi.touch([
        (0, 0, cx - end_dist, cy),
        (1, 0, cx + end_dist, cy),
    ])  # fingers up

def pinch_in():
    """Fingers move together (zoom out)."""
    pinch(16383, 16383, 8000, 1000)

def pinch_out():
    """Fingers move apart (zoom in)."""
    pinch(16383, 16383, 1000, 8000)

pwr = digitalio.DigitalInOut(board.NEOPIXEL_POWER)
pwr.direction = digitalio.Direction.OUTPUT
pwr.value = True
neo = digitalio.DigitalInOut(board.NEOPIXEL)
neo.direction = digitalio.Direction.OUTPUT

def led(r, g, b):
    """Set LED color (RGB — converted to GRB internally)."""
    neopixel_write.neopixel_write(neo, bytearray([g, r, b]))

def led_off():
    neopixel_write.neopixel_write(neo, bytearray([0, 0, 0]))
```

Then call them:

```python
type_text("hello from roadie!")
key(Keycode.SPACE)
key(Keycode.CONTROL, Keycode.A)           # select all
mouse_move(0, 0)                          # top-left
mouse_move(16383, 16383)                  # center
mouse_click()                             # left click
mouse_click(2)                            # right click
scroll(-3)                                # scroll up
touch(16383, 16383)                       # tap center
swipe_left()
swipe_right()
swipe_down()
swipe_up()
pinch_in()                                # zoom out
pinch_out()                               # zoom in
led(255, 0, 0)                          # red
led(0, 0, 255)                          # blue
led_off()

# Common keycodes: ENTER, ESCAPE, BACKSPACE, TAB, SPACE,
# UP_ARROW, DOWN_ARROW, LEFT_ARROW, RIGHT_ARROW

```

## Things to try on the Relay board

Once at the `>>>` prompt (after pressing Ctrl-C):

### Send commands via UART

> **Important:** The HID board must be running its main loop (i.e. `code.py` not
> interrupted) for it to respond over UART. Don't Ctrl-C both boards at the
> same time if you want to test UART communication.

Paste this setup block:

```python
import board, busio
from protocol import (
    MSG_SIZE, RESP_SIZE, BAUD, CMD_PING,
    pack_msg, pack_key_type, pack_mouse_move, pack_mouse_click, pack_touch,
)
import digitalio, neopixel_write, usb_cdc

uart = busio.UART(board.TX, board.RX, baudrate=BAUD, timeout=1)

# Flush stale bytes left over from code.py's main loop
while uart.read(64):
    pass

seq = 0
def send(msg):
    global seq
    while uart.in_waiting:
        uart.read(uart.in_waiting)  # flush stale bytes
    uart.write(msg)
    resp = uart.read(RESP_SIZE)
    print(resp)
    seq = (seq + 1) & 0xFF

def ping():
    send(pack_msg(CMD_PING, seq))

def type_text(text):
    send(pack_key_type(seq, text))

def mouse_move(x, y):
    send(pack_mouse_move(seq, x, y))

def mouse_click(buttons=1):
    send(pack_mouse_click(seq, buttons))

def touch(x, y):
    send(pack_touch(seq, [(0, 1, x, y)]))  # finger down
    send(pack_touch(seq, [(0, 0, x, y)]))   # finger up

pwr = digitalio.DigitalInOut(board.NEOPIXEL_POWER)
pwr.direction = digitalio.Direction.OUTPUT
pwr.value = True
neo = digitalio.DigitalInOut(board.NEOPIXEL)
neo.direction = digitalio.Direction.OUTPUT

def led(r, g, b):
    """Set LED color (RGB — converted to GRB internally)."""
    neopixel_write.neopixel_write(neo, bytearray([g, r, b]))

def led_off():
    neopixel_write.neopixel_write(neo, bytearray([0, 0, 0]))
```

Then call them:

```python
ping()                                    # check UART link
type_text("hello world")
mouse_move(0, 0)
mouse_click()
touch(16383, 16383)
led(255, 0, 0)                          # red
led_off()
print(usb_cdc.data)                       # data serial port object
print(usb_cdc.console)                    # console (REPL) port
dir(board)                                # list all available pins
```

## Quick reference: Protocol constants

From `board/shared/protocol.py`:

| Constant | Value | Description |
|---|---|---|
| `BAUD` | 921600 | UART baud rate between boards |
| `MSG_SIZE` | 32 | Command message size (bytes) |
| `RESP_SIZE` | 2 | Response size (status + seq) |
| `STATUS_OK` | 0x00 | Success |
| `STATUS_ERR` | 0x01 | Error |

## Troubleshooting

**"Permission denied" on /dev/ttyACM*:**
```bash
sudo usermod -a -G dialout $USER
# Log out and back in for it to take effect
```

**Screen says "[screen is terminating]" immediately:**
The device might be in use by another process (like the roadie server). Stop the server first:
```bash
# Check what's using the port
fuser /dev/ttyACM0
```

**No `>>>` prompt after Ctrl-C:**
Try pressing Ctrl-C a few times. If still nothing, press Enter. The board might need a moment.

**REPL shows garbled text:**
Make sure you're connecting at 115200 baud (the CircuitPython console default), not the UART baud rate (921600).

**Want to restart code.py without unplugging:**
Press `Ctrl-D` at the REPL prompt to soft-reload.
