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
import pathlib
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


def reattach_in_x(display):
    """Make X deliver this device's events to clients again.

    An exclusive grab keeps the test's input off the desktop, but X can go
    on ignoring the device afterwards -- events reach the kernel and never
    reach a window. Disabling and re-enabling it in X clears that, which is
    what "make unstick" does for the same symptom.
    """
    env = dict(os.environ, DISPLAY=display)
    try:
        listed = subprocess.run(["xinput", "list"], env=env,
                                capture_output=True, text=True, timeout=20)
    except (OSError, subprocess.SubprocessError) as exc:
        return f"could not run xinput ({exc})"
    if listed.returncode != 0:
        return f"xinput list failed on {display}: {listed.stderr.strip()[:80]}"

    done = 0
    for line in listed.stdout.splitlines():
        if "Roadie-HID" not in line or "id=" not in line:
            continue
        dev = line.split("id=")[1].split()[0]
        for action in ("disable", "enable"):
            r = subprocess.run(["xinput", action, dev], env=env,
                               capture_output=True, text=True, timeout=20)
            if r.returncode != 0:
                return f"xinput {action} {dev} failed: {r.stderr.strip()[:80]}"
        done += 1
    time.sleep(1)
    if not done:
        return f"no Roadie-HID device listed on {display}"
    return None


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


def browser_checks(base, record, args):
    """Play a pinch at a real browser and check it sees two fingers.

    The evdev checks prove the kernel tracks two contacts. They do not say
    whether a page ever hears about it -- that depends on the browser
    treating the device as a touchscreen, which is the part a phone would
    otherwise be needed to find out.
    """
    from test_e2e import Browser, Collector, detect_browser  # noqa: E402

    page = os.path.join(SCRIPT_DIR, "e2e", "gesture-page.html")
    collector = Collector(pathlib.Path(page))
    port = collector.start("127.0.0.1")
    url = f"http://127.0.0.1:{port}/"
    browser = Browser(detect_browser(args.browser), url,
                      args.display, args.xauthority)
    try:
        browser.launch()
        if not collector.ready.wait(args.ready_timeout):
            record("browser", False, "the page never loaded")
            return
        geom = collector.ready_info
        record("browser", True,
               f"page ready, navigator.maxTouchPoints = "
               f"{geom.get('maxTouchPoints')}")

        collector.drain()
        left, right = (11000, 16000), (21000, 16000)
        steps = 14
        for i in range(steps + 1):
            t = i / steps
            dx = round(7000 * t)
            touch(base, [
                {"id": 0, "tip": True, "x": left[0] - dx, "y": left[1]},
                {"id": 1, "tip": True, "x": right[0] + dx, "y": right[1]},
            ])
            time.sleep(0.04)

        seen = collector.wait_for(
            lambda evs: next((e for e in evs if (e.get("contacts") or 0) >= 2),
                             None),
            3)
        touch(base, [])
        time.sleep(args.settle)
        events = collector.snapshot()

        if not seen:
            any_touch = next((e for e in events if e.get("kind") == "touchstart"),
                             None)
            record("browser two fingers", False,
                   "the page saw one finger, not two" if any_touch
                   else "the page saw no touch events at all")
            return
        peak = max((e.get("peak") or 0) for e in events)
        record("browser two fingers", peak >= 2,
               f"page had {peak} touches at once")

        # The page turns the pinch into a scale. Checking that is worth more
        # than counting events: it says the gesture was understood, which is
        # the thing a phone would otherwise have to prove.
        scales = [e["scale"] for e in events if e.get("scale") is not None]
        if len(scales) < 2:
            record("browser pinch zooms", False,
                   "the page never reported a scale")
        else:
            grew = scales[-1] > scales[0] * 1.2
            record("browser pinch zooms", grew,
                   f"guitar scaled {scales[0]:.2f}x → {scales[-1]:.2f}x"
                   + ("" if grew else " (expected it to grow)"))

        # And a twist should rotate it.
        collector.drain()
        cx, cy, radius = 16000, 16000, 5000
        steps = 14
        for i in range(steps + 1):
            a = (i / steps) * (math.pi / 2)
            touch(base, [
                {"id": 0, "tip": True,
                 "x": round(cx - radius * math.cos(a)),
                 "y": round(cy - radius * math.sin(a))},
                {"id": 1, "tip": True,
                 "x": round(cx + radius * math.cos(a)),
                 "y": round(cy + radius * math.sin(a))},
            ])
            time.sleep(0.04)
        time.sleep(args.settle)
        touch(base, [])
        spins = [e["rotation"] for e in collector.snapshot()
                 if e.get("rotation") is not None]
        if len(spins) < 2:
            record("browser twist rotates", False,
                   "the page never reported a rotation")
        else:
            turned = abs(spins[-1] - spins[0])
            record("browser twist rotates", turned > 20,
                   f"guitar turned {turned:.0f}°"
                   + ("" if turned > 20 else " (expected it to rotate)"))
    finally:
        try:
            touch(base, [])
        except Exception:
            pass
        browser.stop()
        collector.stop()


def main():
    parser = argparse.ArgumentParser(
        description="Check two contacts and gestures reach this machine.")
    parser.add_argument("--base", default=None,
                        help="use a roadie already serving here")
    parser.add_argument("--settle", type=float, default=0.6)
    parser.add_argument("--keep", action="store_true",
                        help="stay in multi-touch mode at the end")
    parser.add_argument("--display", default=os.environ.get("DISPLAY") or ":0")
    parser.add_argument("--xauthority", default=os.environ.get("XAUTHORITY"))
    parser.add_argument("--browser", default=None)
    parser.add_argument("--ready-timeout", type=float, default=60)
    parser.add_argument("--no-browser", action="store_true",
                        help="only the /dev/input checks")
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

    total = [0]

    def record(name, ok, detail):
        total[0] += 1
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
            # x only: the kernel emits a coordinate when it changes, and y is
            # the same for both contacts here, so it is simply never sent.
            got0, got1 = history[0][-1][0], history[1][-1][0]
            want0, want1 = scale(left[0]), scale(right[0])
            near = max(8, (high - low) // 1000)
            ok = abs(got0 - want0) <= near and abs(got1 - want1) <= near
            record("contact positions", ok,
                   f"slot0 x={got0} (wanted {want0}), "
                   f"slot1 x={got1} (wanted {want1})")
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
        # The browser needs the device ungrabbed: an exclusive grab is how
        # the evdev checks keep test input off the desktop, and it would
        # equally keep it out of the page.
        capture.close()
        capture = None
        problem = reattach_in_x(args.display)
        if problem:
            record("re-attach for the browser", False, problem)
        else:
            note("re-attached the device in X so the browser can see it")
        if not args.no_browser:
            browser_checks(base, record, args)
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
        print(f"  ❌ test-multitouch     {len(failures)} of {total[0]} checks failed")
        return 1
    result(f"test-multitouch     {total[0]}/{total[0]} checks")
    return 0


if __name__ == "__main__":
    sys.exit(main())
