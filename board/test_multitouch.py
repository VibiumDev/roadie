#!/usr/bin/env python3
"""
Multi-touch and gesture test: two contacts, moving independently.

The board always sends two contacts. Whether the host sees them depends on
which kernel driver claims it, and one driver claims the whole USB HID
interface -- so a Linux host can have multi-touch or a separate mouse
device, not both. This test switches the host to multi-touch for the
duration and switches it back afterwards, however it ends.

That makes it the one test that cannot run alongside the others, which is
why it is its own target rather than part of "make test".

Usage:
    make test-multitouch
"""

import argparse
import fcntl
import math
import os
import struct
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from test_hid import (EVENT_FORMAT, EVENT_SIZE, EventCapture,  # noqa: E402
                      INPUT_PREFIX, max_mt_contacts, one)
from test_e2e import Roadie, http_json  # noqa: E402
from testout import check, fail, heading, note, result  # noqa: E402

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
TOGGLE = os.path.join(SCRIPT_DIR, "multitouch.sh")

EV_ABS = 0x03
ABS_MT_SLOT = 0x2F
ABS_MT_POSITION_X = 0x35
ABS_MT_POSITION_Y = 0x36
ABS_MT_TRACKING_ID = 0x39

HID_MAX = 32767


def axis_range(path, code):
    """Low and high values an absolute axis reports."""
    buf = bytearray(24)
    with open(path, "rb") as f:
        fcntl.ioctl(f.fileno(), (2 << 30) | (24 << 16) | (ord("E") << 8)
                    | (0x40 + code), buf)
    _, low, high = struct.unpack("iii", bytes(buf[:12]))
    return low, high


def mode():
    out = subprocess.run([TOGGLE, "status"], capture_output=True, text=True).stdout
    if "multi-touch" in out:
        return "multi"
    if "single-touch" in out:
        return "single"
    return "unknown"


def set_mode(want):
    subprocess.run([TOGGLE, "on" if want == "multi" else "off"],
                   capture_output=True, text=True)
    time.sleep(2)


def tracks(events):
    """Per-slot position history from a protocol B event stream.

    Returns {slot: [(x, y), ...]}, in the order the kernel reported them.
    A slot's entry only grows when a coordinate actually changes, which is
    what the kernel emits -- so a finger held still contributes one point.
    """
    slot = 0
    pos = {}
    history = {}
    for label, etype, code, value in events:
        if etype != EV_ABS:
            continue
        if code == ABS_MT_SLOT:
            slot = value
        elif code == ABS_MT_TRACKING_ID and value == -1:
            pos.pop(slot, None)
        elif code in (ABS_MT_POSITION_X, ABS_MT_POSITION_Y):
            # The kernel only emits a coordinate that changed, so a gesture
            # along one axis never mentions the other. Carry the last known
            # value forward rather than waiting for a pair.
            x, y = pos.get(slot, (0, 0))
            if code == ABS_MT_POSITION_X:
                x = value
            else:
                y = value
            pos[slot] = (x, y)
            history.setdefault(slot, []).append((x, y))
    return history


def spread(history):
    """Distance between the two contacts, first sample and last."""
    if 0 not in history or 1 not in history:
        return None, None
    def dist(a, b):
        return math.hypot(a[0] - b[0], a[1] - b[1])
    return (dist(history[0][0], history[1][0]),
            dist(history[0][-1], history[1][-1]))


def touch(base, contacts):
    http_json(f"{base}/api/hid/touch", {"contacts": contacts})


def main():
    parser = argparse.ArgumentParser(
        description="Check two contacts and gestures reach this machine.")
    parser.add_argument("--base", default=None,
                        help="use a roadie already serving here")
    parser.add_argument("--settle", type=float, default=0.6)
    parser.add_argument("--keep", action="store_true",
                        help="stay in multi-touch mode at the end")
    args = parser.parse_args()

    if not os.path.exists(TOGGLE):
        fail(f"{TOGGLE} is missing")

    heading("test-multitouch", "two contacts and gestures, read back at /dev/input")

    was = mode()
    if was == "unknown":
        fail("Could not tell which mode this host is in — is the HID board "
             "plugged into this machine?")
    if was != "multi":
        note("switching this host to multi-touch for the test")
        set_mode("multi")
        if mode() != "multi":
            fail("Could not switch to multi-touch. Does this user have sudo?")

    roadie = None
    capture = None
    failures = []

    def record(name, ok, detail):
        if ok:
            check(name, detail)
        else:
            failures.append(name)
            print(f"  ❌ {name:<18} {detail}")

    try:
        path = one(INPUT_PREFIX + "*-event-if03")
        if not path:
            fail("No touch device found. Is the HID board plugged into this "
                 "machine?")
        low, high = axis_range(path, ABS_MT_POSITION_X)
        record("device", True, f"{os.path.basename(path)}, x range {low}..{high}")

        capture = EventCapture({"touch": path}, grab=True)
        if not capture.ok:
            fail("Could not open the touch device. Is this user in the "
                 "'input' group?")
        capture.start()

        roadie = Roadie(args.base, quiet=True)
        base = roadie.ensure()

        def scale(v):
            return round(low + (high - low) * v / HID_MAX)

        # 1. Two contacts at once.
        capture.drain()
        left, right = (8000, 16000), (24000, 16000)
        touch(base, [{"id": 0, "tip": True, "x": left[0], "y": left[1]},
                     {"id": 1, "tip": True, "x": right[0], "y": right[1]}])
        time.sleep(args.settle)
        events = capture.snapshot()
        peak = max_mt_contacts(events)
        record("two contacts", peak >= 2,
               f"{peak} simultaneous contact(s)" if peak >= 2
               else f"sent 2 but the host tracked {peak}")

        # 2. Each contact where it was asked for.
        history = tracks(events)
        if 0 in history and 1 in history:
            got0, got1 = history[0][-1], history[1][-1]
            want0, want1 = (scale(left[0]), scale(left[1])), (scale(right[0]), scale(right[1]))
            near = max(8, (high - low) // 1000)
            ok = (abs(got0[0] - want0[0]) <= near and abs(got1[0] - want1[0]) <= near)
            record("contact positions", ok,
                   f"slot0 {got0} (wanted {want0}), slot1 {got1} (wanted {want1})")
        else:
            record("contact positions", False,
                   f"only slots {sorted(history)} reported a position")

        # 3. A pinch: the two contacts move apart, independently.
        capture.drain()
        steps = 12
        for i in range(steps + 1):
            t = i / steps
            dx = round(6000 * t)
            touch(base, [
                {"id": 0, "tip": True, "x": left[0] - dx, "y": left[1]},
                {"id": 1, "tip": True, "x": right[0] + dx, "y": right[1]},
            ])
            time.sleep(0.03)
        time.sleep(args.settle)
        pinch = capture.snapshot()
        first, last = spread(tracks(pinch))
        if first is None:
            record("pinch out", False, "the host did not track both contacts")
        else:
            grew = last > first * 1.2
            record("pinch out", grew,
                   f"contacts {first:.0f} apart → {last:.0f} apart"
                   + ("" if grew else " (expected them to separate)"))

        # 4. Both fingers lift.
        capture.drain()
        touch(base, [])
        time.sleep(args.settle)
        lifted = [v for _, et, code, v in capture.snapshot()
                  if et == EV_ABS and code == ABS_MT_TRACKING_ID and v == -1]
        record("lift", len(lifted) >= 2,
               f"{len(lifted)} contact(s) released"
               if len(lifted) >= 2 else
               f"only {len(lifted)} release(s); a finger may still be down")
    finally:
        if capture:
            try:
                touch(base, [])
            except Exception:
                pass
            capture.close()
        if roadie:
            roadie.stop()
        if was != "multi" and not args.keep:
            note("switching this host back to a separate mouse")
            set_mode("single")

    print()
    if failures:
        print(f"  ❌ test-multitouch     {len(failures)} check(s) failed")
        return 1
    result("test-multitouch     4/4 checks")
    return 0


if __name__ == "__main__":
    sys.exit(main())
