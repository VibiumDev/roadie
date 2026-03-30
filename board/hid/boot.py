import supervisor
supervisor.set_usb_identification(product="Roadie-HID")

import storage
storage.getmount("/").label = "ROADIE_HID"

import usb_hid
from absolute_mouse.descriptor import device as mouse_device
from digitizer import device as digitizer_device
usb_hid.enable((usb_hid.Device.KEYBOARD, mouse_device, digitizer_device))
