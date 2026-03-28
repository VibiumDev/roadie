import supervisor
supervisor.set_usb_identification(product="Roadie-Relay")

import storage
storage.getmount("/").label = "ROADIE_RLY"

import usb_cdc
usb_cdc.enable(console=True, data=True)
