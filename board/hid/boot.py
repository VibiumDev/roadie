import supervisor
supervisor.set_usb_identification(product="Roadie-HID")

import storage
storage.getmount("/").label = "ROADIE_HID"

import usb_hid
from absolute_mouse.descriptor import device as mouse_device
from digitizer import device as digitizer_device
usb_hid.enable((usb_hid.Device.KEYBOARD, mouse_device, digitizer_device))

# Voice input: present a USB microphone beside the HID devices (CircuitPython 10.3+).
# Mic only — no speaker — so the phone keeps routing its own audio out via HDMI.
import usb_midi
usb_midi.disable()
import usb_audio
usb_audio.enable(sample_rate=16000, channel_count=1)
