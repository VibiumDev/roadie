#!/usr/bin/env python3
"""
Circular test: send a type command through the full pipeline and verify.

  Pi → serial JSON → Relay → UART → HID → USB keyboard → Pi

Usage:
    python3 board/test_circular.py
    python3 board/test_circular.py "hello world"

Requires both boards plugged into the Pi via USB and connected
over UART (TX/RX/GND).
"""

import glob
import json
import serial
import sys
import time


def find_port(pattern):
    """Find a serial port by glob pattern in /dev/serial/by-id/."""
    matches = glob.glob(pattern)
    if not matches:
        return None
    return matches[0]


def main():
    text = sys.argv[1] if len(sys.argv) > 1 else "ok"

    # find relay data port (if02)
    data_port = find_port("/dev/serial/by-id/usb-Adafruit_Roadie-Relay_*-if02")
    if not data_port:
        print("❌ Relay data port not found. Is the relay board plugged in?")
        sys.exit(1)

    relay_repl = find_port("/dev/serial/by-id/usb-Adafruit_Roadie-Relay_*-if00")
    hid_repl = find_port("/dev/serial/by-id/usb-Adafruit_Roadie-HID_*-if00")

    print(f"  📡 Relay data: {data_port}")
    if relay_repl:
        print(f"  📡 Relay REPL: {relay_repl}")
    if hid_repl:
        print(f"  📡 HID REPL:   {hid_repl}")
    print()

    # open ports
    data = serial.Serial(data_port, 115200, timeout=2)

    rrepl = serial.Serial(relay_repl, 115200, timeout=2) if relay_repl else None
    hrepl = serial.Serial(hid_repl, 115200, timeout=2) if hid_repl else None

    time.sleep(0.5)

    # flush
    if rrepl:
        rrepl.read(rrepl.in_waiting)
    if hrepl:
        hrepl.read(hrepl.in_waiting)

    # send ping first to verify the link
    print("  🏓 Sending ping...")
    data.write(b'{"cmd":"ping"}\n')
    time.sleep(1)

    if rrepl:
        resp = rrepl.read(rrepl.in_waiting or 256).decode(errors="replace").strip()
        if "ok" in resp:
            print(f"  ✅ Ping OK ({resp})")
        else:
            print(f"  ❌ Ping failed: {resp}")
            data.close()
            if rrepl:
                rrepl.close()
            if hrepl:
                hrepl.close()
            sys.exit(1)

    # flush again
    if rrepl:
        rrepl.read(rrepl.in_waiting)
    if hrepl:
        hrepl.read(hrepl.in_waiting)

    # send type command
    cmd = json.dumps({"cmd": "type", "text": text})
    print(f"  ⌨️  Sending: {cmd}")
    data.write((cmd + "\n").encode())
    time.sleep(2)

    if rrepl:
        relay_resp = rrepl.read(rrepl.in_waiting or 256).decode(errors="replace").strip()
        print(f"  📋 Relay: {relay_resp}")

    if hrepl:
        hid_resp = hrepl.read(hrepl.in_waiting or 256).decode(errors="replace").strip()
        print(f"  📋 HID:   {hid_resp}")

    # test absolute mouse — move to top-left corner
    if rrepl:
        rrepl.read(rrepl.in_waiting)
    if hrepl:
        hrepl.read(hrepl.in_waiting)

    # move to bottom-right first, then top-left — makes the jump visible
    data.write(b'{"cmd":"mouse_move","x":32767,"y":32767}\n')
    time.sleep(1)

    cmd = json.dumps({"cmd": "mouse_move", "x": 0, "y": 0})
    print(f"  🖱️  Sending: {cmd}")
    data.write((cmd + "\n").encode())
    time.sleep(1)

    if rrepl:
        relay_resp = rrepl.read(rrepl.in_waiting or 256).decode(errors="replace").strip()
        print(f"  📋 Relay: {relay_resp}")

    if hrepl:
        hid_resp = hrepl.read(hrepl.in_waiting or 256).decode(errors="replace").strip()
        print(f"  📋 HID:   {hid_resp}")

    print()
    print(f"  ✅ Done! \"{text}\" should have been typed, and the mouse should be at top-left (0, 0).")

    data.close()
    if rrepl:
        rrepl.close()
    if hrepl:
        hrepl.close()


if __name__ == "__main__":
    main()
