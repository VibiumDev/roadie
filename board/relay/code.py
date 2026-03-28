# roadie relay board — sends pings to HID board over UART
import time
import board
import busio
import digitalio
import neopixel_write
from protocol import (
    MSG_SIZE, RESP_SIZE, BAUD, CMD_PING, STATUS_OK,
    pack_msg,
)

# NeoPixel setup — QT Py RP2040 requires powering the NeoPixel
power = digitalio.DigitalInOut(board.NEOPIXEL_POWER)
power.direction = digitalio.Direction.OUTPUT
power.value = True

pixel_pin = digitalio.DigitalInOut(board.NEOPIXEL)
pixel_pin.direction = digitalio.Direction.OUTPUT

RED = bytearray([0, 255, 0])  # GRB order
OFF = bytearray([0, 0, 0])

# UART on TX/RX pins (D6/D7)
uart = busio.UART(board.TX, board.RX, baudrate=BAUD, timeout=1)

seq = 0

print("roadie relay board ready (uart)")
neopixel_write.neopixel_write(pixel_pin, RED)

while True:
    msg = pack_msg(CMD_PING, seq)
    uart.write(msg)

    resp = uart.read(RESP_SIZE)
    if resp and len(resp) == RESP_SIZE:
        status, echo_seq = resp[0], resp[1]
        if status == STATUS_OK and echo_seq == seq:
            print("pong seq=%d ok" % seq)
            neopixel_write.neopixel_write(pixel_pin, OFF)
            time.sleep(0.1)
            neopixel_write.neopixel_write(pixel_pin, RED)
        else:
            print("pong seq=%d err status=%d" % (seq, status))
            neopixel_write.neopixel_write(pixel_pin, OFF)
    else:
        print("pong seq=%d timeout" % seq)
        neopixel_write.neopixel_write(pixel_pin, OFF)

    seq = (seq + 1) & 0xFF
    time.sleep(1)
