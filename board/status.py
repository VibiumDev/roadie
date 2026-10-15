#!/usr/bin/env python3
"""
Show the state of everything roadie talks to, in one place.

    python3 board/status.py

Covers the boards, the input roadie can drive on the target, what comes back
from the target, and this machine's own audio and displays. Read-only: it
starts nothing and changes nothing.
"""

import glob
import os
import re
import subprocess
import sys
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))

OK, WARN, BAD = "✅", "⚠️ ", "❌"


def run(cmd, env=None, timeout=10):
    """Run a command, returning stdout or "" — never raising."""
    try:
        full_env = dict(os.environ, **(env or {}))
        return subprocess.run(cmd, capture_output=True, text=True,
                              timeout=timeout, env=full_env).stdout
    except (OSError, subprocess.SubprocessError):
        return ""


def heading(text):
    print(f"\n\033[1m{text}\033[0m")


def row(label, mark, detail=""):
    print(f"  {mark} {label:<13} {detail}")


# ---------------------------------------------------------------------------
# The boards
# ---------------------------------------------------------------------------

def board_firmware(volume):
    """CircuitPython version, if the board's drive happens to be mounted."""
    boot_out = Path(f"/media/{os.environ.get('USER', 'pi')}/{volume}/boot_out.txt")
    try:
        first = boot_out.read_text().splitlines()[0]
    except (OSError, IndexError):
        return ""
    match = re.search(r"CircuitPython (\S+)", first)
    return match.group(1) if match else ""


def show_boards():
    heading("Boards")
    for name, product, volume, extra in (
        ("relay", "Roadie-Relay", "ROADIE_RLY", "-if02"),
        ("hid", "Roadie-HID", "ROADIE_HID", None),
    ):
        ports = sorted(glob.glob(f"/dev/serial/by-id/*{product}_*"))
        if not ports:
            row(name, BAD, "not connected")
            continue
        serial = re.search(r"_([0-9A-F]{16})-", ports[0])
        serial = serial.group(1) if serial else "?"
        version = board_firmware(volume)
        bits = [serial]
        if version:
            bits.append(f"CircuitPython {version}")
        if extra and not any(p.endswith(extra) for p in ports):
            bits.append(f"{WARN}no data port — re-plug it after flashing")
        row(name, OK, "  ".join(bits))


# ---------------------------------------------------------------------------
# What roadie can drive on the target
# ---------------------------------------------------------------------------

def show_target_input():
    heading("Input to the target")
    prefix = "/dev/input/by-id/usb-Adafruit_Roadie-HID_"
    wanted = (
        ("keyboard", "*-event-kbd"),
        ("mouse", "*-if03-event-mouse"),
        ("touch", "*-event-if03"),
    )
    found_any = False
    for label, pattern in wanted:
        matches = sorted(glob.glob(prefix + pattern))
        if not matches:
            row(label, BAD, "not present")
            continue
        found_any = True
        # The kernel's evdev node. Shown in full because a bare "event7" is
        # not obviously a path, and because the number is not stable -- it
        # changes whenever the board is re-plugged.
        detail = os.path.realpath(matches[0])
        if label == "touch":
            try:
                from test_hid import device_has_mt
                has_mt = device_has_mt(matches[0])
            except Exception:
                has_mt = None
            if has_mt is False:
                detail += "   single-touch (hid-generic, by roadie's udev rule)"
            elif has_mt:
                detail += "   multi-touch"
        row(label, OK, detail)

    if not found_any:
        row("", WARN, "plug the HID board into this machine to see these")

    # The board's USB microphone, which carries voice input to the target.
    cards = run(["arecord", "-l"])
    if "RoadieHID" in cards:
        rate = ""
        try:
            stream = Path("/proc/asound/RoadieHID/stream0").read_text()
            match = re.search(r"Rates: (\S+)", stream)
            rate = f"{match.group(1)} Hz" if match else ""
        except OSError:
            pass
        row("microphone", OK, f"ALSA card RoadieHID   {rate}".rstrip())
    else:
        row("microphone", WARN,
            "not enumerated — the HID board needs a re-plug after flashing")

    if found_any:
        print("     (event numbers change on re-plug; the stable names live")
        print("      under /dev/input/by-id/)")


# ---------------------------------------------------------------------------
# What comes back from the target
# ---------------------------------------------------------------------------

def show_target_output():
    heading("From the target")
    videos = sorted(glob.glob("/dev/video*"))
    # The Pi's own codecs occupy /dev/video10+; a dongle lands lower.
    dongles = [v for v in videos
               if int(re.sub(r"\D", "", v) or 99) < 10]
    if dongles:
        row("video", OK, f"capture device {', '.join(dongles)}")
    else:
        row("video", WARN, "no capture dongle detected (HDMI capture unavailable)")

    cards = Path("/proc/asound/cards").read_text() if \
        Path("/proc/asound/cards").exists() else ""
    usb_capture = [line for line in cards.splitlines()
                   if "USB-Audio" in line and "Roadie" not in line]
    if usb_capture:
        row("audio", OK, usb_capture[0].strip())
    else:
        row("audio", WARN, "no capture-dongle audio (comes with the dongle)")


# ---------------------------------------------------------------------------
# This machine
# ---------------------------------------------------------------------------

def pipewire_audio():
    """Default sink and source names from PipeWire, if it is running."""
    status = run(["wpctl", "status"], env={"XDG_RUNTIME_DIR": "/run/user/1000"})
    if not status:
        return None, None
    sink = source = None
    section = None
    for line in status.splitlines():
        if "Sinks:" in line:
            section = "sink"
        elif "Sources:" in line or "Filters:" in line:
            section = "source"
        elif "Streams:" in line or "Video" in line:
            section = None
        if section and "*" in line:
            name = re.sub(r"^.*\*\s*\d+\.\s*", "", line)
            name = re.sub(r"\s*\[.*$", "", name).strip()
            if name and section == "sink" and not sink:
                sink = name
            elif name and section == "source" and not source:
                source = name
    return sink, source


def show_this_machine():
    heading("This machine")

    displays = sorted(os.path.basename(s).replace("X", ":")
                      for s in glob.glob("/tmp/.X11-unix/X*"))
    row("displays", OK if displays else WARN,
        ", ".join(displays) if displays else "none (headless)")

    state = Path(os.environ.get("ROADIE_E2E_STATE_FILE",
                                "/tmp/roadie-e2e-display.state"))
    if state.exists():
        values = dict(line.split("=", 1) for line in
                      state.read_text().splitlines() if "=" in line)
        detail = f":{values.get('display', '?')}"
        if values.get("vnc_port"):
            detail += f"   VNC on {values['vnc_port']}"
        row("test display", OK, detail)
    else:
        row("test display", OK, "not running (a test will make its own)")

    sink, source = pipewire_audio()
    if sink is None:
        row("speakers", WARN, "no PipeWire server reachable")
    else:
        row("speakers", OK if sink else WARN, sink or "no default sink")
        row("microphone", OK if source else WARN, source or "no default source")

    port = None
    for line in run(["ss", "-ltnp"]).splitlines():
        if "roadie" in line:
            match = re.search(r":(\d+)\s", line)
            if match:
                port = match.group(1)
                break
    row("roadie", OK if port else WARN,
        f"serving on :{port}" if port else "not running")

    groups = run(["id", "-nG"]).split()
    row("input group", OK if "input" in groups else BAD,
        "ok" if "input" in groups
        else "missing — run 'make setup', then log out and back in")


def main():
    print("\n\033[1m🎸 roadie status\033[0m")
    show_boards()
    show_target_input()
    show_target_output()
    show_this_machine()
    print()
    print("  Tests: make test-hid | make test-e2e-pi | make test-audio")
    print()


if __name__ == "__main__":
    main()
