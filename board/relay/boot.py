# no custom HID descriptors needed for relay board
import storage
storage.getmount("/").label = "ROADIE_RLY"

import usb_cdc
usb_cdc.enable(console=True, data=True)
