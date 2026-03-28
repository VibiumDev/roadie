# roadie hid board — receives commands over UART and sends USB HID
import board
import busio
import digitalio
import neopixel_write
import time
from protocol import (
    MSG_SIZE, BAUD, CMD_PING, STATUS_OK,
    unpack_msg, pack_resp,
)

# NeoPixel setup — QT Py RP2040 requires powering the NeoPixel
power = digitalio.DigitalInOut(board.NEOPIXEL_POWER)
power.direction = digitalio.Direction.OUTPUT
power.value = True

pixel_pin = digitalio.DigitalInOut(board.NEOPIXEL)
pixel_pin.direction = digitalio.Direction.OUTPUT

GREEN = bytearray([255, 0, 0])  # GRB order
OFF = bytearray([0, 0, 0])

# UART on TX/RX pins (D6/D7)
uart = busio.UART(board.TX, board.RX, baudrate=BAUD, timeout=0.1)

recv_buf = bytearray(MSG_SIZE)
recv_pos = 0

print("roadie hid board ready (uart)")
neopixel_write.neopixel_write(pixel_pin, GREEN)

while True:
    data = uart.read(MSG_SIZE - recv_pos)
    if data is None:
        continue

    recv_buf[recv_pos:recv_pos + len(data)] = data
    recv_pos += len(data)

    if recv_pos >= MSG_SIZE:
        cmd, seq, payload = unpack_msg(recv_buf)
        recv_pos = 0

        if cmd == CMD_PING:
            print("ping seq=%d" % seq)
            neopixel_write.neopixel_write(pixel_pin, OFF)
            time.sleep(0.1)
            neopixel_write.neopixel_write(pixel_pin, GREEN)

        uart.write(pack_resp(STATUS_OK, seq))
