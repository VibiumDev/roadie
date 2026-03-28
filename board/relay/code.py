# roadie relay board — receives JSON commands over USB serial,
# translates to binary protocol, forwards over UART to HID board
import time
import json
import board
import busio
import usb_cdc
import digitalio
import neopixel_write
from protocol import (
    MSG_SIZE, RESP_SIZE, BAUD, STATUS_OK,
    CMD_PING, CMD_KEY_TYPE,
    pack_msg, pack_key_press, pack_key_release, pack_key_type,
    pack_mouse_move, pack_mouse_click, pack_mouse_press, pack_mouse_release,
)

# NeoPixel setup
power = digitalio.DigitalInOut(board.NEOPIXEL_POWER)
power.direction = digitalio.Direction.OUTPUT
power.value = True

pixel_pin = digitalio.DigitalInOut(board.NEOPIXEL)
pixel_pin.direction = digitalio.Direction.OUTPUT

RED = bytearray([0, 255, 0])  # GRB order
OFF = bytearray([0, 0, 0])

# UART to HID board on TX/RX pins (D6/D7)
uart = busio.UART(board.TX, board.RX, baudrate=BAUD, timeout=1)

# USB serial data port (separate from REPL)
data = usb_cdc.data

seq = 0
line_buf = ""
last_ping = time.monotonic()
HEARTBEAT_INTERVAL = 5

# flush stale UART bytes
while uart.read(64):
    pass

print("roadie relay board ready (uart + serial)")
neopixel_write.neopixel_write(pixel_pin, RED)


def next_seq():
    global seq
    s = seq
    seq = (seq + 1) & 0xFF
    return s


def send_and_recv(msg):
    """Send a 32-byte message over UART, read 2-byte response."""
    while uart.in_waiting:
        uart.read(uart.in_waiting)
    uart.write(msg)
    resp = uart.read(RESP_SIZE)
    if resp and len(resp) == RESP_SIZE:
        return resp[0], resp[1]  # status, echo_seq
    return None, None


def translate(d):
    """Translate a JSON command dict to one or more binary messages."""
    cmd = d.get("cmd")
    if cmd == "ping":
        return [pack_msg(CMD_PING, next_seq())]
    elif cmd == "type":
        text = d.get("text", "")
        msgs = []
        for i in range(0, max(1, len(text)), 29):
            msgs.append(pack_key_type(next_seq(), text[i:i + 29]))
        return msgs
    elif cmd == "key_press":
        return [pack_key_press(next_seq(), d["keycode"])]
    elif cmd == "key_release":
        return [pack_key_release(next_seq(), d["keycode"])]
    elif cmd == "mouse_move":
        return [pack_mouse_move(next_seq(), d["x"], d["y"])]
    elif cmd == "mouse_click":
        return [pack_mouse_click(next_seq(), d.get("buttons", 1))]
    elif cmd == "mouse_press":
        return [pack_mouse_press(next_seq(), d.get("buttons", 1))]
    elif cmd == "mouse_release":
        return [pack_mouse_release(next_seq(), d.get("buttons", 1))]
    else:
        print("unknown cmd: %s" % cmd)
        return []


def process_command(line):
    """Parse a JSON line and send the resulting messages."""
    try:
        d = json.loads(line)
    except ValueError:
        print("json err: %s" % line)
        return

    msgs = translate(d)
    for i, msg in enumerate(msgs):
        if i > 0:
            time.sleep(0.05)  # inter-chunk delay for multi-message commands
        status, echo = send_and_recv(msg)
        if status == STATUS_OK:
            print("ok seq=%d" % echo)
            neopixel_write.neopixel_write(pixel_pin, OFF)
            time.sleep(0.1)
            neopixel_write.neopixel_write(pixel_pin, RED)
        elif status is not None:
            print("err seq=%d status=%d" % (echo, status))
        else:
            print("timeout")
            neopixel_write.neopixel_write(pixel_pin, OFF)


while True:
    # check for incoming JSON commands on USB data port
    if data.in_waiting:
        byte = data.read(1)
        if byte == b"\n" or byte == b"\r":
            if line_buf.strip():
                process_command(line_buf.strip())
                last_ping = time.monotonic()
            line_buf = ""
        else:
            line_buf += byte.decode("ascii", "ignore")

    # heartbeat ping when idle
    elif time.monotonic() - last_ping >= HEARTBEAT_INTERVAL:
        status, echo = send_and_recv(pack_msg(CMD_PING, next_seq()))
        if status == STATUS_OK:
            neopixel_write.neopixel_write(pixel_pin, OFF)
            time.sleep(0.1)
            neopixel_write.neopixel_write(pixel_pin, RED)
        else:
            neopixel_write.neopixel_write(pixel_pin, OFF)
        last_ping = time.monotonic()
