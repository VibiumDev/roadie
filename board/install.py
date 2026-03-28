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
  4. Install libraries via circup
  5. Copy board-specific + shared files
  6. Eject

Supports macOS and Linux (Raspberry Pi).
"""

import argparse
import glob
import os
import shutil
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

# board/install.py -> board/ -> repo root
SCRIPT_DIR = Path(__file__).resolve().parent          # board/
REPO_ROOT = SCRIPT_DIR.parent                         # repo root
VENV_DIR = REPO_ROOT / ".venv"

# ---------------------------------------------------------------------------
# Platform detection
# ---------------------------------------------------------------------------

VOLUME_NAMES = ["CIRCUITPY", "ROADIE_RLY", "ROADIE_HID"]


def detect_platform():
    """Return a dict of platform-specific constants."""
    if sys.platform == "darwin":
        return {
            "name": "macOS",
            "volume_boot": "/Volumes/RPI-RP2",
            "volume_base": "/Volumes",
            "serial_patterns": [
                "/dev/tty.usbmodem*",
                "/dev/cu.usbmodem*",
            ],
        }
    elif sys.platform == "linux":
        user = os.environ.get("USER", os.environ.get("LOGNAME", "pi"))
        return {
            "name": "Linux",
            "volume_boot": f"/media/{user}/RPI-RP2",
            "volume_base": f"/media/{user}",
            "serial_patterns": [
                "/dev/ttyACM*",
            ],
        }
    else:
        fail(f"Unsupported platform: {sys.platform}")


PLATFORM = detect_platform()


BOARD_VOLUMES = {
    "relay": "ROADIE_RLY",
    "hid": "ROADIE_HID",
}


def find_cp_volume(board=None):
    """Find a mounted CircuitPython volume.

    If board is specified, prefer that board's named volume.
    Falls back to any known volume (CIRCUITPY, ROADIE_RLY, ROADIE_HID).
    """
    base = PLATFORM["volume_base"]
    # prefer the specific board volume if we know which board
    if board and board in BOARD_VOLUMES:
        path = os.path.join(base, BOARD_VOLUMES[board])
        if os.path.isdir(path):
            return path
    for name in VOLUME_NAMES:
        path = os.path.join(base, name)
        if os.path.isdir(path):
            return path
    return None


# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

BOARD_ID = "adafruit_qtpy_rp2040"
CP_VERSION_DEFAULT = "10.1.3"
UF2_URL_TEMPLATE = (
    "https://downloads.circuitpython.org/bin/{board}/en_US/"
    "adafruit-circuitpython-{board}-en_US-{version}.uf2"
)

CACHE_DIR = Path.home() / ".cache" / "roadie"

BOARDS = ["relay", "hid"]

POLL_INTERVAL = 1   # seconds
POLL_TIMEOUT = 60   # seconds


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def wait_for_any_cp_volume(board=None, timeout=POLL_TIMEOUT):
    """Wait for any CircuitPython volume to appear."""
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


def wait_for_volume(path, timeout=POLL_TIMEOUT, appear=True):
    """Wait for a volume to appear or disappear."""
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


def find_serial_port():
    """Find the CircuitPython serial REPL port."""
    for pattern in PLATFORM["serial_patterns"]:
        matches = sorted(glob.glob(pattern))
        if matches:
            return matches[0]
    return None


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
            # interrupt any running program with ctrl-C twice
            ser.write(b"\x03\x03")
            time.sleep(0.5)
            ser.read(ser.in_waiting)  # flush

            # send command
            ser.write(command.encode() + b"\r\n")
            time.sleep(0.5)
            return True
    except Exception as e:
        warn(f"Serial error: {e}")
        return False


def eject_volume(path):
    """Sync and eject/unmount a volume, platform-aware."""
    subprocess.run(["sync"], check=False)

    if sys.platform == "darwin":
        result = subprocess.run(
            ["diskutil", "eject", path],
            capture_output=True, text=True,
        )
    elif sys.platform == "linux":
        # Find the block device for this mount point
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
            # fallback
            result = subprocess.run(
                ["umount", path],
                capture_output=True, text=True,
            )
    else:
        warn("Don't know how to eject on this platform.")
        return

    if result.returncode == 0:
        info("Ejected. Unplug and re-plug the board.")
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

    # Install/upgrade tools if circup isn't present
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

def ensure_bootloader():
    """Get the board into UF2 bootloader mode (RPI-RP2 mounted)."""
    step("Phase 1: Enter bootloader")

    volume_boot = PLATFORM["volume_boot"]
    volume_cp = find_cp_volume()

    if os.path.isdir(volume_boot):
        info(f"Board already in bootloader ({volume_boot} mounted)")
        return

    if volume_cp:
        info(f"CircuitPython detected ({volume_cp} mounted)")
        print("  🔄 Rebooting into UF2 bootloader via serial REPL...")

        port = find_serial_port()
        if port:
            info(f"Found serial port: {port}")
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
            warn("No serial port found. Falling through to manual mode.")

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

    # Give the filesystem a moment to settle after mounting
    time.sleep(1)


# ---------------------------------------------------------------------------
# Phase 2: Flash CircuitPython firmware
# ---------------------------------------------------------------------------

def flash_firmware(version):
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
        try:
            urllib.request.urlretrieve(url, cached_uf2)
            info(f"Downloaded to {cached_uf2}")
        except Exception as e:
            fail(f"Download failed: {e}")

    if not os.path.isdir(volume_boot):
        fail(f"{volume_boot} is not mounted")

    print(f"  📦 Copying firmware to {volume_boot}...")
    shutil.copy2(cached_uf2, os.path.join(volume_boot, uf2_name))

    # Board auto-reboots: RPI-RP2 disappears, CIRCUITPY appears
    info("Firmware copied. Board is rebooting...")

    if not wait_for_volume(volume_boot, timeout=15, appear=False):
        warn("RPI-RP2 didn't disappear — the UF2 may not have flashed.")

    # Volume may be CIRCUITPY (fresh) or a renamed volume (if old boot.py survived)
    if not wait_for_any_cp_volume(timeout=30):
        fail(
            "No CircuitPython volume appeared after flashing. "
            "Try unplugging and re-plugging the board."
        )

    info("CircuitPython is running!")


# ---------------------------------------------------------------------------
# Phase 3: Install libraries + copy files
# ---------------------------------------------------------------------------

def clean_circuitpy(volume_cp):
    """Remove user files from CIRCUITPY, leaving only CP system files."""
    print(f"  🧹 Cleaning {volume_cp}...")

    # Give the filesystem a moment to settle after mounting
    time.sleep(1)

    # Files/dirs that CircuitPython creates — leave these alone
    keep = {"boot_out.txt", "sd", "settings.toml"}

    lib_dir = os.path.join(volume_cp, "lib")

    for name in os.listdir(volume_cp):
        # Skip dotfiles (filesystem artifacts) and preserved items
        if name.startswith(".") or name in keep:
            continue

        path = os.path.join(volume_cp, name)

        if name == "lib":
            # Empty lib/ but keep the directory
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

    # Ensure lib/ exists even if it wasn't there
    os.makedirs(lib_dir, exist_ok=True)


def install_board(board, circup_bin):
    """Install libs and copy files for a specific board."""
    step(f"Phase 3: Install '{board}' board files")

    volume_cp = find_cp_volume(board)
    board_dir = SCRIPT_DIR / board

    if not board_dir.exists():
        fail(f"Board directory not found: {board_dir}")

    if not volume_cp:
        fail("No CircuitPython volume mounted")

    clean_circuitpy(volume_cp)

    # Install libs via circup, pointing at the correct path
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

    # Copy board-specific .py files to CIRCUITPY root
    print(f"  📋 Copying {board}/ files...")
    for f in sorted(board_dir.glob("*.py")):
        dest = os.path.join(volume_cp, f.name)
        shutil.copy2(f, dest)
        info(f"{f.name}")

    # Copy shared files to CIRCUITPY lib/
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
        "board",
        nargs="?",
        choices=BOARDS,
        help="Which board to flash: relay (📥 IN) or hid (📤 OUT)",
    )
    parser.add_argument(
        "--setup-only",
        action="store_true",
        help="Only create venv and install tools, then exit",
    )
    parser.add_argument(
        "--skip-firmware",
        action="store_true",
        help="Skip CircuitPython firmware install (board already has it)",
    )
    parser.add_argument(
        "--cp-version",
        default=CP_VERSION_DEFAULT,
        help=f"CircuitPython version to install (default: {CP_VERSION_DEFAULT})",
    )

    args = parser.parse_args()

    # --setup-only mode
    if args.setup_only:
        step("Setup: install tools")
        ensure_venv()
        info("Done. Run 'make flash-hid' or 'make flash-relay' to provision a board.")
        return

    # Normal mode requires a board argument
    if not args.board:
        parser.error("board is required (relay or hid) unless using --setup-only")

    print()
    print(f"  🎯 Provisioning board: {args.board}")
    print(f"     Platform: {PLATFORM['name']}")
    print(f"     CircuitPython: {args.cp_version}")
    print(f"     Skip firmware: {args.skip_firmware}")

    # Set up venv so circup and pyserial are available
    circup_bin = ensure_venv()
    add_venv_to_path()

    if not args.skip_firmware:
        ensure_bootloader()
        flash_firmware(args.cp_version)
    else:
        info("Skipping firmware install")
        if not find_cp_volume(args.board):
            print(f"\n  📋 Plug in a CircuitPython board.\n")
            if not wait_for_any_cp_volume(board=args.board, timeout=120):
                fail("Timed out waiting for CircuitPython volume.")

    install_board(args.board, circup_bin)
    eject(args.board)

    volume_name = "ROADIE_RLY" if args.board == "relay" else "ROADIE_HID"
    label = "📥 IN" if args.board == "relay" else "📤 OUT"

    print()
    print(f"  🎯 Done! Board '{args.board}' is provisioned.")
    print(f"     Unplug and re-plug to start.")
    print(f"     The drive will mount as {volume_name}.")
    print(f"     Label this board: {label}")
    print()


if __name__ == "__main__":
    main()
