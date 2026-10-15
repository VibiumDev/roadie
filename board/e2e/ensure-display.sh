#!/usr/bin/env bash
# Run a command against roadie's test display.
#
#   board/e2e/ensure-display.sh python3 board/test_e2e.py --target pi
#
# If a long-lived display is already running (see 'make start-vnc-server')
# this reuses it and leaves it alone, so whatever you have watching stays
# connected. Otherwise it creates a private display for this one command and
# tears it down afterwards.
#
# The browser needs a display of its own rather than your desktop: roadie
# injects input as a real USB keyboard, so the OS delivers it to whatever has
# focus, and on a desktop anything can steal focus mid-test -- the run fails
# and the typed text lands in your terminal. On a display of its own the
# browser is the only window that exists.
#
# Set ROADIE_E2E_USE_ATTACHED=1 to run on your attached monitor instead, with
# the caveat above.
set -euo pipefail

cd "$(dirname "$0")/../.."
. board/e2e/display-lib.sh

[ $# -gt 0 ] || { echo "usage: $(basename "$0") <command> [args...]" >&2; exit 2; }

# ---------------------------------------------------------------------------
# Your attached monitor, if you explicitly ask for it
# ---------------------------------------------------------------------------

if [ "${ROADIE_E2E_USE_ATTACHED:-0}" = "1" ]; then
    for candidate in "${DISPLAY:-}" ":0"; do
        [ -n "$candidate" ] || continue
        for auth in "${XAUTHORITY:-}" "$HOME/.Xauthority" ""; do
            if DISPLAY="$candidate" ${auth:+XAUTHORITY="$auth"} \
                    timeout 10 xset q >/dev/null 2>&1; then
                export DISPLAY="$candidate"
                [ -n "$auth" ] && export XAUTHORITY="$auth" || unset XAUTHORITY
                echo "  🖥️  Using the attached display ${DISPLAY}"
                echo "  ⚠️  Anything that takes focus will capture the test's input."
                echo
                exec "$@"
            fi
        done
    done
    echo "  ⚠️  ROADIE_E2E_USE_ATTACHED=1 but no attached display was reachable"
fi

# ---------------------------------------------------------------------------
# Reuse a long-lived display if one is up
# ---------------------------------------------------------------------------

if display_running; then
    export DISPLAY=":$(state_get display)"
    unset XAUTHORITY
    if vnc_running; then
        if vnc_client_connected; then
            echo "  📺 Reusing the display you're watching at $(vnc_address)"
        else
            echo "  📺 Reusing the running display — watch at $(vnc_address)"
        fi
    else
        echo "  🖥️  Reusing the running display ${DISPLAY}"
    fi
    echo
    # No cleanup: we did not start it, so it is not ours to stop.
    exec "$@"
fi

# ---------------------------------------------------------------------------
# Otherwise, a private display just for this command
# ---------------------------------------------------------------------------

# Anything in the state file now is a leftover from a run that was killed
# before it could clean up. Reap it, or it squats the display and VNC port.
if [ -f "$ROADIE_STATE_FILE" ]; then
    echo "  🧹 Cleaning up a previous run's leftovers"
    stop_everything
fi

OTHER_DISPLAYS=""
for socket in /tmp/.X11-unix/X*; do
    [ -e "$socket" ] || continue
    other=":${socket##*/X}"
    if DISPLAY="$other" XAUTHORITY="$HOME/.Xauthority" \
            timeout 10 xset q >/dev/null 2>&1; then
        OTHER_DISPLAYS="$OTHER_DISPLAYS $other"
    fi
done

roadie_devices_on() {
    DISPLAY="$1" XAUTHORITY="$HOME/.Xauthority" xinput list 2>/dev/null \
        | grep -i "Roadie-HID" | grep -oE "id=[0-9]+" | cut -d= -f2
}

set_roadie_devices() {
    local target="$1" action="$2" id
    for id in $(roadie_devices_on "$target"); do
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
    stop_everything
    restore_other_displays
}
# INT and TERM must *exit*: a trap handler that returns resumes whatever it
# interrupted, which previously left ctrl-C looking like a hang.
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# A desktop here would also receive the injected input, because both X
# servers read the same /dev/input devices -- the typed text would land in
# whatever has focus there. Mute the board on them for the duration.
if [ -n "$OTHER_DISPLAYS" ] && command -v xinput >/dev/null; then
    restore_other_displays   # self-heal after a run that was killed outright
    for other in $OTHER_DISPLAYS; do
        set_roadie_devices "$other" disable
        echo "  🔇 Muted the HID board on desktop display ${other} for this run"
    done
fi

echo "  🖥️  Starting a private display for this run (${ROADIE_RESOLUTION})"
start_display || exit 1
echo

"$@"
