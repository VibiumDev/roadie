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
