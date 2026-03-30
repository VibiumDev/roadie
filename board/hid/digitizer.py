# Multi-touch digitizer (2-contact touchscreen) for USB HID
# Presents as a touch screen to the connected device, enabling
# tap, drag, scroll, and pinch-to-zoom gestures.

import struct
import usb_hid

# Report ID 12 (keyboard=1, mouse=11, digitizer=12)
REPORT_ID = 12

# HID report descriptor for a 2-contact multi-touch digitizer.
# Follows the Windows/Android multi-touch protocol:
#   - Touch Screen usage (Digitizer page 0x0D, usage 0x04)
#   - 2 contact fingers, each with Contact ID, Tip Switch, X, Y
#   - Contact Count field
#   - Maximum Contact Count feature report
#
# Input report format (13 bytes):
#   [id0, tip0, x0_lo, x0_hi, y0_lo, y0_hi,
#    id1, tip1, x1_lo, x1_hi, y1_lo, y1_hi,
#    contact_count]

_REPORT_DESCRIPTOR = bytes((
    0x05, 0x0D,        # Usage Page (Digitizer)
    0x09, 0x04,        # Usage (Touch Screen)
    0xA1, 0x01,        # Collection (Application)
    0x85, REPORT_ID,   # Report ID (12)

    # -- Contact 0 --
    0x05, 0x0D,        # Usage Page (Digitizer)
    0x09, 0x22,        # Usage (Finger)
    0xA1, 0x02,        # Collection (Logical)

    # Contact ID
    0x09, 0x51,        # Usage (Contact Identifier)
    0x75, 0x08,        # Report Size (8)
    0x95, 0x01,        # Report Count (1)
    0x15, 0x00,        # Logical Minimum (0)
    0x25, 0x01,        # Logical Maximum (1)
    0x81, 0x02,        # Input (Data, Var, Abs)

    # Tip Switch (finger down)
    0x09, 0x42,        # Usage (Tip Switch)
    0x15, 0x00,        # Logical Minimum (0)
    0x25, 0x01,        # Logical Maximum (1)
    0x75, 0x08,        # Report Size (8)
    0x95, 0x01,        # Report Count (1)
    0x81, 0x02,        # Input (Data, Var, Abs)

    # X coordinate (absolute, 0-32767)
    0x05, 0x01,        # Usage Page (Generic Desktop)
    0x09, 0x30,        # Usage (X)
    0x15, 0x00,        # Logical Minimum (0)
    0x26, 0xFF, 0x7F,  # Logical Maximum (32767)
    0x75, 0x10,        # Report Size (16)
    0x95, 0x01,        # Report Count (1)
    0x81, 0x02,        # Input (Data, Var, Abs)

    # Y coordinate (absolute, 0-32767)
    0x09, 0x31,        # Usage (Y)
    0x15, 0x00,        # Logical Minimum (0)
    0x26, 0xFF, 0x7F,  # Logical Maximum (32767)
    0x75, 0x10,        # Report Size (16)
    0x95, 0x01,        # Report Count (1)
    0x81, 0x02,        # Input (Data, Var, Abs)

    0xC0,              # End Collection (Logical - finger 0)

    # -- Contact 1 --
    0x05, 0x0D,        # Usage Page (Digitizer)
    0x09, 0x22,        # Usage (Finger)
    0xA1, 0x02,        # Collection (Logical)

    # Contact ID
    0x09, 0x51,        # Usage (Contact Identifier)
    0x75, 0x08,        # Report Size (8)
    0x95, 0x01,        # Report Count (1)
    0x15, 0x00,        # Logical Minimum (0)
    0x25, 0x01,        # Logical Maximum (1)
    0x81, 0x02,        # Input (Data, Var, Abs)

    # Tip Switch
    0x09, 0x42,        # Usage (Tip Switch)
    0x15, 0x00,        # Logical Minimum (0)
    0x25, 0x01,        # Logical Maximum (1)
    0x75, 0x08,        # Report Size (8)
    0x95, 0x01,        # Report Count (1)
    0x81, 0x02,        # Input (Data, Var, Abs)

    # X coordinate
    0x05, 0x01,        # Usage Page (Generic Desktop)
    0x09, 0x30,        # Usage (X)
    0x15, 0x00,        # Logical Minimum (0)
    0x26, 0xFF, 0x7F,  # Logical Maximum (32767)
    0x75, 0x10,        # Report Size (16)
    0x95, 0x01,        # Report Count (1)
    0x81, 0x02,        # Input (Data, Var, Abs)

    # Y coordinate
    0x09, 0x31,        # Usage (Y)
    0x15, 0x00,        # Logical Minimum (0)
    0x26, 0xFF, 0x7F,  # Logical Maximum (32767)
    0x75, 0x10,        # Report Size (16)
    0x95, 0x01,        # Report Count (1)
    0x81, 0x02,        # Input (Data, Var, Abs)

    0xC0,              # End Collection (Logical - finger 1)

    # -- Contact Count (how many contacts in this report) --
    0x05, 0x0D,        # Usage Page (Digitizer)
    0x09, 0x54,        # Usage (Contact Count)
    0x15, 0x00,        # Logical Minimum (0)
    0x25, 0x02,        # Logical Maximum (2)
    0x75, 0x08,        # Report Size (8)
    0x95, 0x01,        # Report Count (1)
    0x81, 0x02,        # Input (Data, Var, Abs)

    # -- Maximum Contact Count (feature report) --
    0x09, 0x55,        # Usage (Contact Count Maximum)
    0x15, 0x00,        # Logical Minimum (0)
    0x25, 0x02,        # Logical Maximum (2)
    0x75, 0x08,        # Report Size (8)
    0x95, 0x01,        # Report Count (1)
    0xB1, 0x02,        # Feature (Data, Var, Abs)

    0xC0,              # End Collection (Application)
))

# usb_hid.Device for boot.py registration
device = usb_hid.Device(
    report_descriptor=_REPORT_DESCRIPTOR,
    usage_page=0x0D,
    usage=0x04,
    report_ids=(REPORT_ID,),
    in_report_lengths=(13,),   # 2 contacts * 6 bytes + 1 contact_count
    out_report_lengths=(0,),
)


class Digitizer:
    """Multi-touch digitizer with 2 contacts."""

    def __init__(self, devices):
        self._device = None
        for d in devices:
            if d.usage_page == 0x0D and d.usage == 0x04:
                self._device = d
                break
        if self._device is None:
            raise ValueError("digitizer device not found")
        self._report = bytearray(13)

    def touch(self, contacts):
        """Send a touch report.
        contacts: list of (id, tip, x, y) tuples.
          id: 0 or 1 (contact identifier)
          tip: 1=finger down, 0=finger lifted
          x, y: 0-32767 absolute coordinates
        """
        report = self._report
        # Zero out the report
        for i in range(13):
            report[i] = 0

        # Fill in contacts (always write both slots)
        for cid, tip, x, y in contacts:
            off = cid * 6  # contact 0 at byte 0, contact 1 at byte 6
            report[off] = cid
            report[off + 1] = 1 if tip else 0
            struct.pack_into("<H", report, off + 2, min(x, 32767))
            struct.pack_into("<H", report, off + 4, min(y, 32767))

        # Contact count at byte 12
        report[12] = len(contacts)

        self._device.send_report(report)

    def release_all(self):
        """Lift all fingers."""
        self.touch([])
