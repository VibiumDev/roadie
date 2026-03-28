# roadie hid board — I2C target, receives commands and sends USB HID
import time
import board
import digitalio
import neopixel_write
import i2ctarget
from i2c_protocol import (
    I2C_ADDR, MSG_SIZE, CMD_PING, STATUS_OK, STATUS_ERR,
    unpack_msg,
)

# NeoPixel setup — QT Py RP2040 requires powering the NeoPixel
power = digitalio.DigitalInOut(board.NEOPIXEL_POWER)
power.direction = digitalio.Direction.OUTPUT
power.value = True

pixel_pin = digitalio.DigitalInOut(board.NEOPIXEL)
pixel_pin.direction = digitalio.Direction.OUTPUT

GREEN = bytearray([255, 0, 0])  # GRB order
OFF = bytearray([0, 0, 0])

# I2C target on STEMMA QT pins
target = i2ctarget.I2CTarget(board.SCL1, board.SDA1, (I2C_ADDR,))

# status returned on next read: [status_code, echo_seq]
last_status = bytearray([STATUS_OK, 0])

# accumulate fragmented I2C writes into a complete message
recv_buf = bytearray(MSG_SIZE)
recv_pos = 0

print("roadie hid board ready (i2c target @ 0x%02x)" % I2C_ADDR)
neopixel_write.neopixel_write(pixel_pin, GREEN)

while True:
    req = target.request()
    if req is None:
        continue

    with req:
        if not req.is_read:
            # controller is writing — accumulate fragments
            data = req.read(MSG_SIZE - recv_pos)
            n = len(data)
            recv_buf[recv_pos:recv_pos + n] = data
            recv_pos += n

            if recv_pos >= MSG_SIZE:
                # full message received — process it
                cmd, seq, payload = unpack_msg(recv_buf)
                if cmd == CMD_PING:
                    print("ping seq=%d" % seq)
                    neopixel_write.neopixel_write(pixel_pin, OFF)
                    time.sleep(0.1)
                    neopixel_write.neopixel_write(pixel_pin, GREEN)
                last_status[0] = STATUS_OK
                last_status[1] = seq
                recv_pos = 0
        else:
            # controller is reading status
            req.write(last_status)
