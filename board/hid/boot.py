import supervisor
supervisor.set_usb_identification(product="Roadie-HID")

import storage
storage.getmount("/").label = "ROADIE_HID"

import usb_hid
from absolute_mouse.descriptor import device
usb_hid.enable((usb_hid.Device.KEYBOARD, device))
