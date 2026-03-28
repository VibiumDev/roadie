# roadie hid board — receives I2C commands, sends USB HID
# TODO: implement I2C target + HID output
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

GREEN = bytearray([255, 0, 0])  # GRB order
OFF = bytearray([0, 0, 0])

print("roadie hid board ready")
while True:
    neopixel_write.neopixel_write(pixel_pin, GREEN)
    print("hid: alive")
    time.sleep(1)
    neopixel_write.neopixel_write(pixel_pin, OFF)
    time.sleep(1)
