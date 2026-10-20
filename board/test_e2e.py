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
import base64
import glob
import json
import math
import os
import shutil
import socket
import struct
import subprocess
import wave
from array import array
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
sys.path.insert(0, str(SCRIPT_DIR))
REPO_ROOT = SCRIPT_DIR.parent
PAGE = SCRIPT_DIR / "e2e" / "page.html"
HUD = SCRIPT_DIR / "e2e" / "hud.js"

HID_MAX = 32767  # absolute HID coordinate range is 0..32767 inclusive

# The Vibium mark, for the drag check to trace: two glyphs that don't touch,
# a V (a hexagon with a notch cut from its top) and the triangle sitting in
# the notch, so the hexagon's top edge has a gap either side of the triangle.
# In logo pixels, 485 x 419. It is drawn round the screen's centre, where the
# HUD's ring sits, placed so the ring is the largest circle that fits inside
# the hexagon (radius 176).
VIBIUM_MARK_SIZE = (485, 419)
VIBIUM_MARK_CENTRE = (242.5, 176)
VIBIUM_MARK = [
    [(62, 0), (242, 310), (421, 0), (485, 109), (305, 419), (178, 419),
     (0, 109), (62, 0)],
    [(123, 0), (361, 0), (242, 205), (123, 0)],
]


# Output comes from testout so every test in the suite reads the same way.
# The old names are kept because they read well at the call sites here.
from testout import check, fail, heading, note, result, skip, warn  # noqa: E402
from testout import section as step  # noqa: E402


# ---------------------------------------------------------------------------
# Observation collector: serves the page and receives the page's reports
# ---------------------------------------------------------------------------

class Collector:
    def __init__(self, page=None, hud=False):
        self.events = []
        self.ready = threading.Event()
        self.ready_info = {}
        self._lock = threading.Lock()
        self._html = (page or PAGE).read_bytes()
        # The HUD is a skin laid over the page; it reads the run's progress
        # from /status. Without it, /status is kept but nobody asks.
        self._hud = HUD.read_bytes() if hud else None
        if hud:
            self._html = self._html.replace(
                b"</body>", b'<script src="hud.js"></script>\n</body>')
        self.status = {"plan": [], "results": [], "phase": None, "done": False}
        self.server = None
        self.port = None

    def _handler(self):
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass  # keep the harness output clean

            def send(self, body, kind):
                self.send_response(200)
                self.send_header("Content-Type", kind)
                self.send_header("Content-Length", str(len(body)))
                self.send_header("Cache-Control", "no-store")
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):
                if self.path in ("/", "/index.html"):
                    self.send(outer._html, "text/html; charset=utf-8")
                elif self.path == "/hud.js" and outer._hud:
                    self.send(outer._hud, "text/javascript; charset=utf-8")
                elif self.path == "/status":
                    with outer._lock:
                        body = json.dumps(outer.status).encode()
                    self.send(body, "application/json")
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

    def set_status(self, **fields):
        with self._lock:
            self.status.update(fields)

    def add_result(self, result):
        with self._lock:
            self.status["results"].append(result)

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


# ---------------------------------------------------------------------------
# Soundcheck: the five notes board/test_audio.py sends, heard at the far end
# ---------------------------------------------------------------------------

# Safari gives the microphone only to a secure page, and the test page is
# plain http, so the microphone is checked on this one: soundcheck/index.html,
# served over HTTPS. Its Mic button records, then plays back what it heard.
SOUNDCHECK = "https://soundcheck.vibium.dev/"
# Where to tap on it, as fractions of the screen: the Mic button, and the
# Allow on Safari's "use the microphone?" prompt. While the page records,
# its buttons are off, so a tap at Allow with no prompt up does nothing.
MIC_BUTTON = (0.50, 0.65)
ALLOW_MIC = (0.67, 0.56)


def record_target_audio(base, secs, path, on_tick=None):
    """Record what the target is playing, from roadie's /audio, to a 16 kHz
    mono wav. Returns the stream's parameters, or None if there was no audio.
    on_tick, if given, is called about four times a second meanwhile."""
    host, _, port = base.split("//", 1)[1].partition(":")
    try:
        sock = socket.create_connection((host, int(port or 80)), timeout=5)
    except OSError:
        return None
    key = base64.b64encode(os.urandom(16)).decode()
    sock.sendall(f"GET /audio HTTP/1.1\r\nHost: {host}\r\nUpgrade: websocket\r\n"
                 f"Connection: Upgrade\r\nSec-WebSocket-Key: {key}\r\n"
                 "Sec-WebSocket-Version: 13\r\n\r\n".encode())
    buf = b""
    while b"\r\n\r\n" not in buf:
        more = sock.recv(4096)
        if not more:
            sock.close()
            return None
        buf += more
    head, buf = buf.split(b"\r\n\r\n", 1)
    if b" 101 " not in head.split(b"\r\n")[0]:
        sock.close()
        return None  # 503: roadie has no audio

    def frame():
        nonlocal buf
        while True:
            if len(buf) >= 2:
                n, off = buf[1] & 0x7F, 2
                if n == 126:
                    n, off = struct.unpack(">H", buf[2:4])[0], 4
                elif n == 127:
                    n, off = struct.unpack(">Q", buf[2:10])[0], 10
                if len(buf) >= off + n:
                    op, data = buf[0] & 0x0F, buf[off:off + n]
                    buf = buf[off + n:]
                    return op, data
            more = sock.recv(65536)
            if not more:
                raise OSError("roadie closed /audio")
            buf += more

    params = json.loads(frame()[1])
    ch, rate = params["channels"], params["sampleRate"]
    samples = array("f")
    sock.settimeout(0.25)
    end = time.monotonic() + secs
    tick = 0.0
    while time.monotonic() < end:
        if on_tick and time.monotonic() >= tick:
            on_tick()
            tick = time.monotonic() + 0.25
        try:
            op, data = frame()
        except socket.timeout:
            continue
        except OSError:
            break
        if op == 2:
            samples.frombytes(data)  # float32, channels interleaved
        elif op == 8:
            break
    sock.close()

    # One channel, then down to about 16 kHz by averaging: the notes are all
    # under 1 kHz, and the note finder is slow at 48 kHz.
    step = max(1, round(rate / 16000))
    frames = len(samples) // ch
    out = array("h")
    for i in range(0, frames - step + 1, step):
        total = 0.0
        for j in range(i * ch, (i + step) * ch):
            total += samples[j]
        v = int(total / (step * ch) * 32767)
        out.append(max(-32768, min(32767, v)))
    with wave.open(path, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate // step)
        w.writeframes(out.tobytes())
    return params


def motif_verdict(path):
    """Whether a recording holds the motif, in order: (passed, detail)."""
    from test_audio import MOTIF, analyze
    peak, heard, dropouts = analyze(path)
    if peak < 500:
        return False, "silence: nothing came through"
    misses = []
    for i, (name, want, _) in enumerate(MOTIF):
        if i >= len(heard):
            misses.append(f"{name} not heard")
        elif abs(heard[i][0] - want) > want * 0.06:
            misses.append(f"{name} heard at {heard[i][0]:.0f} Hz, wanted {want:.0f}")
    if len(heard) > len(MOTIF):
        misses.append(f"{len(heard)} notes heard, wanted {len(MOTIF)}")
    lost = sum(dropouts)
    if lost > 0.5:
        misses.append(f"{lost * 1000:.0f} ms lost to dropouts")
    if misses:
        return False, "; ".join(misses)
    gaps = (f"{len(dropouts)} dropout(s), {lost * 1000:.0f} ms" if dropouts
            else "no dropouts")
    return True, f"{len(MOTIF)}/{len(MOTIF)} notes in order, {gaps}"


def lan_address():
    """This host's address on the LAN, as a target device would reach it."""
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
        try:
            s.connect(("8.8.8.8", 53))   # no packets sent; just picks a route
            return s.getsockname()[0]
        except OSError:
            return "127.0.0.1"


# USB HID keycodes, from the table in API.md.
KEY = {"a": 4, "b": 5, "h": 11, "l": 15, "s": 22, "enter": 40, "space": 44,
       "escape": 41, "[": 47, "ctrl": 224, "gui": 227}


def hid_key(base, keycode, action):
    http_json(f"{base}/api/hid/key", {"keycode": keycode, "action": action})


def hid_combo(base, *names):
    """Hold the modifiers, tap the key, let go in reverse order."""
    codes = [KEY[n] for n in names]
    for code in codes:
        hid_key(base, code, "press")
    for code in reversed(codes):
        hid_key(base, code, "release")
    time.sleep(0.4)


def open_with_keyboard(base, url, target):
    """Drive the target's own keyboard to open the page.

    roadie is a keyboard to the target, so it can do this itself rather
    than needing a second channel onto the device. That matters most on
    Android, where the obvious channel is adb -- and adb over USB is not
    available anyway, because the phone's port is busy being a USB host for
    the HID board. It also means the thing under test is the thing doing
    the work.

    Both of these are the shortcuts a hardware keyboard gets on a phone.
    They are best-effort: if the page does not report in, the caller falls
    back to asking for it to be opened by hand.
    """
    if target == "android":
        note("asking the phone to open the page (Meta+B, then the URL)")
        hid_combo(base, "gui", "h")        # home, for a known starting point
        time.sleep(1.0)
        hid_combo(base, "gui", "b")        # the browser shortcut
        time.sleep(3.0)
        hid_combo(base, "ctrl", "l")       # focus the address bar
        time.sleep(0.8)
    else:
        note("asking the phone to open the page (Spotlight, then Safari)")
        hid_combo(base, "gui", "h")        # home
        time.sleep(1.0)
        hid_combo(base, "gui", "space")    # Spotlight
        time.sleep(1.2)
        http_json(f"{base}/api/hid/type", {"text": "safari"})
        time.sleep(1.2)
        hid_key(base, KEY["enter"], "click")
        time.sleep(3.0)
        safari_go(base, url)
        return

    http_json(f"{base}/api/hid/type", {"text": url})
    time.sleep(0.5)
    hid_key(base, KEY["enter"], "click")


def safari_go(base, url):
    """Send Safari, already in front, to a URL. Escape first: a text field
    focused on the page keeps Cmd+L from the address bar."""
    hid_key(base, KEY["escape"], "click")
    time.sleep(0.4)
    hid_combo(base, "gui", "l")
    time.sleep(0.8)
    http_json(f"{base}/api/hid/type", {"text": url})
    time.sleep(0.5)
    hid_key(base, KEY["enter"], "click")


def ask_by_hand(url, target):
    where = {"iphone": "Safari", "android": "Chrome"}.get(target, "a browser")
    print()
    print(f"  👉 Open this in {where} on the {target}:")
    print()
    print(f"       {url}")
    print()
    print("     (the test starts by itself once the page reports in)")


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
    check("adb device", connected[0])
    result = subprocess.run(
        ["adb", "shell", "am", "start", "-a", "android.intent.action.VIEW",
         "-d", url],
        capture_output=True, text=True, timeout=60,
    )
    if result.returncode != 0:
        fail(f"Couldn't open the browser on the phone: {result.stderr.strip()}")
    check("phone", "asked it to open the test page")


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class Roadie:
    """Use an already-running roadie, or start one just for this test."""

    def __init__(self, base=None, quiet=False, platform=None):
        self.base = base
        self.platform = platform
        self.proc = None
        self.log = None
        # Starting a server is a means to an end for some callers, and not
        # worth three lines of commentary each time they do it.
        self.quiet = quiet

    def _say(self, fn, *args):
        if not self.quiet:
            fn(*args)

    def ensure(self):
        if self.base and roadie_alive(self.base):
            self._say(note, f"using the roadie already at {self.base}")
            return self.base

        if self.base:
            # The default is a port roadie is often not on; only a base the
            # caller actually asked for is worth a warning.
            self._say(note, f"nothing at {self.base}, starting our own")

        binary = REPO_ROOT / "roadie"
        if not binary.exists():
            fail(f"{binary} not built. Run: make build")

        port = free_port()
        self.base = f"http://127.0.0.1:{port}"
        self.log = open("/tmp/roadie-e2e.log", "w")
        argv = [str(binary), "--port", str(port)]
        if self.platform:
            argv += ["--platform", self.platform]
        self.proc = subprocess.Popen(
            argv,
            cwd=str(REPO_ROOT), stdout=self.log, stderr=subprocess.STDOUT,
        )

        for _ in range(60):
            if self.proc.poll() is not None:
                fail("roadie exited during startup — see /tmp/roadie-e2e.log")
            if roadie_alive(self.base):
                self._say(check, "roadie", self.base)
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
        check("browser", f"{preferred}, this desktop's default ({out})")
        return shutil.which(preferred)
    if out:
        warn(f"Desktop default is {out!r}, which isn't one I can drive")

    for candidate in ("chromium", "chromium-browser", "firefox", "firefox-esr"):
        path = shutil.which(candidate)
        if path:
            check("browser", f"{candidate} (no desktop default to go on)")
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
                check("window", f"sized to {size[0]}x{size[1]}")
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
        note(f"launching {name} --kiosk on {self.env.get('DISPLAY')}")
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
    def __init__(self, base, collector, geom, tolerance, settle, local=True,
                 page_relative=False, hud=False):
        self.local = local
        # Paced to be watched: the pointer glides and the keys are typed one
        # by one. What is checked is the same.
        self.hud = hud
        self.phase = None
        self.pos = (HID_MAX // 2, HID_MAX // 2)
        self.base = base
        self.collector = collector
        self.screen_w = geom["screenW"]
        self.screen_h = geom["screenH"]
        self.inner_h = geom["innerH"]
        # Positions come relative to the page (iOS); see leg_pointer_position.
        self.page_relative = page_relative
        self.top_bar = None
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

    def begin(self, phase):
        """Start a group of checks: the HUD shows which one is running."""
        self.phase = phase
        self.collector.set_status(phase=phase)

    def beat(self, seconds=1.4):
        """A pause between groups, only when the run is being watched."""
        if self.hud:
            time.sleep(seconds)

    def move(self, hx, hy):
        """Move the pointer; gliding there when the run is being watched."""
        if self.hud:
            x0, y0 = self.pos
            steps = 22
            for i in range(1, steps):
                t = i / steps
                t = t * t * (3 - 2 * t)  # ease in and out
                self.hid("mouse/move", {"x": round(x0 + (hx - x0) * t),
                                        "y": round(y0 + (hy - y0) * t)})
                time.sleep(0.018)
        self.hid("mouse/move", {"x": hx, "y": hy})
        self.pos = (hx, hy)

    def record(self, name, passed, detail):
        self.results.append((name, passed, detail))
        self.collector.add_result({
            "phase": self.phase, "name": name, "passed": passed,
            "skipped": passed and detail.startswith("skipped — "),
            "detail": detail,
        })
        if not passed:
            print(f"  ❌ {name:<18} {detail}")
        elif detail.startswith("skipped — "):
            skip(name, detail[len("skipped — "):])
        else:
            check(name, detail)

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
            check("keyboard focus", "the page has it")
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
            self.record("mouse move", False,
                        f"({hx},{hy}) → page saw no pointermove")
            return
        have = (got["screenX"], got["screenY"])
        dx, dy = abs(have[0] - want[0]), abs(have[1] - want[1])
        if dx <= self.tolerance and dy <= self.tolerance:
            self.record("mouse move", True,
                        f"({hx},{hy}) → page saw {have}, expected {want}")
        else:
            self.record("mouse move", False,
                        f"({hx},{hy}) → page saw {have}, expected {want} "
                        f"(off by {dx},{dy} > {self.tolerance}px)")

    def leg_pointer_position(self, hx, hy):
        """Where the pointer is, checked by pressing rather than hovering.

        On iOS the pointer is a touch: with AssistiveTouch's Perform Touch
        Gestures the mouse acts as a finger, and a finger has no hover, so
        moving reports nothing until it presses. Moving and then clicking
        shows where it was.

        Safari on iOS also gives positions relative to the page, not the
        screen, and the page starts below a top bar it cannot measure. The
        first press measures that bar, which has to fit in the height Safari
        takes from the screen; later presses have to agree with it.
        """
        self.collector.drain()
        want = self.to_screen(hx, hy)
        self.move(hx, hy)
        time.sleep(self.settle)
        self.hid("mouse/click", {"buttons": 1})
        got = self.collector.wait_for(
            lambda evs: next(
                (e for e in evs if e.get("kind") == "pointerdown"), None),
            self.settle + 3)
        if not got:
            self.record("pointer position", False,
                        f"({hx},{hy}) → the page saw no press")
            return
        # iOS holds a tap down for about 100 ms, however short the click.
        # Moving before it lets go drags the finger instead.
        self.collector.wait_for(
            lambda evs: next(
                (e for e in evs if e.get("kind") == "pointerup"), None),
            self.settle + 3)
        have = (got["screenX"], got["screenY"])
        bar = ""
        if self.page_relative:
            if self.top_bar is None:
                top = want[1] - have[1]
                room = self.screen_h - self.inner_h
                if not -self.tolerance <= top <= room + self.tolerance:
                    self.record("pointer position", False,
                                f"({hx},{hy}) → pressed at page {have}, "
                                f"{top:.0f}pt below where expected; Safari "
                                f"only takes {room}pt")
                    return
                self.top_bar = top
                self.collector.set_status(
                    center_y=self.screen_h / 2 - top)
            have = (have[0], have[1] + self.top_bar)
            bar = f" (page {self.top_bar:.0f}pt down)"
        dx, dy = abs(have[0] - want[0]), abs(have[1] - want[1])
        if dx <= self.tolerance and dy <= self.tolerance:
            self.record("pointer position", True,
                        f"({hx},{hy}) → pressed at {have}, expected {want}{bar}")
        else:
            self.record("pointer position", False,
                        f"({hx},{hy}) → pressed at {have}, expected {want} "
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
            # Say what did turn up, so a failure points somewhere.
            kinds = {}
            for e in evs:
                kinds[e.get("kind")] = kinds.get(e.get("kind"), 0) + 1
            seen = ", ".join(f"{k}x{v}" for k, v in sorted(kinds.items())) \
                or "nothing at all"
            self.record("mouse click", False,
                        f"pointerdown={len(down)} pointerup={len(up)}; "
                        f"page received: {seen}")

    def leg_keyboard(self, text):
        self.collector.drain()
        if self.hud:
            for ch in text:
                self.hid("type", {"text": ch})
                time.sleep(0.07)
        else:
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

    def leg_trace(self):
        """Drag the Vibium mark: press, move with the button held, let go.

        On iOS a drag is how a finger scrolls and swipes, so this stands in
        for the wheel there. The size of the path is checked rather than its
        place, which holds whatever offset the page has from the screen.
        """
        self.collector.drain()
        w, h = self.screen_w, self.screen_h
        scale = min(w, h) * 0.66 / VIBIUM_MARK_SIZE[0]
        mx, my = VIBIUM_MARK_CENTRE
        strokes = [[(w / 2 + (x - mx) * scale, h / 2 + (y - my) * scale)
                    for x, y in stroke] for stroke in VIBIUM_MARK]
        step = min(w, h) * 0.66 / 30   # about 30 moves across the mark

        def at(x, y):
            return (round(x / (w - 1) * HID_MAX), round(y / (h - 1) * HID_MAX))

        def lifts(evs):
            return sum(1 for e in evs if e.get("kind") == "pointerup")

        for n, stroke in enumerate(strokes, 1):
            self.move(*at(*stroke[0]))
            time.sleep(self.settle)
            self.hid("mouse/click", {"buttons": 1, "action": "press"})
            try:
                time.sleep(0.15)
                for (x0, y0), (x1, y1) in zip(stroke, stroke[1:]):
                    k = max(4, round(math.hypot(x1 - x0, y1 - y0) / step))
                    for i in range(1, k + 1):
                        x, y = at(x0 + (x1 - x0) * i / k, y0 + (y1 - y0) * i / k)
                        self.hid("mouse/move", {"x": x, "y": y})
                        time.sleep(0.02 if self.hud else 0.012)
                time.sleep(0.15)
            finally:
                self.hid("mouse/click", {"buttons": 1, "action": "release"})
            self.pos = at(*stroke[-1])
            # Lifted before moving on, or the move to the next stroke drags.
            self.collector.wait_for(lambda evs: lifts(evs) >= n or None,
                                    self.settle + 3)
        time.sleep(0.3)

        # Where the page saw the pointer while it was held, stroke by stroke:
        # touch moves on a phone, pointer moves between press and release
        # with a mouse.
        seen, held = [], False
        for e in self.collector.snapshot():
            kind = e.get("kind")
            if kind in ("touchstart", "pointerdown"):
                if kind == "pointerdown":
                    held = True
                if not seen or seen[-1]:
                    seen.append([])
            elif kind == "pointerup":
                held = False
            elif kind == "touchmove":
                seen[-1:] = seen[-1:] or [[]]
                seen[-1] += [(t["clientX"], t["clientY"])
                             for t in e.get("changedTouches", [])]
            elif kind == "pointermove" and held:
                seen[-1:] = seen[-1:] or [[]]
                seen[-1].append((e["clientX"], e["clientY"]))
        path = [p for stroke in seen for p in stroke]

        def length(stroke):
            return sum(math.dist(a, b) for a, b in zip(stroke, stroke[1:]))

        points = [p for stroke in strokes for p in stroke]
        want = (max(x for x, _ in points) - min(x for x, _ in points),
                max(y for _, y in points) - min(y for _, y in points))
        if len(path) < 10:
            self.record("drag", False,
                        f"page saw {len(path)} moves while held, wanted a path")
            return
        have = (max(x for x, _ in path) - min(x for x, _ in path),
                max(y for _, y in path) - min(y for _, y in path))
        # How much of each stroke arrived: a drag the phone took over part
        # way (to pan or zoom the page) stops short, even if its size looks
        # right.
        seen = [stroke for stroke in seen if stroke]
        covered = min(length(a) / length(b) for a, b in zip(seen, strokes))
        if len(seen) != len(strokes):
            covered = 0.0
        dw, dh = abs(have[0] - want[0]), abs(have[1] - want[1])
        size = f"{have[0]:.0f}x{have[1]:.0f}"
        got = (f"{size} in {len(path)} moves, {len(seen)} strokes, the "
               f"shortest {covered:.0%} there; expected "
               f"{want[0]:.0f}x{want[1]:.0f}")
        if dw <= self.tolerance and dh <= self.tolerance and covered >= 0.9:
            self.record("drag", True, f"page traced the Vibium mark {got}")
        else:
            self.record("drag", False, f"page traced {got} (within "
                        f"{self.tolerance}px, and 90% of each stroke, wanted)")

    def leg_speaker(self, backend):
        """The target plays the motif; roadie's /audio has to hear it.

        page.html plays it on a tap after a press held still for a second (a
        page may only make sound in answer to a tap, and a long press isn't
        one in Safari). With the Linux gadget a phone
        has two ways out, USB and HDMI, and the gadget's speaker is the one
        under test, so /health is asked which one carried it.
        """
        from test_audio import TONE_SECS
        path = "/tmp/roadie_e2e_speaker.wav"
        self.collector.drain()
        sources, box = [], {}

        def which():
            try:
                health = http_json(f"{self.base}/health", timeout=2)
            except Exception:
                return
            src = (health.get("audio") or {}).get("source")
            if src:
                sources.append(src)

        rec = threading.Thread(target=lambda: box.update(
            params=record_target_audio(self.base, TONE_SECS + 5, path, which)),
            daemon=True)
        rec.start()
        time.sleep(0.5)
        self.move(HID_MAX // 2, HID_MAX // 2)
        time.sleep(self.settle)
        self.hid("mouse/click", {"buttons": 1, "action": "press"})
        time.sleep(1.0)
        self.hid("mouse/click", {"buttons": 1, "action": "release"})
        self.collector.wait_for(
            lambda evs: next((e for e in evs if e.get("kind") == "armed"), None),
            self.settle + 2)
        time.sleep(0.5)
        self.hid("mouse/click", {"buttons": 1})
        started = self.collector.wait_for(
            lambda evs: next((e for e in evs if e.get("kind") == "motif"), None),
            self.settle + 3)
        rec.join()
        if not started:
            self.record("speaker", False,
                        "the page never played the motif (a press held for a "
                        "second, then a tap, starts it)")
            return
        if not box.get("params"):
            self.record("speaker", False, "roadie has no /audio to listen to")
            return
        ok, detail = motif_verdict(path)
        via = max(set(sources), key=sources.count) if sources else None
        if via:
            detail += ", via " + ("the USB gadget" if via == "usb" else via.upper())
        if ok and backend == "linux-gadget" and via != "usb":
            ok = False
            detail += (" -- not the gadget's speaker: the phone chose another "
                       "output (`roadie-gadget reset` moves iOS back to USB)")
        if not ok and started.get("state") != "running":
            detail += f"; the page's audio was {started.get('state')}"
        self.record("speaker", ok, f"{detail} ({path})")

    def tap(self, spot):
        """Tap a place on the screen, given as fractions of it. The pointer
        rests there first: iOS hides it after a key, and a click before it
        shows again goes nowhere."""
        hx, hy = round(spot[0] * HID_MAX), round(spot[1] * HID_MAX)
        self.move(hx - 300, hy - 300)
        time.sleep(0.3)
        self.move(hx, hy)
        time.sleep(1.5)
        self.hid("mouse/click", {"buttons": 1})

    def leg_microphone(self, soundcheck, page_url):
        """Roadie's microphone into the phone, heard coming back out.

        On the soundcheck page (see SOUNDCHECK): Mic records for a few
        seconds, then plays back what it heard. Roadie sends the motif
        while it records and listens on /audio for the playback, so the
        page needs no way back to this script. Then Safari goes back to
        the test page.
        """
        from test_audio import TONE_SECS, make_tone, stream_api
        secs = math.ceil(TONE_SECS) + 6          # room for Safari's prompt
        path = "/tmp/roadie_e2e_mic.wav"
        sep = "&" if "?" in soundcheck else "?"
        safari_go(self.base, f"{soundcheck}{sep}seconds={secs}")
        time.sleep(5.0)          # the page can't say it loaded: give it time
        box = {}
        rec = threading.Thread(target=lambda: box.update(
            params=record_target_audio(self.base, 2 * secs + 8, path)),
            daemon=True)
        rec.start()
        self.tap(MIC_BUTTON)
        time.sleep(1.5)
        self.tap(ALLOW_MIC)
        time.sleep(1.0)
        stream_api(make_tone(), self.base)
        rec.join()
        if not box.get("params"):
            self.record("microphone", False, "roadie has no /audio to listen to")
        else:
            ok, detail = motif_verdict(path)
            if not ok:
                shot = "/tmp/roadie_e2e_mic.jpg"
                try:
                    with urllib.request.urlopen(f"{self.base}/snapshot", timeout=5) as r:
                        Path(shot).write_bytes(r.read())
                    detail += f"; the phone's screen at the end: {shot}"
                except OSError:
                    pass
                detail += f" (from {soundcheck})"
            self.record("microphone", ok, f"{detail} ({path})")
        self.back_to_page(page_url)

    def back_to_page(self, url):
        """Back to the test page: Safari's Back (Cmd+[), or the URL again."""
        def back(timeout):
            return self.collector.wait_for(
                lambda evs: next((e for e in evs if e.get("kind") == "ready"
                                  or (e.get("kind") == "visible"
                                      and e.get("visible"))), None), timeout)

        self.collector.drain()
        hid_key(self.base, KEY["escape"], "click")
        time.sleep(0.4)
        hid_combo(self.base, "gui", "[")
        if back(5):
            return
        safari_go(self.base, url)
        if not back(10):
            warn("Couldn't get Safari back to the test page")

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

LEGS = ["mouse", "click", "keyboard", "scroll", "touch", "multitouch", "drag",
        "speaker", "mic"]

# What the HUD types when no --text is given.
HUD_TEXT = "Diagnostics complete. Hands-free control at 100%."


def host_model():
    """What this machine is, for the HUD: the board's own name if it has one."""
    try:
        return Path("/proc/device-tree/model").read_text().strip("\x00\n ")
    except OSError:
        return socket.gethostname()


def main():
    parser = argparse.ArgumentParser(
        description="End-to-end input test through roadie and a real browser.")
    parser.add_argument("--target", default="pi",
                        choices=["pi", "android", "iphone", "pc"],
                        help="which device the HID board is plugged into")
    parser.add_argument("--roadie", default="http://127.0.0.1:8080",
                        help="base URL of a running roadie "
                             "(one is started if this isn't answering)")
    parser.add_argument("--text",
                        help="text to type through the chain (default "
                             f"'roadie e2e 123', or {HUD_TEXT!r} with --hud)")
    parser.add_argument("--browser", help="browser binary to use")
    parser.add_argument("--display", default=os.environ.get("DISPLAY") or ":0")
    parser.add_argument("--xauthority",
                        default=os.environ.get("XAUTHORITY")
                        or str(Path.home() / ".Xauthority"))
    parser.add_argument("--tolerance", type=int, default=12,
                        help="allowed coordinate error in screen pixels")
    parser.add_argument("--settle", type=float, default=0.6,
                        help="seconds to wait for events after each command")
    parser.add_argument("--open", choices=("keyboard", "adb"), default="keyboard",
                        help="how to get the page open on a phone: with the "
                             "HID keyboard roadie already is, or over adb")
    parser.add_argument("--ready-timeout", type=float, default=60,
                        help="seconds to wait for the page to load")
    parser.add_argument("--skip", action="append", default=[], choices=LEGS,
                        help="skip a leg (repeatable)")
    parser.add_argument("--keep-open", action="store_true",
                        help="leave the browser running at the end")
    parser.add_argument("--soundcheck", default=SOUNDCHECK,
                        help="the soundcheck page (soundcheck/index.html) "
                             "the iPhone's microphone is checked on; it has to "
                             "be served over HTTPS")
    parser.add_argument("--hud", action="store_true",
                        help="show the run as a sci-fi HUD, paced for a "
                             "recording; the checks are the same")
    args = parser.parse_args()
    if args.text is None:
        args.text = HUD_TEXT if args.hud else "roadie e2e 123"
    if args.hud:
        args.settle = max(args.settle, 0.9)

    if not PAGE.exists():
        fail(f"Test page missing: {PAGE}")

    heading(f"test-e2e-{args.target}",
            "input through roadie's API, checked in a real browser")
    step("Harness")
    local = args.target == "pi"
    collector = Collector(hud=args.hud)
    if local:
        port = collector.start("127.0.0.1")
        url = f"http://127.0.0.1:{port}/"
    else:
        # A phone or another PC has to be able to reach us.
        port = collector.start("0.0.0.0")
        url = f"http://{lan_address()}:{port}/"
    check("collector", url)

    platform = {"android": "android", "iphone": "ios"}.get(args.target)
    roadie = Roadie(args.roadie, platform=platform)
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
    check("relay", status.get("port"))
    collector.set_status(link={
        "host": host_model(),
        "backend": status.get("backend", "rp2040"),
        "usb_state": status.get("usb_state") or status.get("status"),
        "target": args.target,
    })

    step(f"Opening the test page on target '{args.target}'")
    browser = None
    if local:
        browser = Browser(detect_browser(args.browser), url,
                          args.display, args.xauthority)
        started["browser"] = browser
        browser.launch()
    elif args.target in ("android", "iphone"):
        if args.open == "adb":
            open_on_android(url)
        else:
            open_with_keyboard(base, url, args.target)
    else:
        ask_by_hand(url, args.target)

    note(f"waiting up to {args.ready_timeout:.0f}s for the page")
    if not collector.ready.wait(args.ready_timeout):
        # The keyboard shortcuts are best-effort: launchers differ, a phone
        # may be locked, and a browser may not be where the shortcut points.
        # Ask rather than give up -- the harness is already standing.
        if args.target in ("android", "iphone") and args.open != "adb":
            print()
            print("  ⌨️  The shortcuts did not get there. Hand finish, maybe:")
            ask_by_hand(url, args.target)
            collector.ready.wait(args.ready_timeout)

    if not collector.ready.is_set():
        if browser:
            browser.stop()
        roadie.stop()
        collector.stop()
        if local:
            fail("The page never loaded. Is the display awake and unlocked?")
        fail(f"The {args.target} never loaded the page. Check it can reach "
             f"{url} on this network, then try --ready-timeout 180.")

    geom = collector.ready_info
    check("page ready", f"screen {geom['screenW']}x{geom['screenH']}, "
         f"viewport {geom['innerW']}x{geom['innerH']}, dpr {geom['dpr']}")
    check("touch points", f"navigator.maxTouchPoints = {geom.get('maxTouchPoints')}")
    ios = args.target == "iphone"
    # A drag stands in for the wheel on iOS, and finishes a HUD run.
    drag = "drag" not in args.skip and (ios or args.hud)
    plan = [(leg, title, checks) for leg, title, checks, wanted in [
        ("mouse", "POINTER CALIBRATION", 3, True),
        ("click", "TACTILE RESPONSE", 1, True),
        ("keyboard", "KEYBOARD UPLINK", 1, True),
        ("scroll", "SCROLL VECTOR", 1, not ios),
        ("touch", "TOUCH SURFACE", 1, not ios),
        ("multitouch", "MULTI-CONTACT", 1, not ios),
        ("drag", "SIGNATURE TRACE", 1, drag),
        ("speaker", "SOUNDCHECK \u00b7 SPEAKER", 1, ios),
        ("mic", "SOUNDCHECK \u00b7 MIC", 1, ios),
    ] if wanted and leg not in args.skip]
    collector.set_status(plan=[{"id": leg, "title": title, "checks": checks}
                               for leg, title, checks in plan])
    # Let the kiosk window settle before driving it; with the HUD, let it
    # finish booting too.
    time.sleep(6.5 if args.hud else 1.0)

    step("Checks")
    suite = Suite(base, collector, geom, args.tolerance, args.settle,
                  local=local, page_relative=ios, hud=args.hud)
    try:
        if local:
            suite.focus_window()
        if "mouse" not in args.skip:
            suite.begin("mouse")
            if ios:
                # Positions are kept inside the page: the screen corners are
                # Safari's own furniture, where a press never reaches the
                # page at all.
                for frac in (0.25, 0.5, 0.75):
                    suite.leg_pointer_position(round(HID_MAX * frac),
                                               round(HID_MAX * frac))
            else:
                suite.leg_mouse_move(0, 0)
                suite.leg_mouse_move(HID_MAX, HID_MAX)
                suite.leg_mouse_move(HID_MAX // 2, HID_MAX // 4)
            suite.beat()
        if "click" not in args.skip:
            suite.begin("click")
            suite.leg_mouse_click()
            suite.beat()
        if "keyboard" not in args.skip:
            suite.begin("keyboard")
            suite.leg_keyboard(args.text)
            suite.beat()
        if "scroll" not in args.skip and not ios:
            suite.begin("scroll")
            suite.leg_scroll()
            suite.beat()
        elif ios:
            suite.begin(None)
            suite.record("mouse scroll", True,
                         "skipped — a touch pointer has no wheel; iOS scrolls "
                         "by dragging")
        # iOS registers a mouse (through AssistiveTouch) but not an external
        # touch digitizer, so touch there goes nowhere. Skipping says that,
        # where running it would just look like a fault.
        if "touch" not in args.skip and not ios:
            suite.begin("touch")
            suite.leg_touch(HID_MAX // 4, HID_MAX * 3 // 4)
            suite.beat()
        elif ios:
            suite.record("touch", True,
                         "skipped — iOS does not register an external touch "
                         "digitizer, only a mouse via AssistiveTouch")
        if "multitouch" not in args.skip and not ios:
            suite.begin("multitouch")
            suite.leg_multitouch()
            suite.beat()
        if drag:
            suite.begin("drag")
            suite.leg_trace()
        # The soundcheck, both ways through the phone. iPhone only so far:
        # the taps on the soundcheck page are placed for Safari there.
        if ios and "speaker" not in args.skip:
            suite.beat()
            suite.begin("speaker")
            suite.leg_speaker(status.get("backend", "rp2040"))
        if ios and "mic" not in args.skip:
            suite.beat()
            suite.begin("mic")
            suite.leg_microphone(args.soundcheck, url)
        if args.hud:
            # Park the pointer in a corner, out of the finished picture.
            suite.move(HID_MAX // 16, HID_MAX - HID_MAX // 64)
    finally:
        suite.release_everything()
        collector.set_status(phase=None, done=True)
        if args.hud:
            time.sleep(3.0)  # long enough for the page to see the finish
        # Hold the window open briefly so the run is watchable.
        linger = float(os.environ.get("ROADIE_E2E_LINGER", "2"))
        if browser and not args.keep_open and linger > 0:
            time.sleep(linger)
        if browser and args.keep_open:
            warn(f"Leaving the browser open. Kill it with: "
                 f"pkill -f {browser.profile}")
        elif browser:
            browser.stop()
        roadie.stop()
        collector.stop()

    passed = sum(1 for _, ok, _ in suite.results if ok)
    total = len(suite.results)
    print()
    if passed == total:
        result(f"test-e2e-{args.target:<9} {passed}/{total} checks")
    else:
        print(f"  ❌ test-e2e-{args.target:<9} {passed}/{total} checks")
    for name, ok, detail in suite.results:
        if not ok:
            print(f"     ❌ {name}: {detail}")
    return 0 if passed == total else 1


if __name__ == "__main__":
    sys.exit(main())
