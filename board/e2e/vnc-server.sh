#!/usr/bin/env bash
# Manage a long-lived test display you can watch over VNC.
#
#   board/e2e/vnc-server.sh start|stop|status
#
# Start it once, connect your viewer once, and leave it. Test runs reuse this
# display instead of creating and tearing down their own, so you see every
# run live without reconnecting.
set -euo pipefail

cd "$(dirname "$0")/../.."
. board/e2e/display-lib.sh

usage() { echo "usage: $(basename "$0") start|stop|status" >&2; exit 2; }

# Someone already watching :0 does not need a second server and a second
# viewer -- they can run the test on that desktop instead.
already_watching_desktop() {
    [ -e /tmp/.X11-unix/X0 ] || return 1
    ss -tnH 2>/dev/null | awk '$1 == "ESTAB" {print $4}' \
        | sed 's/.*[:.]//' | grep -qx 5900
}
[ $# -eq 1 ] || usage

case "$1" in
start)
    if already_watching_desktop; then
        echo "  ℹ️  You already have a VNC session on this machine's desktop,"
        echo "     and 'make test-e2e-pi' runs there by default — so you can"
        echo "     watch runs without this server at all."
        echo
    fi
    if display_running; then
        if vnc_running; then
            echo "  ✅ Already running — watch at $(vnc_address)"
            exit 0
        fi
        # Display is up but nothing is serving it; attach a server.
        ROADIE_DISPLAY=":$(state_get display)"
        export DISPLAY="$ROADIE_DISPLAY"
        start_vnc || exit 1
        echo "  ✅ VNC server attached — watch at $(vnc_address)"
        exit 0
    fi

    # Anything in the state file now is a leftover from a killed run.
    [ -f "$ROADIE_STATE_FILE" ] && stop_everything

    echo "  🖥️  Starting test display (${ROADIE_RESOLUTION})"
    start_display || exit 1
    start_vnc || { stop_everything; exit 1; }
    echo
    echo "  📺 Watch at: $(vnc_address)"
    echo "     (the password you set with 'make set-vnc-password')"
    echo
    echo "     Leave your viewer connected. Test runs will appear here."
    echo "     Stop it with: make stop-vnc-server"
    ;;
stop)
    if ! display_running && ! vnc_running; then
        echo "  Nothing running."
        [ -f "$ROADIE_STATE_FILE" ] && { stop_everything; echo "  (cleared stale state)"; }
        exit 0
    fi
    stop_everything
    echo "  🛑 Stopped."
    ;;
status)
    if display_running; then
        echo "  display  : :$(state_get display) (pid $(state_get xorg))"
        if vnc_running; then
            echo "  vnc      : $(vnc_address) (pid $(state_get vnc))"
            if vnc_client_connected; then
                echo "  viewer   : connected"
            else
                echo "  viewer   : not connected"
            fi
        else
            echo "  vnc      : not running"
        fi
    else
        echo "  not running"
    fi
    ;;
*) usage ;;
esac
