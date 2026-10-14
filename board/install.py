#!/usr/bin/env python3
"""
Provisioning script for roadie CircuitPython boards.

Usage:
    python3 board/install.py <relay|hid> [--skip-firmware] [--cp-version VERSION]
    python3 board/install.py --setup-only

Handles the full lifecycle:
  1. Detect board state (raw, CircuitPython, or already in bootloader)
  2. Enter UF2 bootloader (via serial REPL or guided manual steps)
  3. Download + flash CircuitPython firmware
  4. Clean, install libraries, copy board files
  5. Eject

Supports macOS and Linux (Raspberry Pi).
"""

import argparse
import glob
import os
import shutil
import subprocess
import sys
import time
import re
import urllib.request
from pathlib import Path

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

SCRIPT_DIR = Path(__file__).resolve().parent   # board/
REPO_ROOT = SCRIPT_DIR.parent                  # repo root
VENV_DIR = REPO_ROOT / ".venv"

# ---------------------------------------------------------------------------
# Board + platform config
# ---------------------------------------------------------------------------

BOARDS = {
    "relay": {
        "volume": "ROADIE_RLY", "label": "📥 IN", "name": "Relay",
        "product": "Roadie-Relay",
    },
    "hid": {
        "volume": "ROADIE_HID", "label": "📤 OUT", "name": "HID",
        "product": "Roadie-HID",
    },
}

# All volume names we might find a CircuitPython board mounted as
VOLUME_NAMES = ["CIRCUITPY"] + [b["volume"] for b in BOARDS.values()]

BOARD_ID = "adafruit_qtpy_rp2040"
# usb_audio (the HID board's voice-input microphone) needs CircuitPython 10.3+.
CP_VERSION_DEFAULT = "10.3.1"
UF2_URL_TEMPLATE = (
    "https://downloads.circuitpython.org/bin/{board}/en_US/"
    "adafruit-circuitpython-{board}-en_US-{version}.uf2"
)
CACHE_DIR = Path.home() / ".cache" / "roadie"

# Where to look up what versions exist for this board.
VERSIONS_URL = "https://circuitpython.org/board/{board}/"

# circuitpython.org rejects urllib's default User-Agent with a 403.
USER_AGENT = "roadie-install/1.0 (+https://github.com/VibiumDev/roadie)"

POLL_INTERVAL = 1   # seconds
POLL_TIMEOUT = 60   # seconds

# Filesystem needs a moment to settle after a volume mounts.
# Without this, operations on freshly-mounted FAT volumes can hit
# PermissionError or read-only errors on Linux.
MOUNT_SETTLE = 1    # seconds


def parse_version(text):
    """Turn "10.3.1" into (10, 3, 1); return None for pre-releases."""
    if "-" in text:
        return None  # alpha / beta / rc
    try:
        return tuple(int(part) for part in text.split("."))
    except ValueError:
        return None


def resolve_version(version):
    """Resolve the magic value "latest" to the newest stable release.

    Pre-releases are deliberately excluded — the board page lists things
    like 11.0.0-alpha.1 alongside the stable builds, and silently flashing
    an alpha is not what "latest" should mean.
    """
    if version != "latest":
        return version

    url = VERSIONS_URL.format(board=BOARD_ID)
    print(f"  🔎 Looking up the latest stable CircuitPython for {BOARD_ID}...")
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    try:
        with urllib.request.urlopen(request, timeout=30) as resp:
            html = resp.read().decode("utf-8", "replace")
    except Exception as e:
        fail(f"Couldn't reach {url} to resolve 'latest': {e}")

    prefix = f"adafruit-circuitpython-{BOARD_ID}-en_US-"
    found = {}
    for match in re.findall(
            re.escape(prefix) + r"([0-9][^\"\']*?)\.uf2", html):
        parsed = parse_version(match)
        if parsed:
            found[parsed] = match

    if not found:
        fail(f"No stable releases found on {url}. Pass --cp-version explicitly.")

    newest = found[max(found)]
    info(f"Latest stable is {newest}")
    return newest


def detect_platform():
    """Return a dict of platform-specific constants."""
    if sys.platform == "darwin":
        return {
            "name": "macOS",
            "volume_boot": "/Volumes/RPI-RP2",
            "volume_base": "/Volumes",
            "serial_patterns": ["/dev/tty.usbmodem*", "/dev/cu.usbmodem*"],
        }
    elif sys.platform == "linux":
        user = os.environ.get("USER", os.environ.get("LOGNAME", "pi"))
        return {
            "name": "Linux",
            "volume_boot": f"/media/{user}/RPI-RP2",
            "volume_base": f"/media/{user}",
            "serial_patterns": ["/dev/ttyACM*"],
            "serial_by_id": "/dev/serial/by-id/usb-Adafruit_{product}_*-if00",
        }
    else:
        fail(f"Unsupported platform: {sys.platform}")


PLATFORM = detect_platform()

# ---------------------------------------------------------------------------
# Output helpers
# ---------------------------------------------------------------------------

def info(msg):
    print(f"  ✅ {msg}")

def warn(msg):
    print(f"  ⚠️  {msg}")

def step(msg):
    print(f"\n{'─' * 60}")
    print(f"  {msg}")
    print(f"{'─' * 60}")

def fail(msg):
    print(f"\n  ❌ {msg}")
    sys.exit(1)

# ---------------------------------------------------------------------------
# Volume helpers
# ---------------------------------------------------------------------------

def find_cp_volume(board=None):
    """Find a mounted CircuitPython volume.

    If board is specified, prefer that board's named volume.
    Falls back to any known volume name.
    """
    base = PLATFORM["volume_base"]
    if board and board in BOARDS:
        path = os.path.join(base, BOARDS[board]["volume"])
        if os.path.isdir(path):
            return path
    for name in VOLUME_NAMES:
        path = os.path.join(base, name)
        if os.path.isdir(path):
            return path
    return None


def wait_for_volume(path, timeout=POLL_TIMEOUT, appear=True):
    """Wait for a specific volume path to appear or disappear."""
    verb = "appear" if appear else "disappear"
    print(f"  ⏳ Waiting for {path} to {verb}...", end="", flush=True)
    elapsed = 0
    while elapsed < timeout:
        exists = os.path.isdir(path)
        if (appear and exists) or (not appear and not exists):
            print(" ok")
            return True
        time.sleep(POLL_INTERVAL)
        elapsed += POLL_INTERVAL
        print(".", end="", flush=True)
    print(" timeout!")
    return False


def wait_for_any_cp_volume(board=None, timeout=POLL_TIMEOUT):
    """Wait for any known CircuitPython volume to appear."""
    names = ", ".join(VOLUME_NAMES)
    print(f"  ⏳ Waiting for CircuitPython volume ({names})...", end="", flush=True)
    elapsed = 0
    while elapsed < timeout:
        vol = find_cp_volume(board)
        if vol:
            print(" ok")
            return vol
        time.sleep(POLL_INTERVAL)
        elapsed += POLL_INTERVAL
        print(".", end="", flush=True)
    print(" timeout!")
    return None

# ---------------------------------------------------------------------------
# Serial helpers
# ---------------------------------------------------------------------------

def find_serial_port():
    """Find the CircuitPython serial REPL port."""
    for pattern in PLATFORM["serial_patterns"]:
        matches = sorted(glob.glob(pattern))
        if matches:
            return matches[0]
    return None


def find_board_serial(board):
    """Find the REPL port belonging to one specific board.

    With both boards plugged in, the bare /dev/ttyACM* glob is ambiguous —
    it can hand back the other board and reboot the wrong one into the
    bootloader. Prefer the stable by-id path keyed on the USB product name.
    """
    pattern = PLATFORM.get("serial_by_id")
    product = BOARDS.get(board, {}).get("product")
    if pattern and product:
        matches = sorted(glob.glob(pattern.format(product=product)))
        if matches:
            return matches[0]
        return None

    # Platforms without by-id paths (macOS): fall back, but refuse to guess
    # when more than one candidate is present.
    ports = []
    for pat in PLATFORM["serial_patterns"]:
        ports.extend(sorted(glob.glob(pat)))
    if len(ports) > 1:
        warn(
            f"Multiple serial ports found ({', '.join(ports)}) and this "
            "platform can't tell them apart. Unplug the other board."
        )
        return None
    return ports[0] if ports else None


def send_repl_command(port, command):
    """Send a command to the CircuitPython REPL via serial."""
    try:
        import serial
    except ImportError:
        fail(
            "pyserial not installed. Run: make setup\n"
            "  (or: python3 board/install.py --setup-only)"
        )

    try:
        with serial.Serial(port, 115200, timeout=2) as ser:
            ser.write(b"\x03\x03")        # ctrl-C twice to interrupt
            time.sleep(0.5)
            ser.read(ser.in_waiting)       # flush
            ser.write(command.encode() + b"\r\n")
            time.sleep(0.5)
            return True
    except Exception as e:
        warn(f"Serial error: {e}")
        return False

# ---------------------------------------------------------------------------
# Eject helper
# ---------------------------------------------------------------------------

def eject_volume(path):
    """Sync and eject/unmount a volume, platform-aware."""
    subprocess.run(["sync"], check=False)

    if sys.platform == "darwin":
        result = subprocess.run(
            ["diskutil", "eject", path],
            capture_output=True, text=True,
        )
    elif sys.platform == "linux":
        result = subprocess.run(
            ["findmnt", "-n", "-o", "SOURCE", path],
            capture_output=True, text=True,
        )
        if result.returncode == 0 and result.stdout.strip():
            block_device = result.stdout.strip()
            result = subprocess.run(
                ["udisksctl", "unmount", "-b", block_device],
                capture_output=True, text=True,
            )
        else:
            result = subprocess.run(
                ["umount", path], capture_output=True, text=True,
            )
    else:
        warn("Don't know how to eject on this platform.")
        return

    if result.returncode == 0:
        info("Ejected.")
    else:
        warn("Couldn't auto-eject. Unplug and re-plug manually.")

# ---------------------------------------------------------------------------
# Setup: venv + tools
# ---------------------------------------------------------------------------

def ensure_venv():
    """Create a venv at repo root and install circup + pyserial."""
    if not VENV_DIR.exists():
        print(f"  🐍 Creating venv at {VENV_DIR}...")
        subprocess.run(
            [sys.executable, "-m", "venv", str(VENV_DIR)],
            check=True,
        )

    pip = VENV_DIR / "bin" / "pip"
    circup = VENV_DIR / "bin" / "circup"

    if not circup.exists():
        print("  📦 Installing circup and pyserial...")
        subprocess.run(
            [str(pip), "install", "--quiet", "circup", "pyserial"],
            check=True,
        )
    else:
        info("venv and tools already installed")

    return circup


def add_venv_to_path():
    """Add the venv's site-packages to sys.path so pyserial is importable."""
    venv_site = VENV_DIR / "lib"
    for p in venv_site.glob("python*/site-packages"):
        if str(p) not in sys.path:
            sys.path.insert(0, str(p))

# ---------------------------------------------------------------------------
# Phase 1: Enter bootloader
# ---------------------------------------------------------------------------

def ensure_bootloader(board=None):
    """Get the board into UF2 bootloader mode (RPI-RP2 mounted)."""
    step("Phase 1: Enter bootloader")

    volume_boot = PLATFORM["volume_boot"]
    volume_cp = find_cp_volume(board)

    if os.path.isdir(volume_boot):
        info(f"Board already in bootloader ({volume_boot} mounted)")
        return

    if volume_cp:
        info(f"CircuitPython detected ({volume_cp} mounted)")
        print("  🔄 Rebooting into UF2 bootloader via serial REPL...")

        port = find_board_serial(board)
        if port:
            info(f"Found serial port for {board}: {port}")
            cmd = (
                "import microcontroller; "
                "microcontroller.on_next_reset(microcontroller.RunMode.UF2); "
                "microcontroller.reset()"
            )
            send_repl_command(port, cmd)

            if wait_for_volume(volume_cp, timeout=10, appear=False):
                if wait_for_volume(volume_boot, timeout=15):
                    info("Board is now in bootloader mode")
                    return
            warn("Serial reboot didn't work. Falling through to manual mode.")
        else:
            warn(f"No serial port found for '{board}'. "
                 "Falling through to manual mode.")

    # Manual bootloader entry
    print()
    print("  📋 Manual bootloader entry required:")
    print()
    print("     1. Unplug the board from USB")
    print("     2. Hold down the BOOT button")
    print("     3. While holding BOOT, plug the board into USB")
    print("     4. Release BOOT after the RPI-RP2 drive appears")
    print()

    if not wait_for_volume(volume_boot, timeout=120):
        fail(f"Timed out waiting for {volume_boot}. Try again.")

    time.sleep(MOUNT_SETTLE)

# ---------------------------------------------------------------------------
# Phase 2: Flash CircuitPython firmware
# ---------------------------------------------------------------------------

def flash_firmware(version, board=None):
    """Download the CircuitPython UF2 and copy it to the bootloader drive."""
    step(f"Phase 2: Flash CircuitPython {version}")

    volume_boot = PLATFORM["volume_boot"]
    CACHE_DIR.mkdir(parents=True, exist_ok=True)
    uf2_name = f"adafruit-circuitpython-{BOARD_ID}-en_US-{version}.uf2"
    cached_uf2 = CACHE_DIR / uf2_name

    if cached_uf2.exists():
        info(f"Using cached firmware: {cached_uf2}")
    else:
        url = UF2_URL_TEMPLATE.format(board=BOARD_ID, version=version)
        print(f"  ⬇️  Downloading {url}")
        request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
        try:
            with urllib.request.urlopen(request, timeout=120) as resp:
                payload = resp.read()
            # Write via a temp file so an interrupted download can't leave a
            # truncated UF2 in the cache for the next run to flash.
            partial = cached_uf2.with_suffix(".uf2.part")
            partial.write_bytes(payload)
            partial.replace(cached_uf2)
            info(f"Downloaded to {cached_uf2}")
        except Exception as e:
            fail(f"Download failed: {e}")

    if not os.path.isdir(volume_boot):
        fail(f"{volume_boot} is not mounted")

    print(f"  📦 Copying firmware to {volume_boot}...")
    shutil.copy2(cached_uf2, os.path.join(volume_boot, uf2_name))

    info("Firmware copied. Board is rebooting...")

    if not wait_for_volume(volume_boot, timeout=15, appear=False):
        warn("RPI-RP2 didn't disappear — the UF2 may not have flashed.")

    # Volume may be CIRCUITPY (fresh) or a renamed volume (if old boot.py survived)
    if not wait_for_any_cp_volume(board=board, timeout=30):
        fail(
            "No CircuitPython volume appeared after flashing. "
            "Try unplugging and re-plugging the board."
        )

    info("CircuitPython is running!")

# ---------------------------------------------------------------------------
# Phase 3: Clean, install libraries, copy files
# ---------------------------------------------------------------------------

def clean_circuitpy(volume_cp):
    """Remove user files from CIRCUITPY, keeping only CP system files."""
    print(f"  🧹 Cleaning {volume_cp}...")
    time.sleep(MOUNT_SETTLE)

    keep = {"boot_out.txt", "sd", "settings.toml"}
    lib_dir = os.path.join(volume_cp, "lib")

    for name in os.listdir(volume_cp):
        if name.startswith(".") or name in keep:
            continue

        path = os.path.join(volume_cp, name)

        if name == "lib":
            for item in os.listdir(lib_dir):
                item_path = os.path.join(lib_dir, item)
                if os.path.isdir(item_path):
                    shutil.rmtree(item_path)
                else:
                    os.remove(item_path)
            info("lib/ emptied")
        elif os.path.isdir(path):
            shutil.rmtree(path)
            info(f"removed {name}/")
        else:
            os.remove(path)
            info(f"removed {name}")

    os.makedirs(lib_dir, exist_ok=True)


def install_board(board, circup_bin):
    """Clean the volume, install libs, and copy files for a board."""
    step(f"Phase 3: Install '{board}' board files")

    volume_cp = find_cp_volume(board)
    board_dir = SCRIPT_DIR / board

    if not board_dir.exists():
        fail(f"Board directory not found: {board_dir}")
    if not volume_cp:
        fail("No CircuitPython volume mounted")

    clean_circuitpy(volume_cp)

    # Install libs via circup
    req_file = board_dir / "requirements.txt"
    if req_file.exists():
        print(f"  📚 Installing libraries from {req_file.name}...")
        subprocess.run(
            [str(circup_bin), "--path", volume_cp, "install", "-r", str(req_file)],
            check=True,
        )
        info("Libraries installed")
    else:
        warn(f"No requirements.txt for {board}")

    # Copy board .py files to volume root
    print(f"  📋 Copying {board}/ files...")
    for f in sorted(board_dir.glob("*.py")):
        shutil.copy2(f, os.path.join(volume_cp, f.name))
        info(f"{f.name}")

    # Copy shared files to lib/
    shared_dir = SCRIPT_DIR / "shared"
    if shared_dir.exists() and any(shared_dir.glob("*.py")):
        lib_dir = os.path.join(volume_cp, "lib")
        os.makedirs(lib_dir, exist_ok=True)
        print(f"  📋 Copying shared/ files...")
        for f in sorted(shared_dir.glob("*.py")):
            shutil.copy2(f, os.path.join(lib_dir, f.name))
            info(f"{f.name} → lib/")

# ---------------------------------------------------------------------------
# Phase 4: Eject
# ---------------------------------------------------------------------------

def sync_board(board):
    """Copy board and shared .py files to the mounted volume without
    cleaning or reinstalling libraries.  Detects boot.py changes and
    advises a power-cycle when needed."""
    volume_cp = find_cp_volume(board)
    if not volume_cp:
        fail(f"Board '{board}' is not mounted. Plug it in first.")

    board_dir = SCRIPT_DIR / board
    if not board_dir.exists():
        fail(f"Board directory not found: {board_dir}")

    time.sleep(MOUNT_SETTLE)

    boot_changed = False

    # Copy board .py files
    for f in sorted(board_dir.glob("*.py")):
        dest = os.path.join(volume_cp, f.name)
        # Detect boot.py changes
        if f.name == "boot.py" and os.path.exists(dest):
            with open(f, "rb") as a, open(dest, "rb") as b:
                if a.read() != b.read():
                    boot_changed = True
        shutil.copy2(f, dest)

    # Copy shared files to lib/
    shared_dir = SCRIPT_DIR / "shared"
    if shared_dir.exists() and any(shared_dir.glob("*.py")):
        lib_dir = os.path.join(volume_cp, "lib")
        os.makedirs(lib_dir, exist_ok=True)
        for f in sorted(shared_dir.glob("*.py")):
            shutil.copy2(f, os.path.join(lib_dir, f.name))

    return boot_changed


def eject(board=None):
    """Eject the CircuitPython drive."""
    step("Phase 4: Eject")
    volume_cp = find_cp_volume(board)
    if volume_cp:
        eject_volume(volume_cp)
    else:
        warn("No CircuitPython volume found to eject.")

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(
        description="Provision a roadie CircuitPython board.",
    )
    parser.add_argument(
        "board", nargs="?", choices=BOARDS,
        help="Which board to flash: relay (📥 IN) or hid (📤 OUT)",
    )
    parser.add_argument(
        "--setup-only", action="store_true",
        help="Only create venv and install tools, then exit",
    )
    parser.add_argument(
        "--sync", action="store_true",
        help="Quick sync: copy .py files only (no clean, no lib reinstall)",
    )
    parser.add_argument(
        "--skip-firmware", action="store_true",
        help="Skip CircuitPython firmware install (board already has it)",
    )
    parser.add_argument(
        "--cp-version", default=CP_VERSION_DEFAULT,
        help=(
            f"CircuitPython version to install (default: {CP_VERSION_DEFAULT}). "
            "Use 'latest' to resolve the newest stable release, "
            "skipping alphas and betas."
        ),
    )

    args = parser.parse_args()

    # --setup-only mode
    if args.setup_only:
        step("Setup: install tools")
        ensure_venv()
        info("Done. Run 'make flash-hid' or 'make flash-relay' to provision a board.")
        return

    if not args.board and not args.sync:
        parser.error("board is required (relay or hid) unless using --setup-only or --sync")

    # --sync mode: lightweight file copy
    if args.sync:
        boards = [args.board] if args.board else list(BOARDS.keys())
        need_power_cycle = []

        for board in boards:
            volume_name = BOARDS[board]["volume"]
            volume_path = os.path.join(PLATFORM["volume_base"], volume_name)
            if not os.path.isdir(volume_path):
                fail(f"{volume_name} not mounted. Is the {board} board plugged in?")
            if sync_board(board):
                need_power_cycle.append(board)
            info(f"{BOARDS[board]['name']} synced.")
            # Board may remount after file changes trigger a reload.
            # Wait for it to settle, then eject. Retry if it remounts.
            time.sleep(MOUNT_SETTLE)
            for _ in range(3):
                if os.path.isdir(volume_path):
                    eject_volume(volume_path)
                    time.sleep(MOUNT_SETTLE)
                else:
                    break

        print()
        if need_power_cycle:
            labels = ", ".join(
                f"{b} ({BOARDS[b]['label']})" for b in need_power_cycle)
            print(f"  ⚠️  Action required: unplug and re-plug {labels}")
        print(f"  🎯 Done!")
        print()
        return

    board_info = BOARDS[args.board]
    cp_version = resolve_version(args.cp_version)

    print()
    print(f"  🎯 Provisioning board: {args.board}")
    print(f"     Platform: {PLATFORM['name']}")
    print(f"     CircuitPython: {cp_version}")
    print(f"     Skip firmware: {args.skip_firmware}")

    circup_bin = ensure_venv()
    add_venv_to_path()

    if not args.skip_firmware:
        ensure_bootloader(args.board)
        flash_firmware(cp_version, args.board)
    else:
        info("Skipping firmware install")
        if not find_cp_volume(args.board):
            print(f"\n  📋 Plug in a CircuitPython board.\n")
            if not wait_for_any_cp_volume(board=args.board, timeout=120):
                fail("Timed out waiting for CircuitPython volume.")

    install_board(args.board, circup_bin)
    eject(args.board)

    print()
    print(f"  🎯 Done! Board '{args.board}' is provisioned.")
    print(f"     Unplug and re-plug to start.")
    print(f"     The drive will mount as {board_info['volume']}.")
    print(f"     Label this board: {board_info['label']}")
    print()


if __name__ == "__main__":
    main()
