# Custom absolute mouse for USB HID.
# Descriptor matches the known-working zero-hid/TinyPilot pattern:
#   - No nested Pointer/Physical collection
#   - 8 buttons, absolute X/Y (0-32767), wheel, h-pan
#   - 7-byte report

import struct
import usb_hid

REPORT_ID = 11

_REPORT_DESCRIPTOR = bytes((
    0x05, 0x01,        # Usage Page (Generic Desktop)
    0x09, 0x02,        # Usage (Mouse)
    0xA1, 0x01,        # Collection (Application)
    0x85, REPORT_ID,   # Report ID (11)

    # 8 buttons
    0x05, 0x09,        # Usage Page (Button)
    0x19, 0x01,        # Usage Minimum (Button 1)
    0x29, 0x08,        # Usage Maximum (Button 8)
    0x15, 0x00,        # Logical Minimum (0)
    0x25, 0x01,        # Logical Maximum (1)
    0x95, 0x08,        # Report Count (8)
    0x75, 0x01,        # Report Size (1)
    0x81, 0x02,        # Input (Data, Var, Abs)

    # X, Y absolute coordinates
    0x05, 0x01,        # Usage Page (Generic Desktop)
    0x09, 0x30,        # Usage (X)
    0x09, 0x31,        # Usage (Y)
    0x15, 0x00,        # Logical Minimum (0)
    0x26, 0xFF, 0x7F,  # Logical Maximum (32767)
    0x75, 0x10,        # Report Size (16)
    0x95, 0x02,        # Report Count (2)
    0x81, 0x02,        # Input (Data, Var, Abs)

    # Vertical wheel
    0x09, 0x38,        # Usage (Wheel)
    0x15, 0x81,        # Logical Minimum (-127)
    0x25, 0x7F,        # Logical Maximum (127)
    0x75, 0x08,        # Report Size (8)
    0x95, 0x01,        # Report Count (1)
    0x81, 0x06,        # Input (Data, Var, Rel)

    # Horizontal wheel
    0x05, 0x0C,        # Usage Page (Consumer Devices)
    0x0A, 0x38, 0x02,  # Usage (AC Pan)
    0x15, 0x81,        # Logical Minimum (-127)
    0x25, 0x7F,        # Logical Maximum (127)
    0x75, 0x08,        # Report Size (8)
    0x95, 0x01,        # Report Count (1)
    0x81, 0x06,        # Input (Data, Var, Rel)

    0xC0,              # End Collection
))

device = usb_hid.Device(
    report_descriptor=_REPORT_DESCRIPTOR,
    usage_page=0x01,
    usage=0x02,
    report_ids=(REPORT_ID,),
    in_report_lengths=(7,),   # buttons(1) + x(2) + y(2) + wheel(1) + hpan(1)
    out_report_lengths=(0,),
)


class Mouse:
    """Absolute mouse with buttons, wheel, and horizontal scroll."""

    def __init__(self, devices):
        self._device = None
        for d in devices:
            if d.usage_page == 0x01 and d.usage == 0x02:
                self._device = d
                break
        if self._device is None:
            raise ValueError("mouse device not found")
        self._report = bytearray(7)
        self._buttons = 0

    def move(self, x=None, y=None, wheel=0):
        """Move to absolute position and/or scroll.
        x, y: 0-32767 absolute coordinates (None = no change)
        wheel: -127 to 127 vertical scroll
        """
        report = self._report
        report[0] = self._buttons
        if x is not None:
            struct.pack_into("<H", report, 1, min(max(x, 0), 32767))
        if y is not None:
            struct.pack_into("<H", report, 3, min(max(y, 0), 32767))
        report[5] = wheel & 0xFF
        report[6] = 0  # h-pan
        self._device.send_report(report)

    def press(self, buttons):
        """Press button(s). buttons: bitmask (1=left, 2=right, 4=middle)."""
        self._buttons |= buttons
        self._send()

    def release(self, buttons):
        """Release button(s)."""
        self._buttons &= ~buttons
        self._send()

    def click(self, buttons):
        """Click button(s) (press then release)."""
        self.press(buttons)
        self.release(buttons)

    def _send(self):
        """Send current state."""
        report = self._report
        report[0] = self._buttons
        report[5] = 0
        report[6] = 0
        self._device.send_report(report)
