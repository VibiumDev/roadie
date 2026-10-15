#!/usr/bin/env python3
"""
Board-level verification for the roadie board pair.

Rather than reading the boards' REPL chatter -- which can look healthy while
nothing is actually injected -- this walks the whole path and then checks what
the Pi *actually received* as USB HID input:

  Pi → serial JSON → Relay → UART → HID → USB HID → Pi /dev/input/*

Each leg asserts on decoded input events, so a silent break anywhere in
the chain fails loudly.

Usage:
    python3 board/test_hid.py
    python3 board/test_hid.py --quick          # ping + keyboard + mouse only
    python3 board/test_hid.py --text "roadie ok"
    python3 board/test_hid.py --skip touch

Note: the HID board is a real keyboard to this Pi. Typed text goes to
whatever currently has focus on the local console/desktop session.
"""

import argparse
import fcntl
import glob
import json
import os
import select
import struct
import sys
import threading
import time

try:
    import serial
except ImportError:
    sys.exit("pyserial not installed. Run: make setup")

# ---------------------------------------------------------------------------
# Linux input event plumbing
# ---------------------------------------------------------------------------

# struct input_event { struct timeval time; __u16 type, code; __s32 value; }
EVENT_FORMAT = "llHHi"
EVENT_SIZE = struct.calcsize(EVENT_FORMAT)

EV_SYN, EV_KEY, EV_REL, EV_ABS = 0x00, 0x01, 0x02, 0x03

# EVIOCGRAB = _IOW('E', 0x90, int): take exclusive ownership of an input
# device. Without it the keystrokes and pointer moves this test injects also
# land in whatever window currently has focus on the desktop — typing into a
# terminal, say — because the HID board is a real keyboard to this machine.
EVIOCGRAB = (1 << 30) | (4 << 16) | (ord('E') << 8) | 0x90

# EVIOCGABS(code) = _IOR('E', 0x40 + code, struct input_absinfo).
# The kernel only emits an absolute axis when its value *changes*, so sending
# the same coordinates twice produces a button event and no position events
# at all. Reading the axis directly gives the current position regardless.
def EVIOCGABS(code):
    return (2 << 30) | (24 << 16) | (ord('E') << 8) | (0x40 + code)


def current_abs(path, code):
    """Current value of an absolute axis, or None if it can't be read."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK)
    except OSError:
        return None
    try:
        buf = bytearray(24)
        fcntl.ioctl(fd, EVIOCGABS(code), buf, True)
        return struct.unpack("iiiiii", bytes(buf))[0]
    except OSError:
        return None
    finally:
        os.close(fd)

ABS_X, ABS_Y = 0x00, 0x01
ABS_MT_SLOT = 0x2F
ABS_MT_POSITION_X, ABS_MT_POSITION_Y = 0x35, 0x36
ABS_MT_TRACKING_ID = 0x39

BTN_LEFT, BTN_RIGHT, BTN_MIDDLE = 0x110, 0x111, 0x112
BTN_TOUCH = 0x14A

REL_WHEEL = 0x08

# Linux keycode -> character, enough to round-trip ASCII test strings.
_ROWS = [
    ("1234567890", 2),
    ("qwertyuiop", 16),
    ("asdfghjkl", 30),
    ("zxcvbnm", 44),
]
KEYCODE_TO_CHAR = {}
for _chars, _base in _ROWS:
    for _i, _c in enumerate(_chars):
        KEYCODE_TO_CHAR[_base + _i] = _c
KEYCODE_TO_CHAR.update({
    57: " ", 28: "\n", 15: "\t",
    12: "-", 13: "=", 26: "[", 27: "]", 43: "\\",
    39: ";", 40: "'", 41: "`", 51: ",", 52: ".", 53: "/",
})

SHIFT_KEYS = {42, 54}  # KEY_LEFTSHIFT, KEY_RIGHTSHIFT
SHIFT_MAP = {
    "1": "!", "2": "@", "3": "#", "4": "$", "5": "%",
    "6": "^", "7": "&", "8": "*", "9": "(", "0": ")",
    "-": "_", "=": "+", "[": "{", "]": "}", "\\": "|",
    ";": ":", "'": '"', "`": "~", ",": "<", ".": ">", "/": "?",
}

HID_PREFIX = "/dev/serial/by-id/usb-Adafruit_Roadie-HID_"
RELAY_PREFIX = "/dev/serial/by-id/usb-Adafruit_Roadie-Relay_"
INPUT_PREFIX = "/dev/input/by-id/usb-Adafruit_Roadie-HID_"


def one(pattern):
    """Return the single path matching a glob, or None."""
    matches = sorted(glob.glob(pattern))
    return matches[0] if matches else None


class EventCapture:
    """Background reader that accumulates events from several input devices.

    Devices are grabbed exclusively by default so the injected input is
    consumed here instead of leaking into the focused desktop window.
    """

    def __init__(self, paths, grab=True):
        self.fds = {}
        self.grabbed = set()
        for label, path in paths.items():
            if path and os.path.exists(path):
                try:
                    fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK)
                except OSError as e:
                    print(f"  ⚠️  cannot open {label} ({path}): {e}")
                    continue
                self.fds[fd] = label
                if grab:
                    try:
                        fcntl.ioctl(fd, EVIOCGRAB, 1)
                        self.grabbed.add(fd)
                    except OSError as e:
                        print(f"  ⚠️  could not grab {label} exclusively ({e}); "
                              "input will also reach the desktop")
        self.events = []
        self._lock = threading.Lock()
        self._stop = threading.Event()
        self._thread = threading.Thread(target=self._run, daemon=True)

    @property
    def ok(self):
        return bool(self.fds)

    def start(self):
        self._thread.start()

    def _run(self):
        while not self._stop.is_set():
            ready, _, _ = select.select(list(self.fds), [], [], 0.05)
            for fd in ready:
                try:
                    data = os.read(fd, EVENT_SIZE * 64)
                except (BlockingIOError, OSError):
                    continue
                label = self.fds[fd]
                for off in range(0, len(data) - EVENT_SIZE + 1, EVENT_SIZE):
                    _s, _us, etype, code, value = struct.unpack(
                        EVENT_FORMAT, data[off:off + EVENT_SIZE])
                    with self._lock:
                        self.events.append((label, etype, code, value))

    def drain(self):
        """Clear everything captured so far and return it."""
        with self._lock:
            captured, self.events = self.events, []
        return captured

    def snapshot(self):
        with self._lock:
            return list(self.events)

    def close(self):
        self._stop.set()
        self._thread.join(timeout=1)
        for fd in self.fds:
            if fd in self.grabbed:
                try:
                    fcntl.ioctl(fd, EVIOCGRAB, 0)
                except OSError:
                    pass
            try:
                os.close(fd)
            except OSError:
                pass


def decode_typed(events):
    """Rebuild the text represented by keyboard key-down events."""
    out = []
    shift = False
    for label, etype, code, value in events:
        if label != "kbd" or etype != EV_KEY:
            continue
        if code in SHIFT_KEYS:
            shift = value != 0
            continue
        if value != 1:  # key-down only
            continue
        char = KEYCODE_TO_CHAR.get(code)
        if char is None:
            out.append(f"<{code}>")
        elif shift:
            out.append(SHIFT_MAP.get(char, char.upper()))
        else:
            out.append(char)
    return "".join(out)


def device_has_mt(path):
    """Does this input device expose kernel multi-touch axes?

    roadie's own udev rule rebinds the HID board from hid-multitouch to
    hid-generic so the mouse and touchscreen show up as separate devices.
    That trade-off collapses touch to a single contact, so a multi-touch
    check here is reporting a configuration choice, not a defect.
    """
    try:
        real = os.path.realpath(path)
        name = os.path.basename(real)
        with open(f"/sys/class/input/{name}/device/capabilities/abs") as f:
            bitmap = int(f.read().strip().replace(",", ""), 16)
    except (OSError, ValueError):
        return None
    return bool(bitmap & (1 << ABS_MT_SLOT))


def max_mt_contacts(events):
    """Peak number of simultaneous contacts in a kernel MT-B event stream.

    Protocol B tracks each finger in a slot: ABS_MT_SLOT selects the slot and
    ABS_MT_TRACKING_ID assigns an id (or -1 to lift that finger).
    """
    slot = 0
    active = {}
    peak = 0
    for label, etype, code, value in events:
        if label != "touch" or etype != EV_ABS:
            continue
        if code == ABS_MT_SLOT:
            slot = value
        elif code == ABS_MT_TRACKING_ID:
            if value == -1:
                active.pop(slot, None)
            else:
                active[slot] = value
                peak = max(peak, len(active))
    return peak


def last_abs(events, label, axes):
    """Return the final absolute value seen for each axis code."""
    found = {}
    for lbl, etype, code, value in events:
        if lbl == label and etype == EV_ABS and code in axes:
            found[code] = value
    return found


# ---------------------------------------------------------------------------
# Test legs
# ---------------------------------------------------------------------------

class Runner:
    def __init__(self, data, capture, settle):
        self.data = data
        self.capture = capture
        self.settle = settle
        self.results = []

    def send(self, **payload):
        line = json.dumps(payload)
        self.data.write((line + "\n").encode())
        self.data.flush()
        return line

    def release_everything(self):
        """Let go of any button or contact, whatever happened.

        A run that stops between a press and its release leaves the button
        held as far as the host is concerned -- a context menu that will not
        close, a drag that will not end -- and nothing in the test would put
        it right.
        """
        try:
            self.send(cmd="mouse_release", buttons=7)  # left | right | middle
            self.send(cmd="touch", contacts=[])
        except Exception:
            pass  # already closing down; nothing useful to do here

    def record(self, name, passed, detail):
        self.results.append((name, passed, detail))
        mark = "✅" if passed else "❌"
        print(f"  {mark} {name}: {detail}")

    def leg_ping(self):
        """Ping and read the relay's reply on the data port.

        The relay answers {"ok":true} once the HID board has ACKed over
        UART, so this confirms the round-trip rather than merely that the
        relay accepted the command. It deliberately does not look at the
        console: a print with nothing draining it blocks the board.
        """
        self.capture.drain()
        self.data.reset_input_buffer()
        self.send(cmd="ping")
        reply = self.data.readline().decode(errors="replace").strip()
        if '"ok":true' in reply.replace(" ", ""):
            self.record("ping", True, f"relay round-trip confirmed ({reply})")
        elif reply:
            self.record("ping", False, f"relay reported {reply}")
        else:
            self.record("ping", False,
                        "no reply from the relay — is the HID board powered "
                        "and wired to it?")

    def leg_type(self, text):
        self.capture.drain()
        self.send(cmd="type", text=text)
        # Typing is paced by the HID board; give it room per character.
        time.sleep(max(self.settle, 0.08 * len(text) + 1.0))
        got = decode_typed(self.capture.snapshot())
        if got == text:
            self.record("keyboard", True, f"received {got!r} over USB HID")
        else:
            self.record("keyboard", False, f"expected {text!r}, Pi saw {got!r}")

    def leg_mouse_move(self, x, y):
        self.capture.drain()
        self.send(cmd="mouse_move", x=x, y=y)
        time.sleep(self.settle)
        pos = last_abs(self.capture.snapshot(), "mouse", {ABS_X, ABS_Y})
        if ABS_X not in pos or ABS_Y not in pos:
            self.record("mouse_move", False,
                        f"no absolute motion seen (sent {x},{y})")
            return
        gx, gy = pos[ABS_X], pos[ABS_Y]
        # Allow for the kernel rescaling the 0-32767 logical range.
        tol = max(64, int(0.02 * 32767))
        if abs(gx - x) <= tol and abs(gy - y) <= tol:
            self.record("mouse_move", True, f"landed at ({gx}, {gy})")
        else:
            self.record("mouse_move", False,
                        f"sent ({x}, {y}) but Pi saw ({gx}, {gy})")

    def leg_mouse_click(self):
        self.capture.drain()
        self.send(cmd="mouse_click", buttons=1)

        # Wait for both halves rather than sleeping a fixed time: under load
        # the release can land after the window closes, which read as a
        # release with no press.
        def both_seen(events):
            states = {e[3] for e in events
                      if e[0] == "mouse" and e[1] == EV_KEY
                      and e[2] == BTN_LEFT}
            return {0, 1} <= states

        deadline = time.monotonic() + self.settle + 3
        while time.monotonic() < deadline:
            if both_seen(self.capture.snapshot()):
                break
            time.sleep(0.05)
        events = self.capture.snapshot()
        downs = [e for e in events
                 if e[0] == "mouse" and e[1] == EV_KEY
                 and e[2] == BTN_LEFT and e[3] == 1]
        ups = [e for e in events
               if e[0] == "mouse" and e[1] == EV_KEY
               and e[2] == BTN_LEFT and e[3] == 0]
        if downs and ups:
            self.record("mouse_click", True, "left button press + release seen")
        else:
            self.record("mouse_click", False,
                        f"press={len(downs)} release={len(ups)}")

    def leg_mouse_scroll(self, amount):
        self.capture.drain()
        self.send(cmd="mouse_scroll", amount=amount)
        time.sleep(self.settle)
        wheel = [e for e in self.capture.snapshot()
                 if e[0] == "mouse" and e[1] == EV_REL and e[2] == REL_WHEEL
                 and e[3] != 0]
        if wheel:
            ticks = ", ".join(str(e[3]) for e in wheel)
            self.record("mouse_scroll", True, f"REL_WHEEL ticks: {ticks}")
        else:
            self.record("mouse_scroll", False,
                        f"sent amount={amount} but no REL_WHEEL reached the Pi")

    def leg_multitouch(self, a, b, touch_path=None):
        if touch_path and device_has_mt(touch_path) is False:
            self.record("multitouch", True,
                        "skipped — this host runs hid-generic by roadie's own "
                        "udev rule, which is single-touch by design")
            return
        self.capture.drain()
        self.send(cmd="touch", contacts=[
            {"id": 0, "tip": True, "x": a[0], "y": a[1]},
            {"id": 1, "tip": True, "x": b[0], "y": b[1]},
        ])
        time.sleep(self.settle)
        peak = max_mt_contacts(self.capture.snapshot())
        self.send(cmd="touch", contacts=[])
        time.sleep(self.settle)
        if peak >= 2:
            self.record("multitouch", True, f"{peak} simultaneous contacts")
        else:
            self.record("multitouch", False,
                        f"sent 2 contacts but the Pi tracked only {peak}")

    def leg_touch(self, x, y, path=None):
        self.capture.drain()
        self.send(cmd="touch", contacts=[{"id": 0, "tip": True, "x": x, "y": y}])
        time.sleep(self.settle)
        self.send(cmd="touch", contacts=[{"id": 0, "tip": False, "x": x, "y": y}])
        time.sleep(self.settle)
        events = self.capture.snapshot()
        touched = any(e[0] == "touch" and e[1] == EV_KEY
                      and e[2] == BTN_TOUCH and e[3] == 1 for e in events)
        pos = last_abs(events, "touch",
                       {ABS_X, ABS_Y, ABS_MT_POSITION_X, ABS_MT_POSITION_Y})
        gx = pos.get(ABS_MT_POSITION_X, pos.get(ABS_X))
        gy = pos.get(ABS_MT_POSITION_Y, pos.get(ABS_Y))
        # Repeating a coordinate emits no position events, because nothing
        # changed. Ask the device where it is instead of concluding it moved
        # nowhere.
        if (gx is None or gy is None) and path:
            gx = current_abs(path, ABS_X) if gx is None else gx
            gy = current_abs(path, ABS_Y) if gy is None else gy
        if touched and gx is not None and gy is not None:
            tol = max(64, int(0.02 * 32767))
            if abs(gx - x) <= tol and abs(gy - y) <= tol:
                self.record("touch", True, f"contact down at ({gx}, {gy})")
            else:
                self.record("touch", False,
                            f"sent ({x}, {y}) but Pi saw ({gx}, {gy})")
        elif touched:
            self.record("touch", False, "contact seen but no coordinates")
        else:
            self.record("touch", False, "no touch contact reached the Pi")


def main():
    parser = argparse.ArgumentParser(
        description="Closed-loop verification of the roadie board pair.")
    parser.add_argument("--text", default="roadie ok",
                        help="text to type through the chain")
    parser.add_argument("--quick", action="store_true",
                        help="only the fast legs: ping, keyboard, mouse move")
    parser.add_argument("--no-grab", action="store_true",
                        help="don't grab the input devices exclusively; the "
                             "injected input will also reach the desktop")
    parser.add_argument("--dump", action="store_true",
                        help="print every captured input event (diagnostics)")
    parser.add_argument("--settle", type=float, default=1.0,
                        help="seconds to wait for events after each command")
    parser.add_argument("--skip", action="append", default=[],
                        choices=["ping", "type", "mouse", "click", "scroll",
                                 "touch", "multitouch"],
                        help="skip a leg (repeatable)")
    args = parser.parse_args()
    if args.quick:
        args.skip = list(set(args.skip) | {"click", "scroll", "touch",
                                           "multitouch"})

    data_port = one(RELAY_PREFIX + "*-if02")
    relay_repl = one(RELAY_PREFIX + "*-if00")
    hid_repl = one(HID_PREFIX + "*-if00")

    if not data_port:
        sys.exit("❌ Relay data port not found. Is the relay board plugged in?")

    print()
    print("  🔌 Ports")
    print(f"     relay data: {data_port}")
    print(f"     relay repl: {relay_repl or '(none)'}")
    print(f"     hid repl:   {hid_repl or '(none)'}")

    inputs = {
        "kbd":   one(INPUT_PREFIX + "*-event-kbd"),
        "mouse": one(INPUT_PREFIX + "*-event-mouse"),
        "touch": one(INPUT_PREFIX + "*-event-if03"),
    }
    print()
    print("  🎯 Return path (what the Pi receives)"
          + ("" if args.no_grab else " — grabbed exclusively"))
    for label, path in inputs.items():
        print(f"     {label:<6} {path or '(not found)'}")

    capture = EventCapture(inputs, grab=not args.no_grab)
    if not capture.ok:
        sys.exit("\n❌ Could not open any HID input device. "
                 "Is this user in the 'input' group?")
    capture.start()

    # Draining the HID REPL keeps CircuitPython's console from stalling
    # on a full USB CDC buffer while we drive the chain.
    hrepl = serial.Serial(hid_repl, 115200, timeout=0.2) if hid_repl else None
    rrepl = serial.Serial(relay_repl, 115200, timeout=0.2) if relay_repl else None
    data = serial.Serial(data_port, 115200, timeout=2)
    time.sleep(0.5)
    for port in (hrepl, rrepl):
        if port:
            port.reset_input_buffer()

    print()
    print("  ▶️  Running legs")
    runner = Runner(data, capture, args.settle)
    try:
        if "ping" not in args.skip:
            runner.leg_ping()
        if "type" not in args.skip:
            runner.leg_type(args.text)
        if "mouse" not in args.skip:
            runner.leg_mouse_move(32767, 32767)
            runner.leg_mouse_move(0, 0)
            runner.leg_mouse_move(16384, 8192)
        if "click" not in args.skip:
            runner.leg_mouse_click()
        if "scroll" not in args.skip:
            runner.leg_mouse_scroll(5)
        if "touch" not in args.skip:
            runner.leg_touch(8192, 16384, path=inputs.get("touch"))
        if "multitouch" not in args.skip:
            runner.leg_multitouch((8192, 16384), (24576, 16384),
                                  touch_path=inputs.get("touch"))
    finally:
        runner.release_everything()
        if hrepl:
            tail = hrepl.read(hrepl.in_waiting or 1).decode(errors="replace").strip()
            if tail:
                print(f"\n  📋 HID REPL said: {tail[:300]}")
        for port in (data, hrepl, rrepl):
            if port:
                port.close()
        capture.close()

    if args.dump:
        print()
        print("  🔬 Raw events captured during the final leg")
        names = {EV_SYN: "SYN", EV_KEY: "KEY", EV_REL: "REL", EV_ABS: "ABS"}
        for label, etype, code, value in capture.snapshot():
            if etype == EV_SYN:
                continue
            print(f"     {label:<6} {names.get(etype, etype):<4} "
                  f"code=0x{code:03x} value={value}")

    passed = sum(1 for _, ok, _ in runner.results if ok)
    total = len(runner.results)
    print()
    print(f"  {'🎯' if passed == total else '💥'} {passed}/{total} legs passed")
    print()
    return 0 if passed == total else 1


if __name__ == "__main__":
    sys.exit(main())
