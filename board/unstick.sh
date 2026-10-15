#!/usr/bin/env bash
# Recover from input that has got stuck.
#
#   make unstick
#
# Two separate things can go wrong, and both need undoing:
#
#   * The board can be left holding a button or a contact, if something
#     stopped between a press and its release. The host then behaves as
#     though you are holding the mouse down -- a context menu that will not
#     close, a drag that will not end.
#
#   * An X server can stop delivering that device's buttons to windows even
#     after the button is released, while still delivering motion. Clicks
#     then appear to do nothing. Re-attaching the device clears it.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "  🖐  Releasing every button and contact"
port=$(ls /dev/serial/by-id/usb-Adafruit_Roadie-Relay_*-if02 2>/dev/null | head -1)
if [ -n "$port" ]; then
    .venv/bin/python - "$port" <<'PY'
import serial, sys, time
with serial.Serial(sys.argv[1], 115200, timeout=2) as s:
    time.sleep(0.3)
    s.write(b'{"cmd":"mouse_release","buttons":7}\n')
    time.sleep(0.2)
    s.write(b'{"cmd":"touch","contacts":[]}\n')
    time.sleep(0.3)
PY
else
    echo "     (relay not connected — skipping)"
fi

for socket in /tmp/.X11-unix/X*; do
    [ -e "$socket" ] || continue
    display=":${socket##*/X}"
    export DISPLAY="$display" XAUTHORITY="${XAUTHORITY:-$HOME/.Xauthority}"
    xset q >/dev/null 2>&1 || continue
    ids=$(xinput list 2>/dev/null | grep -i "Roadie-HID" \
          | grep -oE "id=[0-9]+" | cut -d= -f2 || true)
    [ -n "$ids" ] || continue
    echo "  🔁 Re-attaching the board's devices on ${display}"
    for id in $ids; do
        xinput disable "$id" 2>/dev/null || true
    done
    sleep 1
    for id in $ids; do
        xinput enable "$id" 2>/dev/null || true
    done
done

echo "  ✅ Done. Clicks should land again."
