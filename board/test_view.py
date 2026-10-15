#!/usr/bin/env python3
"""
Voice input test through roadie's own /view page.

The other audio tests supply their own page. This one drives the real
viewer UI, the way an operator does: find the microphone button, click it
with the actual USB mouse, and check the tune comes out of the target's
microphone.

Two things are under test that nothing else covers:

  * /view's mic button really starts voice input when clicked -- not when
    a script calls startTalk(), which is a different thing.
  * a real HID click lands on the right pixel. The button is found by
    asking the browser where it is, so the test clicks what the page
    actually drew rather than a hard-coded coordinate.

The button is located over WebDriver BiDi using the vibium CLI, which
attaches to a browser this script launches (see "Remote Browser Control"
in the vibium docs). Launching it here is what lets the browser have a
file playing in place of a microphone -- and it means vibium never has to
fetch a browser of its own, which is the part that has no arm64 build.

Usage:
    python3 board/test_view.py           # on a Pi with both boards attached

Requires: both boards plugged in over USB and wired together over UART,
the HID board's mic enumerated as ALSA card "RoadieHID", chromium,
chromedriver, and node (for npx vibium).
"""

import argparse
import json
import math
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import wave
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "shared"))

from protocol import AUDIO_RATE  # noqa: E402
from test_audio import MOTIF, TONE_SECS, analyze, make_tone  # noqa: E402
from test_e2e import Roadie, free_port, http_json, reap_old_profiles  # noqa: E402
from testout import check, fail, heading, result  # noqa: E402

WAV_PATH = "/tmp/roadie_audio_view.wav"
# Silence at the head of the file that stands in for the microphone, so the
# tune still starts after the capture is running.
LEAD_SECS = 1.0
SESSION = "roadie-view"          # a named vibium session, so a developer's
                                 # own daemon is left alone
VERBOSE = False


def detail(*args):
    if VERBOSE:
        print(*args)


# ---------------------------------------------------------------------------
# vibium
# ---------------------------------------------------------------------------

def vibium_command():
    """How to invoke the vibium CLI.

    npx re-resolves the package on every call, which adds up over a dozen
    of them, so prefer a real binary on PATH and fall back to npx.
    """
    direct = shutil.which("vibium")
    if direct:
        return [direct]
    cached = sorted(Path.home().glob(".npm/_npx/*/node_modules/.bin/vibium"))
    if cached:
        return [str(cached[-1])]
    if not shutil.which("npx"):
        fail("Neither vibium nor npx found — install node, or: npm i -g vibium")
    return ["npx", "--yes", "vibium"]


class Vibium:
    """The vibium CLI, attached to a browser we launched ourselves."""

    def __init__(self, driver_url, caps):
        self.base = vibium_command() + ["--session", SESSION]
        self.driver_url = driver_url.rstrip("/")
        self.caps = caps
        self.env = dict(os.environ)
        self.session_id = None
        self.started = False

    def run(self, *args, timeout=90, check=True):
        proc = subprocess.run(self.base + list(args), env=self.env,
                              capture_output=True, text=True, timeout=timeout)
        out = (proc.stdout or "").strip()
        if check and proc.returncode != 0:
            err = (proc.stderr or "").strip() or out
            fail(f"vibium {' '.join(args)} failed: {err}")
        return out

    def start(self):
        """Attach vibium to a browser of ours, over BiDi.

        vibium takes either a browser-level endpoint, where it creates the
        session, or the socket of a session that already exists. We want
        the second: the session has to be created with our own capabilities
        -- a file standing in for the microphone -- and it is chromedriver
        that knows how to do that.
        """
        ws = self._create_session()
        out = self.run("start", ws)
        self.started = True
        detail(f"  🔗 {out}")

    def _create_session(self):
        """Ask chromedriver for a session, and return its BiDi socket."""
        body = json.dumps({"capabilities": {"alwaysMatch": dict(
            self.caps, webSocketUrl=True)}}).encode()
        req = urllib.request.Request(
            f"{self.driver_url}/session", data=body,
            headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                payload = json.load(resp)
        except urllib.error.HTTPError as exc:
            fail(f"chromedriver refused the session: "
                 f"{exc.read().decode(errors='replace')[:400]}")
        except OSError as exc:
            fail(f"Could not reach chromedriver: {exc}")
        value = payload.get("value", {})
        self.session_id = value.get("sessionId")
        ws = value.get("capabilities", {}).get("webSocketUrl")
        if not ws:
            fail("chromedriver returned no webSocketUrl, so it cannot be "
                 "driven over BiDi")
        return ws

    def eval_json(self, expr):
        """Evaluate an expression that returns a JSON string."""
        out = self.run("eval", expr)
        # --json wraps the result; the bare form prints it. Accept either.
        try:
            parsed = json.loads(out)
        except json.JSONDecodeError:
            fail(f"vibium eval gave no JSON: {out[:200]}")
        if isinstance(parsed, dict) and "result" in parsed:
            parsed = parsed["result"]
        if isinstance(parsed, str):
            parsed = json.loads(parsed)
        return parsed

    def stop(self):
        if self.started:
            self.run("stop", check=False, timeout=60)
            self.started = False
        if self.session_id:
            # vibium attached to a session it did not create, so closing the
            # browser is ours to do.
            req = urllib.request.Request(
                f"{self.driver_url}/session/{self.session_id}", method="DELETE")
            try:
                urllib.request.urlopen(req, timeout=30).close()
            except (OSError, urllib.error.HTTPError):
                pass
            self.session_id = None


# ---------------------------------------------------------------------------
# the page
# ---------------------------------------------------------------------------

# Everything the test needs to know about the button, in one round trip:
# where it is, whether the page thinks voice input is available, and
# whether it is currently talking.
PROBE_JS = """(() => {
  const b = document.getElementById('ptt');
  if (!b) return JSON.stringify({found: false});
  const r = b.getBoundingClientRect();
  return JSON.stringify({
    found: true,
    cx: r.x + r.width / 2, cy: r.y + r.height / 2,
    w: r.width, h: r.height,
    title: b.title, opacity: b.style.opacity,
    talking: /180, ?40, ?40/.test(b.style.background),
    screenX: window.screenX, screenY: window.screenY,
    outerW: window.outerWidth, innerW: window.innerWidth,
    outerH: window.outerHeight, innerH: window.innerHeight,
    dpr: window.devicePixelRatio,
    secure: window.isSecureContext
  });
})()"""


def screen_size(display):
    """The X screen in device pixels, which is what the HID mouse spans."""
    try:
        out = subprocess.run(["xdpyinfo"], env=dict(os.environ, DISPLAY=display),
                             capture_output=True, text=True, timeout=20).stdout
        for line in out.splitlines():
            if "dimensions:" in line:
                w, h = line.split()[1].split("x")
                return int(w), int(h)
    except (OSError, subprocess.SubprocessError, ValueError):
        pass
    return None


def to_screen(probe, screen):
    """Button centre, page coordinates → absolute mouse coordinates.

    The window's own offset and its chrome are added back in, so this
    holds whether the browser is fullscreen or in a window.
    """
    dpr = probe.get("dpr") or 1.0
    border = max(0.0, (probe["outerW"] - probe["innerW"]) / 2.0)
    chrome = max(0.0, probe["outerH"] - probe["innerH"])
    x = (probe["screenX"] + border + probe["cx"]) * dpr
    y = (probe["screenY"] + chrome + probe["cy"]) * dpr
    sw, sh = screen
    if not (0 <= x < sw and 0 <= y < sh):
        fail(f"Button computes to ({x:.0f}, {y:.0f}), off a {sw}x{sh} screen")
    # roadie's absolute mouse spans the screen in 0-32767.
    return round(x / sw * 32767), round(y / sh * 32767), x, y


def hid_click(base, nx, ny):
    """Move the real mouse there and press the left button."""
    http_json(f"{base}/api/hid/mouse/move", {"x": nx, "y": ny})
    # Moving and clicking in one breath can beat the page's own hover
    # handling; a beat between them is how a hand does it anyway.
    time.sleep(0.3)
    http_json(f"{base}/api/hid/mouse/click", {"buttons": 1})


def click_mic_button(vib, base, screen):
    """Find the mic button and click it with the real mouse.

    The position is read immediately before clicking rather than reused
    from earlier. /view reflows while it is open -- when roadie picks up a
    capture device the control bar moves down the screen -- and a stale
    coordinate clicks whatever has taken that spot.
    """
    probe = vib.eval_json(PROBE_JS)
    if not probe.get("found"):
        fail("The #ptt button is no longer on the page")
    nx, ny, px, py = to_screen(probe, screen)
    detail(f"  🎯 Button at ({px:.0f}, {py:.0f}) on screen → mouse ({nx}, {ny})")
    hid_click(base, nx, ny)
    return probe


def wait_for(vib, want, what, timeout=20):
    """Poll the page until the button reaches a state."""
    deadline = time.monotonic() + timeout
    probe = None
    while time.monotonic() < deadline:
        probe = vib.eval_json(PROBE_JS)
        if probe.get("talking") is want:
            return probe
        time.sleep(0.5)
    fail(f"The mic button never {what} — the click did not take effect. "
         f"Last state: talking={probe.get('talking') if probe else '?'}")


def record(seconds):
    seconds = math.ceil(seconds)  # arecord -d takes whole seconds
    detail(f"  🎙️  Recording {seconds}s from ALSA card RoadieHID...")
    rec = subprocess.Popen(
        ["arecord", "-q", "-D", "plughw:RoadieHID", "-f", "S16_LE",
         "-r", str(AUDIO_RATE), "-c", "1", "-d", str(seconds), WAV_PATH],
        stderr=subprocess.PIPE, text=True)
    time.sleep(0.8)
    return rec


def finish_recording(rec):
    """Wait for the capture, and say so if it did not work.

    Without this an unusable capture shows up later as a missing file,
    which says nothing about the microphone being busy or absent.
    """
    _, err = rec.communicate()
    if rec.returncode != 0 or not os.path.exists(WAV_PATH):
        message = (err or "").strip() or f"arecord exited {rec.returncode}"
        fail(f"Could not record from the HID board's microphone: {message}")


# ---------------------------------------------------------------------------

def main():
    global VERBOSE
    parser = argparse.ArgumentParser(
        description="Test voice input through roadie's own /view page.")
    parser.add_argument("--base", default=None,
                        help="use a roadie already serving here")
    parser.add_argument("--display", default=os.environ.get("DISPLAY") or ":0")
    parser.add_argument("-v", "--verbose", action="store_true")
    args = parser.parse_args()
    VERBOSE = args.verbose

    chromium = shutil.which("chromium") or shutil.which("chromium-browser")
    if not chromium:
        fail("chromium not found — needed to play a file as the microphone")
    driver = shutil.which("chromedriver")
    if not driver:
        fail("chromedriver not found — install it (apt install chromium-driver)")
    if not shutil.which("arecord"):
        fail("arecord not found — install alsa-utils")

    screen = screen_size(args.display)
    if not screen:
        fail(f"No display at {args.display} — run: board/e2e/ensure-display.sh")

    heading("test-view", "roadie's own /view page, clicked with the real mouse")
    detail(f"     display {args.display}, {screen[0]}x{screen[1]}")

    # The browser's "microphone": a second of silence so the tune still
    # starts after recording begins, then the motif, then enough silence
    # that the file cannot loop back round into it mid-capture.
    pcm = make_tone()
    notes = " ".join(name for name, _, _ in MOTIF)
    fake_dir = Path(tempfile.mkdtemp(prefix="roadie-view-mic-"))
    fake_wav = fake_dir / "voice.wav"
    with wave.open(str(fake_wav), "w") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(AUDIO_RATE)
        w.writeframes(b"\x00\x00" * int(AUDIO_RATE * LEAD_SECS) + pcm
                      + b"\x00\x00" * int(AUDIO_RATE * 8))

    reap_old_profiles("roadie-view-profile-")
    reap_old_profiles("roadie-view-mic-")
    profile = tempfile.mkdtemp(prefix="roadie-view-profile-")

    roadie = Roadie(args.base, quiet=not VERBOSE)
    vib = None
    driver_proc = None
    rec = None
    try:
        base = roadie.ensure()

        driver_port = free_port()
        driver_log = open("/tmp/roadie-view-chromedriver.log", "w")
        driver_proc = subprocess.Popen(
            [driver, f"--port={driver_port}", "--allowed-ips="],
            # chromedriver passes its own environment to the browser, so
            # this is what decides which screen the window opens on.
            env=dict(os.environ, DISPLAY=args.display),
            stdout=driver_log, stderr=subprocess.STDOUT)
        detail(f"  🧩 chromedriver on {driver_port}")

        caps = {"goog:chromeOptions": {
            "binary": chromium,
            # Chromium's "controlled by automated test software" notice
            # overlaps the top of the page, which is exactly where /view
            # puts its controls -- and it would shift the button we are
            # about to click at a real screen coordinate.
            "excludeSwitches": ["enable-automation"],
            "useAutomationExtension": False,
            "args": [
            "kiosk",                       # fullscreen, so the page owns the screen
            f"user-data-dir={profile}",
            "no-first-run", "no-default-browser-check", "noerrdialogs",
            # Otherwise the desktop puts up an "Unlock Login Keyring"
            # prompt, which takes focus away from the page being clicked.
            "password-store=basic", "use-mock-keychain",
            # Grant the mic without a prompt, and play our file into it.
            "use-fake-ui-for-media-stream", "use-fake-device-for-media-stream",
            f"use-file-for-fake-audio-capture={fake_wav}",
            "autoplay-policy=no-user-gesture-required",
            "disable-infobars",
        ]}}
        vib = Vibium(f"http://127.0.0.1:{driver_port}", caps)
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if driver_proc.poll() is not None:
                fail("chromedriver exited — see /tmp/roadie-view-chromedriver.log")
            try:
                urllib.request.urlopen(
                    f"http://127.0.0.1:{driver_port}/status", timeout=2).close()
                break
            except OSError:
                time.sleep(0.3)
        else:
            fail("chromedriver never became ready")

        vib.start()
        check("vibium attached", "to our own chromium, over WebDriver BiDi")

        # 127.0.0.1, not the LAN address: getUserMedia only works in a
        # secure context, and /view disables the button without one.
        view = f"{base}/view"
        vib.run("go", view)
        check("page opened", view)

        probe = vib.eval_json(PROBE_JS)
        if not probe.get("found"):
            fail("No #ptt button on /view — has the viewer page changed?")
        if not probe.get("secure"):
            fail(f"{view} is not a secure context, so /view disables voice "
                 f"input. It needs to be reached over localhost or https.")
        if probe.get("title") != "Toggle microphone":
            fail(f"/view says voice input is unavailable: {probe.get('title')!r}")
        if probe.get("talking"):
            fail("The mic was already live before anything clicked it")

        # Worked out here so a bad coordinate is reported before anything
        # else happens; the click locates the button again for itself.
        _, _, px, py = to_screen(probe, screen)
        check("mic button found", f"at ({px:.0f}, {py:.0f}) on screen")

        # Recording starts first: the browser begins playing the file that
        # stands in for its microphone as soon as the click grants access,
        # and checking that the click worked takes a second or two of its
        # own. Starting the capture first means those seconds cost nothing.
        # Where the notes fall in the recording does not matter -- they are
        # found by energy, not by position.
        detail(f"  🔊 The browser's microphone plays {TONE_SECS:.1f}s — {notes}")
        rec = record(TONE_SECS + LEAD_SECS + 6)

        click_mic_button(vib, base, screen)
        wait_for(vib, True, "turned on")
        check("hardware click", "the real USB mouse started voice input")

        finish_recording(rec)
        rec = None

        click_mic_button(vib, base, screen)
        wait_for(vib, False, "turned off")
        check("second click", "voice input stopped")
    finally:
        if rec is not None and rec.poll() is None:
            rec.terminate()
        if vib:
            vib.stop()
        if driver_proc and driver_proc.poll() is None:
            driver_proc.terminate()
            try:
                driver_proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                driver_proc.kill()
        roadie.stop()
        shutil.rmtree(profile, ignore_errors=True)
        shutil.rmtree(fake_dir, ignore_errors=True)

    # What actually came out of the target's microphone.
    peak, heard, dropouts = analyze(WAV_PATH)
    if peak < 500:
        print("  ❌ No signal captured — the click landed but no audio arrived.")
        print(f"     The capture is at {WAV_PATH}.")
        sys.exit(1)

    ok = len(heard) == len(MOTIF)
    rows = []
    for i, (name, want, secs) in enumerate(MOTIF):
        if i < len(heard):
            got, note_rms = heard[i]
            close = abs(got - want) <= want * 0.06
            ok = ok and close
            rows.append(f"     {'✅' if close else '❌'} {name:<3} "
                        f"expected {want:6.1f} Hz, heard {got:6.1f} Hz "
                        f"({secs:.2f}s, RMS {note_rms:.0f})")
        else:
            ok = False
            rows.append(f"     ❌ {name:<3} expected {want:6.1f} Hz, not heard")
    if not ok or VERBOSE:
        print(f"  📈 Heard {len(heard)} of {len(MOTIF)} notes:")
        for row in rows:
            print(row)

    lost = sum(dropouts)
    if dropouts:
        print(f"  ⚠️  {len(dropouts)} dropout(s) mid-tune, {lost * 1000:.0f} ms "
              f"lost — the audio had gaps in it")
    if lost > 0.5:
        ok = False
        print("  ❌ Too much audio was lost to call the path healthy.")
    if not ok:
        print("  ❌ The motif did not arrive intact.")
        sys.exit(1)
    print()
    result(f"test-view          {len(MOTIF)}/{len(MOTIF)} notes   {WAV_PATH}")


if __name__ == "__main__":
    main()
