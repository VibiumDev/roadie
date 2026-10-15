#!/usr/bin/env bash
# Run a command against a display, picking whichever makes sense here.
#
#   board/e2e/ensure-display.sh python3 board/test_e2e.py --target pi
#
# In order of preference:
#
#   1. This machine's desktop, if it has one. The test takes it over for the
#      duration -- a kiosk browser grabs focus and the injected input follows
#      focus, which is exactly what you want when you are watching over VNC
#      and not using that desktop for anything else.
#   2. A long-lived display from 'make start-vnc-server', if one is running.
#   3. A private throwaway display, created and torn down for this one run.
#      This is what happens on a Pi with no monitor attached.
#
# Set ROADIE_E2E_ISOLATED=1 to skip step 1 and never touch the desktop --
# worth it for an unattended or CI run, where something stealing focus would
# break the test. The HID board's input follows focus, so a test on a desktop
# someone else is using is not trustworthy.
set -euo pipefail

cd "$(dirname "$0")/../.."
. board/e2e/display-lib.sh

[ $# -gt 0 ] || { echo "usage: $(basename "$0") <command> [args...]" >&2; exit 2; }

# ---------------------------------------------------------------------------
# 1. This machine's desktop, unless isolation was asked for
# ---------------------------------------------------------------------------

# ROADIE_E2E_USE_ATTACHED is the old name for "prefer the desktop", which is
# now the default; it is still honoured so existing invocations keep working.
if [ "${ROADIE_E2E_ISOLATED:-0}" != "1" ]; then
    for candidate in "${DISPLAY:-}" ":0"; do
        [ -n "$candidate" ] || continue
        for auth in "${XAUTHORITY:-}" "$HOME/.Xauthority" ""; do
            if DISPLAY="$candidate" ${auth:+XAUTHORITY="$auth"} \
                    timeout 10 xset q >/dev/null 2>&1; then
                export DISPLAY="$candidate"
                [ -n "$auth" ] && export XAUTHORITY="$auth" || unset XAUTHORITY
                echo "  🖥️  Using this machine's desktop (${DISPLAY})" \
                     "— it takes over the screen while running"
                exec "$@"
            fi
        done
    done
    echo "  🖥️  No desktop here — using a virtual display"
fi

# ---------------------------------------------------------------------------
# 2/3. A long-lived display if one is up, else a private one
# ---------------------------------------------------------------------------

OWNS_DISPLAY=0

if display_running; then
    TARGET_DISPLAY=":$(state_get display)"
    if vnc_running; then
        echo "  📺 Reusing the running display — watch at $(vnc_address)"
    else
        echo "  🖥️  Reusing the running display ${TARGET_DISPLAY}"
    fi
else
    # Anything in the state file now is a leftover from a run that was killed
    # before it could clean up. Reap it, or it squats the display and port.
    if [ -f "$ROADIE_STATE_FILE" ]; then
        echo "  🧹 Cleaning up a previous run's leftovers"
        stop_everything
    fi
    echo "  🖥️  Starting a private display for this run (${ROADIE_RESOLUTION})"
    start_display || exit 1
    TARGET_DISPLAY="$ROADIE_DISPLAY"
    OWNS_DISPLAY=1
fi

export DISPLAY="$TARGET_DISPLAY"
unset XAUTHORITY

# ---------------------------------------------------------------------------
# Keep the injected input off every *other* display on this machine
# ---------------------------------------------------------------------------
#
# Every X server on this host reads the same /dev/input devices, so the HID
# board's input reaches all of them -- a desktop session, or a VNC connection
# into :0, would receive the test's keystrokes in whatever has focus there.
# This applies just as much when reusing a long-lived display as when making
# one, so it is done for both.

OTHER_DISPLAYS=""
for socket in /tmp/.X11-unix/X*; do
    [ -e "$socket" ] || continue
    other=":${socket##*/X}"
    [ "$other" = "$TARGET_DISPLAY" ] && continue
    if DISPLAY="$other" XAUTHORITY="$HOME/.Xauthority" \
            timeout 10 xset q >/dev/null 2>&1; then
        OTHER_DISPLAYS="$OTHER_DISPLAYS $other"
    fi
done

set_roadie_devices() {
    local target="$1" action="$2" id
    for id in $(DISPLAY="$target" XAUTHORITY="$HOME/.Xauthority" \
            xinput list 2>/dev/null | grep -i "Roadie-HID" \
            | grep -oE "id=[0-9]+" | cut -d= -f2); do
        DISPLAY="$target" XAUTHORITY="$HOME/.Xauthority" \
            xinput "$action" "$id" 2>/dev/null
    done
}

restore_other_displays() {
    local other
    for other in $OTHER_DISPLAYS; do set_roadie_devices "$other" enable; done
}

cleanup() {
    set +e
    restore_other_displays
    # Only stop the display if this run started it.
    [ "$OWNS_DISPLAY" = "1" ] && stop_everything
}
# INT and TERM must *exit*: a trap handler that returns resumes whatever it
# interrupted, which previously left ctrl-C looking like a hang.
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [ -n "$OTHER_DISPLAYS" ] && command -v xinput >/dev/null; then
    restore_other_displays   # self-heal after a run that was killed outright
    for other in $OTHER_DISPLAYS; do
        set_roadie_devices "$other" disable
        echo "  🔇 Muted the HID board on ${other} for this run"
    done
fi

echo

"$@"
