# roadie relay board — I2C controller, sends pings to HID board
import time
import board
import bitbangio
import digitalio
import neopixel_write
from i2c_protocol import (
    I2C_ADDR, MSG_SIZE, CMD_PING, STATUS_OK,
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

# I2C controller on STEMMA QT pins — retry until HID board pull-ups are ready
i2c = None
print("roadie relay board waiting for i2c bus...")
while i2c is None:
    try:
        i2c = bitbangio.I2C(board.SCL1, board.SDA1)
    except RuntimeError:
        neopixel_write.neopixel_write(pixel_pin, OFF)
        time.sleep(0.5)
        neopixel_write.neopixel_write(pixel_pin, RED)
        time.sleep(0.5)

seq = 0

print("roadie relay board ready (i2c controller)")
neopixel_write.neopixel_write(pixel_pin, RED)

while True:
    while not i2c.try_lock():
        pass
    try:
        msg = pack_msg(CMD_PING, seq)
        i2c.writeto(I2C_ADDR, msg)
        time.sleep(0.01)  # give target time to process before reading

        result = bytearray(2)
        i2c.readfrom_into(I2C_ADDR, result)

        status, echo_seq = result[0], result[1]
        if status == STATUS_OK and echo_seq == seq:
            print("pong seq=%d ok" % seq)
            neopixel_write.neopixel_write(pixel_pin, OFF)
            time.sleep(0.1)
            neopixel_write.neopixel_write(pixel_pin, RED)
        else:
            print("pong seq=%d err status=%d" % (seq, status))
            neopixel_write.neopixel_write(pixel_pin, OFF)
    except OSError as e:
        print("i2c error: %s" % e)
        neopixel_write.neopixel_write(pixel_pin, OFF)
    finally:
        i2c.unlock()

    seq = (seq + 1) & 0xFF
    time.sleep(1)
