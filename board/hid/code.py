# roadie hid board — receives commands over UART, executes USB HID actions
import time
import board
import busio
import digitalio
import neopixel_write
import usb_hid
from adafruit_hid.keyboard import Keyboard
from adafruit_hid.keyboard_layout_us import KeyboardLayoutUS
from absolute_mouse import Mouse as AbsoluteMouse
from protocol import (
    MSG_SIZE, BAUD, STATUS_OK, STATUS_ERR,
    CMD_PING, CMD_KEY_PRESS, CMD_KEY_RELEASE, CMD_KEY_TYPE,
    CMD_MOUSE_MOVE, CMD_MOUSE_CLICK, CMD_MOUSE_PRESS, CMD_MOUSE_RELEASE,
    CMD_MOUSE_SCROLL, CMD_TOUCH,
    unpack_msg, pack_resp,
)
from digitizer import Digitizer

# NeoPixel setup
power = digitalio.DigitalInOut(board.NEOPIXEL_POWER)
power.direction = digitalio.Direction.OUTPUT
power.value = True

pixel_pin = digitalio.DigitalInOut(board.NEOPIXEL)
pixel_pin.direction = digitalio.Direction.OUTPUT

GREEN = bytearray([255, 0, 0])  # GRB order
OFF = bytearray([0, 0, 0])

# UART from relay board on TX/RX pins (D6/D7)
uart = busio.UART(board.TX, board.RX, baudrate=BAUD, timeout=0.1)

# USB HID devices
kbd = Keyboard(usb_hid.devices)
layout = KeyboardLayoutUS(kbd)
mouse = AbsoluteMouse(usb_hid.devices)
try:
    digitizer = Digitizer(usb_hid.devices)
except ValueError:
    digitizer = None

# receive buffer
recv_buf = bytearray(MSG_SIZE)
recv_pos = 0
last_recv = time.monotonic()

print("roadie hid board ready (uart + hid)")
neopixel_write.neopixel_write(pixel_pin, GREEN)


def handle_command(cmd, seq, payload):
    """Execute a HID command. Returns status byte."""
    try:
        if cmd == CMD_PING:
            return STATUS_OK

        elif cmd == CMD_KEY_TYPE:
            text = payload.decode("ascii")
            layout.write(text)
            return STATUS_OK

        elif cmd == CMD_KEY_PRESS:
            kbd.press(payload[0])
            return STATUS_OK

        elif cmd == CMD_KEY_RELEASE:
            kbd.release(payload[0])
            return STATUS_OK

        elif cmd == CMD_MOUSE_MOVE:
            x = (payload[0] << 8) | payload[1]
            y = (payload[2] << 8) | payload[3]
            mouse.move(x, y)
            return STATUS_OK

        elif cmd == CMD_MOUSE_CLICK:
            mouse.click(payload[0])
            return STATUS_OK

        elif cmd == CMD_MOUSE_PRESS:
            mouse.press(payload[0])
            return STATUS_OK

        elif cmd == CMD_MOUSE_RELEASE:
            mouse.release(payload[0])
            return STATUS_OK

        elif cmd == CMD_MOUSE_SCROLL:
            amount = payload[0]
            if amount > 127:
                amount -= 256  # unsigned byte to signed int8
            mouse.move(wheel=amount)
            return STATUS_OK

        elif cmd == CMD_TOUCH:
            if not digitizer:
                return STATUS_ERR
            count = payload[0]
            contacts = []
            for i in range(count):
                off = 1 + i * 6
                cid = payload[off]
                tip = payload[off + 1]
                x = (payload[off + 2] << 8) | payload[off + 3]
                y = (payload[off + 4] << 8) | payload[off + 5]
                contacts.append((cid, tip, x, y))
            digitizer.touch(contacts)
            return STATUS_OK

        else:
            print("unknown cmd: 0x%02x" % cmd)
            return STATUS_ERR

    except Exception as e:
        print("hid err: %s" % e)
        return STATUS_ERR


while True:
    data = uart.read(MSG_SIZE - recv_pos)
    if data is None:
        # reset buffer if partial message stalls for >2s
        if recv_pos > 0 and time.monotonic() - last_recv > 2:
            recv_pos = 0
        continue

    recv_buf[recv_pos:recv_pos + len(data)] = data
    recv_pos += len(data)
    last_recv = time.monotonic()

    if recv_pos >= MSG_SIZE:
        cmd, seq, payload = unpack_msg(recv_buf)
        recv_pos = 0

        status = handle_command(cmd, seq, payload)
        uart.write(pack_resp(status, seq))

        if cmd == CMD_PING:
            print("ping seq=%d" % seq)
        else:
            print("cmd=0x%02x seq=%d status=%d" % (cmd, seq, status))

        neopixel_write.neopixel_write(pixel_pin, OFF)
        time.sleep(0.1)
        neopixel_write.neopixel_write(pixel_pin, GREEN)
