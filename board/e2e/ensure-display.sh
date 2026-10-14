#!/usr/bin/env bash
# Run a command on an isolated X display, so HID input can only reach it.
#
#   board/e2e/ensure-display.sh python3 board/test_e2e.py --target pi
#
# A throwaway Xorg on the dummy video driver is created by default, even when
# a monitor is attached. That is not just for display-less machines -- it is
# what makes the test correct.
#
# Roadie injects input as a real USB keyboard and mouse, so the OS delivers it
# to whichever window has focus. There is no way to address a specific window.
# On a desktop you are also using, anything can take focus mid-test -- a
# notification, a screensaver, you clicking something -- and the keystrokes
# land there instead: the test fails, and the typed string ends up in your
# terminal. On a display of its own the browser is the only window that
# exists, so focus is deterministic and nothing can leak.
#
# Set ROADIE_E2E_USE_ATTACHED=1 to run on the attached display instead, which
# is useful when you want to watch the test happen on a real screen. Expect
# the caveats above if you do.
#
# Set ROADIE_E2E_VNC=1 to serve the virtual display over VNC, so a machine
# with no monitor can still be watched. See docs/how-to/watch-tests.md.
#
# Two notes, both measured rather than assumed:
#
#   * Xvfb is NOT used. It has no evdev input devices (only synthetic XTEST
#     ones), so input injected by the HID board never reaches it and every
#     input check fails. A real Xorg loads libinput and sees the USB devices.
#
#   * A window manager is required. Without one, --kiosk is never resized to
#     fill the screen, so input aimed at the far corners lands outside the
#     browser window.
#
#   * If this machine also runs a desktop, the HID board's input reaches BOTH
#     X servers -- the virtual one and the desktop -- because they read the
#     same /dev/input devices. The typed test string would land in whatever
#     window has focus on the desktop. So the board's devices are disabled on
#     the other X server for the duration and restored afterwards.
set -euo pipefail

RESOLUTION="${ROADIE_E2E_RESOLUTION:-1280x720}"
WIDTH="${RESOLUTION%x*}"
HEIGHT="${RESOLUTION#*x}"
# Virtual by default; opt in to the attached display.
USE_ATTACHED="${ROADIE_E2E_USE_ATTACHED:-0}"
# Back-compat: FORCE_VIRTUAL=1 used to be how you asked for the virtual one.
[ "${ROADIE_E2E_FORCE_VIRTUAL:-0}" = "1" ] && USE_ATTACHED=0

if [ $# -eq 0 ]; then
    echo "usage: $0 <command> [args...]" >&2
    exit 2
fi

usable_display() {
    # $1 = display, $2 = xauthority (may be empty)
    local probe_display="$1" probe_auth="$2"
    [ -e "/tmp/.X11-unix/X${probe_display#:}" ] || return 1
    if [ -n "$probe_auth" ]; then
        DISPLAY="$probe_display" XAUTHORITY="$probe_auth" \
            timeout 10 xset q >/dev/null 2>&1
    else
        DISPLAY="$probe_display" timeout 10 xset q >/dev/null 2>&1
    fi
}

# ---------------------------------------------------------------------------
# Prefer a real display when there is one
# ---------------------------------------------------------------------------

if [ "$USE_ATTACHED" = "1" ]; then
    for candidate_display in "${DISPLAY:-}" ":0"; do
        [ -n "$candidate_display" ] || continue
        for candidate_auth in "${XAUTHORITY:-}" "$HOME/.Xauthority" ""; do
            if usable_display "$candidate_display" "$candidate_auth"; then
                export DISPLAY="$candidate_display"
                if [ -n "$candidate_auth" ]; then
                    export XAUTHORITY="$candidate_auth"
                else
                    unset XAUTHORITY
                fi
                echo "  🖥️  Using the attached display ${DISPLAY}"
                echo "  ⚠️  Anything that takes focus will capture the test's"
                echo "     keystrokes. Unset ROADIE_E2E_USE_ATTACHED to isolate."
                echo
                exec "$@"
            fi
        done
    done
    echo "  ⚠️  ROADIE_E2E_USE_ATTACHED=1 but no display was reachable"
fi
echo "  🖥️  Using an isolated virtual display (keeps input off your desktop)"

# ---------------------------------------------------------------------------
# Otherwise, build one
# ---------------------------------------------------------------------------

for binary in Xorg matchbox-window-manager; do
    command -v "$binary" >/dev/null || {
        echo "  ❌ $binary not found. Install the virtual display dependencies:" >&2
        echo "       sudo apt install xserver-xorg-video-dummy matchbox-window-manager" >&2
        echo "     (or run 'make setup', which installs them)" >&2
        exit 1
    }
done
[ -e /usr/lib/xorg/modules/drivers/dummy_drv.so ] || {
    echo "  ❌ Xorg dummy video driver missing. Install it with:" >&2
    echo "       sudo apt install xserver-xorg-video-dummy" >&2
    exit 1
}

# Whether root is needed depends on /etc/X11/Xwrapper.config, which
# `make setup` configures so no root is required. Try without it first and
# only fall back to sudo, so the common path needs no privileges at all.
SUDO=""

DISPLAY_NUM=98
while [ -e "/tmp/.X11-unix/X${DISPLAY_NUM}" ] && [ "$DISPLAY_NUM" -lt 128 ]; do
    DISPLAY_NUM=$((DISPLAY_NUM + 1))
done
VDISPLAY=":${DISPLAY_NUM}"

# Give the dummy driver an explicit modeline where we can. Not fatal if we
# can't: the Modes/Virtual lines below still pin the framebuffer size, and
# the driver has common resolutions built in. cvt isn't packaged on Debian
# 13, so gtf (from xserver-xorg-core) is the usable one.
MODELINE=""
MODE_NAME="${WIDTH}x${HEIGHT}"
for generator in cvt gtf; do
    command -v "$generator" >/dev/null || continue
    MODELINE="$("$generator" "$WIDTH" "$HEIGHT" 60 2>/dev/null \
        | grep -i '^ *Modeline' | head -1)"
    if [ -n "$MODELINE" ]; then
        # Generators name the mode themselves (gtf makes "1280x720_60.00"),
        # and Modes has to reference that exact name to take effect.
        generated_name="$(printf '%s' "$MODELINE" | sed -n 's/.*"\([^"]*\)".*/\1/p')"
        [ -n "$generated_name" ] && MODE_NAME="$generated_name"
        break
    fi
done

CONF="$(mktemp /tmp/roadie-xorg-dummy.XXXXXX.conf)"
cat > "$CONF" <<CONFEOF
Section "ServerFlags"
    Option "AutoAddDevices" "true"
    Option "AutoEnableDevices" "true"
EndSection
Section "Device"
    Identifier "dummy-video"
    Driver     "dummy"
    VideoRam   256000
EndSection
Section "Monitor"
    Identifier  "dummy-monitor"
    HorizSync   5.0 - 1000.0
    VertRefresh 5.0 - 200.0
    ${MODELINE}
EndSection
Section "Screen"
    Identifier "dummy-screen"
    Device     "dummy-video"
    Monitor    "dummy-monitor"
    DefaultDepth 24
    SubSection "Display"
        Depth  24
        Modes  "${MODE_NAME}" "${WIDTH}x${HEIGHT}"
        Virtual ${WIDTH} ${HEIGHT}
    EndSubSection
EndSection
Section "ServerLayout"
    Identifier "dummy-layout"
    Screen     "dummy-screen"
EndSection
CONFEOF

XORG_LOG="/tmp/roadie-xorg${DISPLAY_NUM}.log"
WM_LOG="/tmp/roadie-matchbox${DISPLAY_NUM}.log"
VNC_LOG="/tmp/roadie-x11vnc${DISPLAY_NUM}.log"

# ---------------------------------------------------------------------------
# Keep the injected input out of any desktop session on this machine
# ---------------------------------------------------------------------------

OTHER_DISPLAYS=""
for socket in /tmp/.X11-unix/X*; do
    [ -e "$socket" ] || continue
    other=":${socket##*/X}"
    [ "$other" = "$VDISPLAY" ] && continue
    if DISPLAY="$other" XAUTHORITY="$HOME/.Xauthority" \
            timeout 10 xset q >/dev/null 2>&1; then
        OTHER_DISPLAYS="$OTHER_DISPLAYS $other"
    fi
done

roadie_device_ids() {
    DISPLAY="$1" XAUTHORITY="$HOME/.Xauthority" xinput list 2>/dev/null \
        | grep -i "Roadie-HID" | grep -oE "id=[0-9]+" | cut -d= -f2
}

set_roadie_devices() {
    # $1 = display, $2 = enable|disable
    local target="$1" action="$2" id
    for id in $(roadie_device_ids "$target"); do
        DISPLAY="$target" XAUTHORITY="$HOME/.Xauthority" \
            xinput "$action" "$id" 2>/dev/null
    done
}

restore_other_displays() {
    local other
    for other in $OTHER_DISPLAYS; do
        set_roadie_devices "$other" enable
    done
}

cleanup() {
    set +e
    [ -n "${VNC_PID:-}" ] && kill "$VNC_PID" 2>/dev/null
    [ -n "${WM_PID:-}" ] && kill "$WM_PID" 2>/dev/null
    pkill -f "Xorg ${VDISPLAY}" 2>/dev/null \
        || $SUDO pkill -f "Xorg ${VDISPLAY}" 2>/dev/null
    restore_other_displays
    rm -f "$CONF"
}
trap cleanup EXIT INT TERM

if [ -n "$OTHER_DISPLAYS" ] && command -v xinput >/dev/null; then
    # Re-enable first, so a previous run that was killed outright doesn't
    # leave the desktop's devices switched off.
    restore_other_displays
    for other in $OTHER_DISPLAYS; do
        set_roadie_devices "$other" disable
        echo "  🔇 Muted the HID board on desktop display ${other} for this run"
    done
fi

start_xorg() {
    $SUDO Xorg "$VDISPLAY" -config "$CONF" -ac -nolisten tcp -novtswitch \
        >"$XORG_LOG" 2>&1 &
    local _
    for _ in $(seq 1 80); do
        [ -e "/tmp/.X11-unix/X${DISPLAY_NUM}" ] && return 0
        sleep 0.25
    done
    return 1
}

echo "  🖥️  Starting Xorg on ${VDISPLAY} (${WIDTH}x${HEIGHT}, dummy driver)"
if ! start_xorg; then
    if [ "$(id -u)" -ne 0 ] && sudo -n true 2>/dev/null; then
        echo "  ↻  Retrying with sudo (run 'make setup' to avoid needing it)"
        SUDO="sudo -n"
        start_xorg || {
            echo "  ❌ Xorg failed on ${VDISPLAY}. Last lines of ${XORG_LOG}:" >&2
            tail -20 "$XORG_LOG" >&2
            exit 1
        }
    else
        echo "  ❌ Could not start an X server, and sudo is unavailable." >&2
        echo "     Run 'make setup' to allow non-root X servers, or run:" >&2
        echo "       sudo -v   # then retry" >&2
        echo "     Last lines of ${XORG_LOG}:" >&2
        tail -20 "$XORG_LOG" >&2
        exit 1
    fi
fi

# -ac disables access control, so no Xauthority is needed or wanted.
unset XAUTHORITY
export DISPLAY="$VDISPLAY"

matchbox-window-manager -use_titlebar no >"$WM_LOG" 2>&1 &
WM_PID=$!
sleep 2
echo "  ✅ Virtual display ready on ${VDISPLAY}"

# ---------------------------------------------------------------------------
# Optionally serve the display over VNC so it can be watched remotely
# ---------------------------------------------------------------------------

if [ "${ROADIE_E2E_VNC:-0}" = "1" ]; then
    command -v x11vnc >/dev/null || {
        echo "  ❌ x11vnc not installed. Run 'make setup', or:" >&2
        echo "       sudo apt install x11vnc" >&2
        exit 1
    }

    # Raspberry Pi OS enables RealVNC's own server, which already owns 5900,
    # so start there and walk upwards to the first free port.
    port_in_use() {
        ss -ltnH 2>/dev/null | awk '{print $4}' | sed 's/.*[:.]//' \
            | grep -qx "$1"
    }
    # 5934 (VNC display :34) is deliberately out of the way. A port you can
    # predict matters more than a clever one, because it means an ssh tunnel
    # and a saved viewer bookmark keep working run after run. The low ports
    # are the contended ones: Raspberry Pi OS runs RealVNC on 5900, and 5901
    # is the conventional "display :1" slot other VNC servers take.
    # A password, stored once, beats an ssh tunnel for this: you save it in
    # your viewer and then connect straight to the Pi every time. The tunnel
    # was only ever compensating for an unauthenticated server.
    VNC_PASSWD_FILE="${ROADIE_E2E_VNC_PASSWD_FILE:-$HOME/.config/roadie/vncpasswd}"
    if [ ! -f "$VNC_PASSWD_FILE" ]; then
        mkdir -p "$(dirname "$VNC_PASSWD_FILE")"
        # Read a fixed block first: piping /dev/urandom into `head -c` makes
        # head close the pipe, and with pipefail that SIGPIPE is fatal.
        generated="$(head -c 256 /dev/urandom \
            | LC_ALL=C tr -dc 'a-zA-Z0-9' | cut -c1-8)"
        x11vnc -storepasswd "$generated" "$VNC_PASSWD_FILE" >/dev/null 2>&1 || {
            echo "  ❌ Could not create a VNC password file at" >&2
            echo "     ${VNC_PASSWD_FILE}" >&2
            exit 1
        }
        chmod 600 "$VNC_PASSWD_FILE"
        echo "  🔑 Generated a VNC password for this machine: ${generated}"
        echo "     Saved to ${VNC_PASSWD_FILE} — save it in your viewer and"
        echo "     you will not be asked to think about it again."
    fi

    VNC_PORT="${ROADIE_E2E_VNC_PORT:-5934}"
    if [ -z "${ROADIE_E2E_VNC_PORT:-}" ]; then
        while port_in_use "$VNC_PORT" && [ "$VNC_PORT" -lt 5960 ]; do
            VNC_PORT=$((VNC_PORT + 1))
        done
        if [ "$VNC_PORT" != "5934" ]; then
            echo "  ℹ️  5934 was busy; using ${VNC_PORT} this run."
            echo "     Pin it with ROADIE_E2E_VNC_PORT to keep a tunnel stable."
        fi
    fi
    # Password-protected, so it can be reached directly. Set
    # ROADIE_E2E_VNC_LISTEN=localhost to restrict it to an ssh tunnel instead.
    if [ "${ROADIE_E2E_VNC_LISTEN:-all}" = "localhost" ]; then
        VNC_SCOPE="-localhost"
        VNC_WHERE="localhost:${VNC_PORT}"
        VNC_NOTE="tunnel first: ssh -L ${VNC_PORT}:localhost:${VNC_PORT} $(id -un)@$(hostname)"
    else
        VNC_SCOPE=""
        host_addr="$(hostname -I 2>/dev/null | awk '{print $1}')"
        VNC_WHERE="${host_addr:-$(hostname)}:${VNC_PORT}"
        VNC_NOTE="password is in ${VNC_PASSWD_FILE}"
    fi

    # View-only by default. Not only for safety: a stray click from whoever
    # is watching would move focus on the test's display and break the run.
    if [ "${ROADIE_E2E_VNC_CONTROL:-0}" = "1" ]; then
        VNC_MODE=""
        VNC_NOTE="${VNC_NOTE} — input enabled, so don't click mid-test"
    else
        VNC_MODE="-viewonly"
    fi

    x11vnc -display "$VDISPLAY" -rfbport "$VNC_PORT" -forever -shared \
        -rfbauth "$VNC_PASSWD_FILE" $VNC_SCOPE $VNC_MODE \
        -quiet >"$VNC_LOG" 2>&1 &
    VNC_PID=$!
    sleep 2
    if ! kill -0 "$VNC_PID" 2>/dev/null; then
        echo "  ❌ x11vnc failed to start on port ${VNC_PORT}." >&2
        if grep -qi "could not obtain listening port" "$VNC_LOG" 2>/dev/null; then
            echo "     That port is taken. Pick another with" >&2
            echo "     ROADIE_E2E_VNC_PORT=<port>." >&2
        fi
        echo "     See ${VNC_LOG}:" >&2
        tail -10 "$VNC_LOG" >&2
        exit 1
    fi

    echo
    echo "  📺 Watch the test at:  ${VNC_WHERE}"
    echo "     ${VNC_NOTE}"
    echo

    vnc_client_connected() {
        ss -tnH 2>/dev/null | awk '$1 == "ESTAB" {print $4}' \
            | sed 's/.*[:.]//' | grep -qx "$VNC_PORT"
    }

    VNC_WAIT="${ROADIE_E2E_VNC_WAIT:-client}"
    case "$VNC_WAIT" in
        client)
            # Block until something is actually watching, so the test can't
            # finish before you have connected.
            VNC_CLIENT_TIMEOUT="${ROADIE_E2E_VNC_CLIENT_TIMEOUT:-300}"
            echo "  ⏳ Waiting for a viewer to connect"
            echo "     (up to ${VNC_CLIENT_TIMEOUT}s; ctrl-C to give up,"
            echo "      or set ROADIE_E2E_VNC_WAIT=0 to start immediately)"
            waited=0
            while ! vnc_client_connected; do
                if [ "$waited" -ge "$VNC_CLIENT_TIMEOUT" ]; then
                    echo "  ⚠️  No viewer connected after ${VNC_CLIENT_TIMEOUT}s;"
                    echo "     running anyway."
                    break
                fi
                sleep 1
                waited=$((waited + 1))
            done
            if vnc_client_connected; then
                echo "  ✅ Viewer connected — starting in 2s"
                sleep 2
            fi
            ;;
        0)
            ;;
        *)
            echo "  ⏳ Starting in ${VNC_WAIT}s — connect your viewer now."
            sleep "$VNC_WAIT"
            ;;
    esac
fi

echo

"$@"
