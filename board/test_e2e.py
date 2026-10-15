#!/usr/bin/env python3
"""
Full end-to-end input test, through the roadie web server and a real browser
running on the target device.

The whole chain is exercised and then checked from the far end:

  this script → roadie HTTP API → relay (USB serial) → UART → HID board
              → USB HID → Pi kernel → X11 → browser → test page
              → POST /report → this script

Every leg asserts on what the *page* actually received, so a break anywhere
between the HTTP API and the rendered window fails loudly.

Usage:
    python3 board/test_e2e.py --target pi
    python3 board/test_e2e.py --target android
    python3 board/test_e2e.py --target iphone
    python3 board/test_e2e.py --target pc --text "roadie 123"

The relay board stays plugged into this host either way. What changes is where
the HID board is plugged in, and therefore which device the test page has to
run on:

  pi       HID board plugged into this machine. The page opens in this
           machine's own browser. Needs a display.
  android  HID board plugged into an Android phone (USB OTG). The page is
           opened on the phone over ADB-on-WiFi, because the phone's USB port
           is busy being a host for the HID board.
  iphone   HID board plugged into an iPhone. You open the printed URL in
           Safari yourself -- there is no ADB equivalent.
  pc       HID board plugged into another computer. You open the printed URL
           in a browser there.

Setup is deliberately done out of band rather than by driving HID, so that a
failure is attributable: if the keystrokes are what is broken, you cannot use
keystrokes to prepare the test that is meant to detect it.
"""

import argparse
import atexit
import glob
import json
import os
import shutil
import socket
import subprocess
import sys
import re
import tempfile
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from test_hid import INPUT_PREFIX, device_has_mt, one

SCRIPT_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPT_DIR.parent
PAGE = SCRIPT_DIR / "e2e" / "page.html"

HID_MAX = 32767  # absolute HID coordinate range is 0..32767 inclusive


# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------

def step(msg):
    print(f"\n{'─' * 64}\n  {msg}\n{'─' * 64}")

def info(msg):
    print(f"  ✅ {msg}")

def warn(msg):
    print(f"  ⚠️  {msg}")

def fail(msg):
    print(f"\n  ❌ {msg}")
    sys.exit(1)


# ---------------------------------------------------------------------------
# Observation collector: serves the page and receives the page's reports
# ---------------------------------------------------------------------------

class Collector:
    def __init__(self):
        self.events = []
        self.ready = threading.Event()
        self.ready_info = {}
        self._lock = threading.Lock()
        self._html = PAGE.read_bytes()
        self.server = None
        self.port = None

    def _handler(self):
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass  # keep the harness output clean

            def do_GET(self):
                if self.path in ("/", "/index.html"):
                    self.send_response(200)
                    self.send_header("Content-Type", "text/html; charset=utf-8")
                    self.send_header("Content-Length", str(len(outer._html)))
                    self.send_header("Cache-Control", "no-store")
                    self.end_headers()
                    self.wfile.write(outer._html)
                else:
                    self.send_response(204)
                    self.end_headers()

            def do_POST(self):
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length)
                self.send_response(204)
                self.end_headers()
                try:
                    event = json.loads(raw)
                except ValueError:
                    return
                with outer._lock:
                    outer.events.append(event)
                if event.get("kind") == "ready":
                    outer.ready_info = event
                    outer.ready.set()

        return Handler

    def start(self, host="127.0.0.1"):
        self.server = ThreadingHTTPServer((host, 0), self._handler())
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        return self.port

    def stop(self):
        if self.server:
            server, self.server = self.server, None
            server.shutdown()
            server.server_close()

    def drain(self):
        with self._lock:
            events, self.events = self.events, []
        return events

    def snapshot(self):
        with self._lock:
            return list(self.events)

    def wait_for(self, predicate, timeout):
        """Poll the collected events until predicate finds what it wants."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            match = predicate(self.snapshot())
            if match:
                return match
            time.sleep(0.05)
        return None


# ---------------------------------------------------------------------------
# roadie process / HTTP client
# ---------------------------------------------------------------------------

def http_json(url, payload=None, timeout=10):
    data = json.dumps(payload).encode() if payload is not None else None
    request = urllib.request.Request(
        url, data=data, method="POST" if data else "GET",
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=timeout) as resp:
        body = resp.read().decode()
    return json.loads(body) if body.strip() else {}


def roadie_alive(base):
    try:
        http_json(f"{base}/health", timeout=3)
        return True
    except Exception:
        return False


def lan_address():
    """This host's address on the LAN, as a target device would reach it."""
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
        try:
            s.connect(("8.8.8.8", 53))   # no packets sent; just picks a route
            return s.getsockname()[0]
        except OSError:
            return "127.0.0.1"


def open_on_android(url):
    """Ask the phone to open the URL via ADB over WiFi."""
    if not shutil.which("adb"):
        fail("adb not found. Install android-tools-adb, or open the URL on "
             "the phone by hand and re-run with --target pc.")
    devices = subprocess.run(["adb", "devices"], capture_output=True,
                             text=True, timeout=30).stdout
    connected = [l.split()[0] for l in devices.splitlines()[1:]
                 if l.strip() and l.split()[-1] == "device"]
    if not connected:
        fail("No ADB device. Connect over WiFi first:\n"
             "    adb tcpip 5555          (with the phone on USB, once)\n"
             "    adb connect <phone-ip>:5555\n"
             "  USB ADB can't be used here: the phone's port is busy acting "
             "as a USB host for the HID board.")
    info(f"ADB device: {connected[0]}")
    result = subprocess.run(
        ["adb", "shell", "am", "start", "-a", "android.intent.action.VIEW",
         "-d", url],
        capture_output=True, text=True, timeout=60,
    )
    if result.returncode != 0:
        fail(f"Couldn't open the browser on the phone: {result.stderr.strip()}")
    info("Asked the phone to open the test page")


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class Roadie:
    """Use an already-running roadie, or start one just for this test."""

    def __init__(self, base=None, quiet=False):
        self.base = base
        self.proc = None
        self.log = None
        # Starting a server is a means to an end for some callers, and not
        # worth three lines of commentary each time they do it.
        self.quiet = quiet

    def _say(self, fn, message):
        if not self.quiet:
            fn(message)

    def ensure(self):
        if self.base and roadie_alive(self.base):
            self._say(info, f"Using the roadie already serving at {self.base}")
            return self.base

        if self.base:
            self._say(warn,
                      f"Nothing answering at {self.base} — starting our own roadie")

        binary = REPO_ROOT / "roadie"
        if not binary.exists():
            fail(f"{binary} not built. Run: make build")

        port = free_port()
        self.base = f"http://127.0.0.1:{port}"
        self.log = open("/tmp/roadie-e2e.log", "w")
        self._say(print, f"  🚀 Starting {binary} --port {port}")
        self.proc = subprocess.Popen(
            [str(binary), "--port", str(port)],
            cwd=str(REPO_ROOT), stdout=self.log, stderr=subprocess.STDOUT,
        )

        for _ in range(60):
            if self.proc.poll() is not None:
                fail("roadie exited during startup — see /tmp/roadie-e2e.log")
            if roadie_alive(self.base):
                self._say(info, f"roadie is serving at {self.base}")
                return self.base
            time.sleep(0.5)
        fail("roadie did not become healthy — see /tmp/roadie-e2e.log")

    def stop(self):
        if self.proc and self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.proc.kill()
        self.proc = None
        if self.log:
            self.log.close()
            self.log = None


# ---------------------------------------------------------------------------
# Browser
# ---------------------------------------------------------------------------

def detect_browser(override=None):
    if override:
        path = shutil.which(override)
        if not path:
            fail(f"Browser not found: {override}")
        return path

    # Honour the desktop's configured default when we can read it.
    try:
        out = subprocess.run(
            ["xdg-settings", "get", "default-web-browser"],
            capture_output=True, text=True, timeout=10,
        ).stdout.strip()
    except Exception:
        out = ""
    desktop_map = {
        "chromium.desktop": "chromium",
        "chromium-browser.desktop": "chromium-browser",
        "firefox.desktop": "firefox",
        "firefox-esr.desktop": "firefox-esr",
    }
    preferred = desktop_map.get(out)
    if preferred and shutil.which(preferred):
        info(f"Desktop default browser is {preferred} ({out})")
        return shutil.which(preferred)
    if out:
        warn(f"Desktop default is {out!r}, which isn't one I can drive")

    for candidate in ("chromium", "chromium-browser", "firefox", "firefox-esr"):
        path = shutil.which(candidate)
        if path:
            info(f"Falling back to {candidate}")
            return path
    fail("No usable browser found (looked for chromium and firefox)")


def reap_old_profiles(prefix, max_age=3600):
    """Delete throwaway browser profiles left behind by killed runs.

    Cleanup only happens on a normal exit, and --keep-open skips it on
    purpose, so these accumulate -- a Chromium profile is about a megabyte.
    Only ones an hour old are removed, so a concurrent run's profile is
    never pulled out from under it.
    """
    now = time.time()
    for path in glob.glob(f"/tmp/{prefix}*"):
        try:
            if now - os.path.getmtime(path) > max_age:
                shutil.rmtree(path, ignore_errors=True)
        except OSError:
            pass


class Browser:
    def __init__(self, binary, url, display, xauthority):
        self.binary = binary
        self.url = url
        reap_old_profiles("roadie-e2e-profile-")
        self.profile = tempfile.mkdtemp(prefix="roadie-e2e-profile-")
        self.proc = None
        self.env = dict(os.environ, DISPLAY=display)
        if xauthority:
            self.env["XAUTHORITY"] = xauthority
        else:
            self.env.pop("XAUTHORITY", None)

    def screen_size(self):
        """Pixel size of the X screen, or None if it can't be determined.

        Needed because a bare virtual display has no window manager, so
        --kiosk is never resized to fill the screen: the window stays at
        whatever Chromium asked for and input aimed at the far edges lands
        outside it. Sizing the window explicitly removes that dependency.
        """
        try:
            out = subprocess.run(
                ["xrandr", "--query"], env=self.env,
                capture_output=True, text=True, timeout=15,
            ).stdout
        except (OSError, subprocess.SubprocessError):
            return None
        match = re.search(r"current\s+(\d+)\s*x\s*(\d+)", out)
        if match:
            return int(match.group(1)), int(match.group(2))
        return None

    def launch(self):
        name = os.path.basename(self.binary)
        if "firefox" in name:
            argv = [self.binary, "--kiosk", "--new-instance",
                    "--profile", self.profile, self.url]
        else:
            size = self.screen_size()
            geometry = []
            if size:
                geometry = [f"--window-size={size[0]},{size[1]}"]
                info(f"Sizing the browser window to {size[0]}x{size[1]}")
            argv = [
                self.binary,
                "--kiosk",
                *geometry,
                f"--user-data-dir={self.profile}",
                "--no-first-run",
                "--no-default-browser-check",
                "--disable-session-crashed-bubble",
                "--disable-infobars",
                "--noerrdialogs",
                "--disable-translate",
                # Keeps Chromium away from the login keyring, which would
                # otherwise raise an "Unlock Login Keyring" prompt that
                # steals focus mid-test.
                "--password-store=basic",
                "--use-mock-keychain",
                # Make sure the digitizer is treated as a real touch device.
                "--touch-events=enabled",
                "--window-position=0,0",
                self.url,
            ]
        print(f"  🌐 Launching {name} --kiosk on {self.env.get('DISPLAY')}")
        self.proc = subprocess.Popen(
            argv, env=self.env,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )

    def stop(self):
        if self.proc and self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.proc.kill()
        shutil.rmtree(self.profile, ignore_errors=True)


# ---------------------------------------------------------------------------
# The legs
# ---------------------------------------------------------------------------

class Suite:
    def __init__(self, base, collector, geom, tolerance, settle, local=True):
        self.local = local
        self.base = base
        self.collector = collector
        self.screen_w = geom["screenW"]
        self.screen_h = geom["screenH"]
        self.tolerance = tolerance
        self.settle = settle
        self.results = []

    def hid(self, path, payload):
        return http_json(f"{self.base}/api/hid/{path}", payload)

    def to_screen(self, hx, hy):
        """Map an absolute HID coordinate to the expected screen pixel."""
        return (round(hx / HID_MAX * (self.screen_w - 1)),
                round(hy / HID_MAX * (self.screen_h - 1)))

    def release_everything(self):
        """Let go of any button or contact, whatever happened."""
        try:
            self.hid("mouse/click", {"buttons": 7, "action": "release"})
            self.hid("touch", {"contacts": []})
        except Exception:
            pass

    def record(self, name, passed, detail):
        self.results.append((name, passed, detail))
        print(f"  {'✅' if passed else '❌'} {name}: {detail}")

    # -- individual legs ----------------------------------------------------

    def focus_window(self):
        """Click the page so it owns the keyboard before we type into it."""
        cx, cy = HID_MAX // 2, HID_MAX // 2
        self.hid("mouse/move", {"x": cx, "y": cy})
        time.sleep(0.3)
        self.hid("mouse/click", {"buttons": 1, "action": "click"})
        time.sleep(self.settle)
        got = self.collector.wait_for(
            lambda evs: next((e for e in evs if e.get("hasFocus")), None), 5)
        if got:
            info("Page has keyboard focus")
        else:
            warn("Page never reported focus — keyboard leg may fail")

    def leg_mouse_move(self, hx, hy):
        self.collector.drain()
        self.hid("mouse/move", {"x": hx, "y": hy})
        want = self.to_screen(hx, hy)
        got = self.collector.wait_for(
            lambda evs: next(
                (e for e in reversed(evs) if e.get("kind") == "pointermove"),
                None),
            self.settle + 3)
        if not got:
            self.record(f"mouse move ({hx},{hy})", False,
                        "page saw no pointermove")
            return
        have = (got["screenX"], got["screenY"])
        dx, dy = abs(have[0] - want[0]), abs(have[1] - want[1])
        if dx <= self.tolerance and dy <= self.tolerance:
            self.record(f"mouse move ({hx},{hy})", True,
                        f"page saw screen {have}, expected {want}")
        else:
            self.record(f"mouse move ({hx},{hy})", False,
                        f"page saw screen {have}, expected {want} "
                        f"(off by {dx},{dy} > {self.tolerance}px)")

    def leg_mouse_click(self):
        self.collector.drain()
        self.hid("mouse/click", {"buttons": 1, "action": "click"})
        self.collector.wait_for(
            lambda evs: next((e for e in evs if e.get("kind") == "pointerup"),
                             None),
            self.settle + 3)
        evs = self.collector.snapshot()
        down = [e for e in evs if e.get("kind") == "pointerdown"]
        up = [e for e in evs if e.get("kind") == "pointerup"]
        if down and up:
            kinds = sorted({e.get("pointerType") for e in down + up})
            self.record("mouse click", True,
                        f"page saw pointerdown+pointerup "
                        f"(button {down[0]['button']}, pointerType {kinds})")
        else:
            self.record("mouse click", False,
                        f"pointerdown={len(down)} pointerup={len(up)}")

    def leg_keyboard(self, text):
        self.collector.drain()
        self.hid("type", {"text": text})
        got = self.collector.wait_for(
            lambda evs: next(
                (e for e in reversed(evs)
                 if e.get("kind") == "keydown" and e.get("typed") == text),
                None),
            self.settle + 0.25 * len(text) + 5)
        if got:
            self.record("keyboard", True, f"page assembled {text!r}")
            return
        keys = [e for e in self.collector.snapshot()
                if e.get("kind") == "keydown"]
        assembled = keys[-1].get("typed") if keys else ""
        self.record("keyboard", False,
                    f"expected {text!r}, page assembled {assembled!r} "
                    f"from {len(keys)} keydown events")

    def leg_scroll(self):
        self.collector.drain()
        self.hid("mouse/scroll", {"amount": 5})
        got = self.collector.wait_for(
            lambda evs: next((e for e in evs if e.get("kind") == "wheel"), None),
            self.settle + 3)
        if got:
            self.record("mouse scroll", True, f"page saw wheel dy={got['deltaY']}")
        else:
            self.record("mouse scroll", False, "page saw no wheel event")

    def leg_touch(self, hx, hy):
        self.collector.drain()
        want = self.to_screen(hx, hy)
        self.hid("touch", {"contacts": [{"id": 0, "tip": True, "x": hx, "y": hy}]})
        got = self.collector.wait_for(
            lambda evs: next(
                (e for e in evs if e.get("kind") == "touchstart"
                 and e.get("changedTouches")),
                None),
            self.settle + 3)
        # Always lift the finger, even if nothing was observed.
        self.hid("touch", {"contacts": []})
        time.sleep(self.settle)

        if not got:
            down = self.collector.wait_for(
                lambda evs: next(
                    (e for e in evs if e.get("kind") == "pointerdown"
                     and e.get("pointerType") == "touch"), None), 0.1)
            if down:
                self.record("touch", False,
                            "page saw a touch pointerdown but no touchstart")
            else:
                self.record("touch", False, "page saw no touch event at all")
            return

        point = got["changedTouches"][0]
        have = (point["screenX"], point["screenY"])
        dx, dy = abs(have[0] - want[0]), abs(have[1] - want[1])
        if dx <= self.tolerance and dy <= self.tolerance:
            self.record("touch", True,
                        f"page saw touchstart at screen {have}, expected {want}")
        else:
            self.record("touch", False,
                        f"page saw touchstart at screen {have}, expected {want} "
                        f"(off by {dx},{dy} > {self.tolerance}px)")

    def leg_multitouch(self):
        # Same trade-off test_hid reports: roadie's udev rule rebinds the
        # HID board to hid-generic so mouse and touchscreen are separate
        # devices, which makes the host single-touch by design. The browser
        # can't see two contacts the kernel never produced.
        touch_path = one(INPUT_PREFIX + "*-event-if03") if self.local else None
        if touch_path and device_has_mt(touch_path) is False:
            self.record("multi-touch", True,
                        "skipped — this host runs hid-generic by roadie's own "
                        "udev rule, which is single-touch by design")
            return
        self.collector.drain()
        a = (HID_MAX // 4, HID_MAX // 2)
        b = (HID_MAX * 3 // 4, HID_MAX // 2)
        self.hid("touch", {"contacts": [
            {"id": 0, "tip": True, "x": a[0], "y": a[1]},
            {"id": 1, "tip": True, "x": b[0], "y": b[1]},
        ]})
        got = self.collector.wait_for(
            lambda evs: next(
                (e for e in evs if e.get("kind") in ("touchstart", "touchmove")
                 and len(e.get("touches", [])) >= 2),
                None),
            self.settle + 3)
        self.hid("touch", {"contacts": []})
        time.sleep(self.settle)
        if got:
            spots = [(t["screenX"], t["screenY"]) for t in got["touches"]]
            self.record("multi-touch", True, f"page saw 2 contacts at {spots}")
        else:
            seen = max((len(e.get("touches", []))
                        for e in self.collector.snapshot()
                        if e.get("kind", "").startswith("touch")), default=0)
            self.record("multi-touch", False,
                        f"page never saw 2 simultaneous contacts (max {seen})")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

LEGS = ["mouse", "click", "keyboard", "scroll", "touch", "multitouch"]


def main():
    parser = argparse.ArgumentParser(
        description="End-to-end input test through roadie and a real browser.")
    parser.add_argument("--target", default="pi",
                        choices=["pi", "android", "iphone", "pc"],
                        help="which device the HID board is plugged into")
    parser.add_argument("--roadie", default="http://127.0.0.1:8080",
                        help="base URL of a running roadie "
                             "(one is started if this isn't answering)")
    parser.add_argument("--text", default="roadie e2e 123",
                        help="text to type through the chain")
    parser.add_argument("--browser", help="browser binary to use")
    parser.add_argument("--display", default=os.environ.get("DISPLAY") or ":0")
    parser.add_argument("--xauthority",
                        default=os.environ.get("XAUTHORITY")
                        or str(Path.home() / ".Xauthority"))
    parser.add_argument("--tolerance", type=int, default=12,
                        help="allowed coordinate error in screen pixels")
    parser.add_argument("--settle", type=float, default=0.6,
                        help="seconds to wait for events after each command")
    parser.add_argument("--ready-timeout", type=float, default=60,
                        help="seconds to wait for the page to load")
    parser.add_argument("--skip", action="append", default=[], choices=LEGS,
                        help="skip a leg (repeatable)")
    parser.add_argument("--keep-open", action="store_true",
                        help="leave the browser running at the end")
    args = parser.parse_args()

    if not PAGE.exists():
        fail(f"Test page missing: {PAGE}")

    step("Phase 1: harness")
    local = args.target == "pi"
    collector = Collector()
    if local:
        port = collector.start("127.0.0.1")
        url = f"http://127.0.0.1:{port}/"
    else:
        # A phone or another PC has to be able to reach us.
        port = collector.start("0.0.0.0")
        url = f"http://{lan_address()}:{port}/"
    info(f"Collector listening on {url}")

    roadie = Roadie(args.roadie)
    started = {}

    def emergency_cleanup():
        """Tear down on any exit path, including one nobody planned for.

        A roadie left running holds the relay's serial port, so every later
        test fails with "device or resource busy" -- a long way from the
        actual fault, and on a different day.
        """
        browser = started.get("browser")
        if browser is not None and not args.keep_open:
            browser.stop()
        roadie.stop()
        collector.stop()

    atexit.register(emergency_cleanup)
    base = roadie.ensure()

    status = {}
    try:
        status = http_json(f"{base}/api/hid/status", timeout=5)
    except Exception as e:
        warn(f"Couldn't read HID status: {e}")
    if status.get("status") != "connected":
        roadie.stop()
        collector.stop()
        fail(f"Relay board is not connected (status: {status!r}). "
             "Plug in the relay board.")
    info(f"Relay connected on {status.get('port')}")

    step(f"Phase 2: open the test page on target '{args.target}'")
    browser = None
    if local:
        browser = Browser(detect_browser(args.browser), url,
                          args.display, args.xauthority)
        started["browser"] = browser
        browser.launch()
    elif args.target == "android":
        open_on_android(url)
    else:
        where = "Safari" if args.target == "iphone" else "a browser"
        print()
        print(f"  👉 Open this in {where} on the {args.target}:")
        print()
        print(f"       {url}")
        print()
        print("     (the test starts by itself once the page reports in)")

    print(f"  ⏳ Waiting up to {args.ready_timeout:.0f}s for the page...")
    if not collector.ready.wait(args.ready_timeout):
        if browser:
            browser.stop()
        roadie.stop()
        collector.stop()
        if local:
            fail("The page never loaded. Is the display awake and unlocked?")
        fail(f"The {args.target} never loaded the page. Check it can reach "
             f"{url} on this network, then try --ready-timeout 180.")

    geom = collector.ready_info
    info(f"Page ready: screen {geom['screenW']}x{geom['screenH']}, "
         f"viewport {geom['innerW']}x{geom['innerH']}, dpr {geom['dpr']}")
    info(f"navigator.maxTouchPoints = {geom.get('maxTouchPoints')}")
    time.sleep(1.0)  # let the kiosk window settle before driving it

    step("Phase 3: legs")
    suite = Suite(base, collector, geom, args.tolerance, args.settle,
                  local=local)
    try:
        if local:
            suite.focus_window()
        if "mouse" not in args.skip:
            suite.leg_mouse_move(0, 0)
            suite.leg_mouse_move(HID_MAX, HID_MAX)
            suite.leg_mouse_move(HID_MAX // 2, HID_MAX // 4)
        if "click" not in args.skip:
            suite.leg_mouse_click()
        if "keyboard" not in args.skip:
            suite.leg_keyboard(args.text)
        if "scroll" not in args.skip:
            suite.leg_scroll()
        if "touch" not in args.skip:
            suite.leg_touch(HID_MAX // 4, HID_MAX * 3 // 4)
        if "multitouch" not in args.skip:
            suite.leg_multitouch()
    finally:
        suite.release_everything()
        if browser and args.keep_open:
            warn(f"Leaving the browser open. Kill it with: "
                 f"pkill -f {browser.profile}")
        elif browser:
            browser.stop()
        roadie.stop()
        collector.stop()

    passed = sum(1 for _, ok, _ in suite.results if ok)
    total = len(suite.results)
    step(f"{'🎯' if passed == total else '💥'} {passed}/{total} legs passed")
    for name, ok, detail in suite.results:
        if not ok:
            print(f"     ❌ {name}: {detail}")
    print()
    return 0 if passed == total else 1


if __name__ == "__main__":
    sys.exit(main())
