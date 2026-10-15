# Shared helpers for roadie's test display. Sourced, not executed.
#
# A real Xorg on the dummy video driver, plus a minimal window manager, and
# optionally x11vnc so it can be watched. Xvfb is deliberately not used: it
# has no evdev input devices, so input injected by the HID board never
# reaches it. A real Xorg loads libinput and sees the USB devices, which is
# the whole point. The window manager is not optional either -- without one
# --kiosk is never resized to fill the screen, and input aimed at the far
# corners lands outside the browser window.

ROADIE_STATE_FILE="${ROADIE_E2E_STATE_FILE:-/tmp/roadie-e2e-display.state}"
ROADIE_RESOLUTION="${ROADIE_E2E_RESOLUTION:-1280x720}"
ROADIE_VNC_PORT_DEFAULT=5934

state_get() {
    [ -f "$ROADIE_STATE_FILE" ] || return 1
    awk -F= -v k="$1" '$1 == k {print $2; found=1} END {exit !found}' \
        "$ROADIE_STATE_FILE"
}

state_set() {
    local key="$1" value="$2" tmp
    tmp="$(mktemp)"
    [ -f "$ROADIE_STATE_FILE" ] && grep -v "^${key}=" "$ROADIE_STATE_FILE" > "$tmp"
    printf '%s=%s\n' "$key" "$value" >> "$tmp"
    mv "$tmp" "$ROADIE_STATE_FILE"
}

pid_alive() {
    local pid="${1:-}"
    [ -n "$pid" ] || return 1
    kill -0 "$pid" 2>/dev/null && return 0
    sudo -n kill -0 "$pid" 2>/dev/null
}

# Is there a usable roadie display already running?
display_running() {
    local d x
    d="$(state_get display 2>/dev/null)" || return 1
    x="$(state_get xorg 2>/dev/null)" || return 1
    pid_alive "$x" || return 1
    [ -e "/tmp/.X11-unix/X${d}" ] || return 1
    return 0
}

vnc_running() {
    local v
    v="$(state_get vnc 2>/dev/null)" || return 1
    pid_alive "$v"
}

# Is a viewer actually attached to the VNC port?
vnc_client_connected() {
    local port
    port="$(state_get vnc_port 2>/dev/null)" || return 1
    ss -tnH 2>/dev/null | awk '$1 == "ESTAB" {print $4}' \
        | sed 's/.*[:.]//' | grep -qx "$port"
}

stop_pid() {
    local pid="${1:-}" i
    [ -n "$pid" ] || return 0
    pid_alive "$pid" || return 0
    kill "$pid" 2>/dev/null || sudo -n kill "$pid" 2>/dev/null
    # Xorg does not always go on the first SIGTERM, so wait, then insist.
    for i in 1 2 3 4 5 6 7 8; do
        pid_alive "$pid" || return 0
        sleep 0.25
    done
    kill -9 "$pid" 2>/dev/null || sudo -n kill -9 "$pid" 2>/dev/null
}

require_display_deps() {
    local binary
    for binary in Xorg matchbox-window-manager; do
        command -v "$binary" >/dev/null || {
            echo "❌ $binary not found. Run 'make setup', or:" >&2
            echo "     sudo apt install xserver-xorg-video-dummy matchbox-window-manager" >&2
            return 1
        }
    done
    [ -e /usr/lib/xorg/modules/drivers/dummy_drv.so ] || {
        echo "❌ Xorg dummy video driver missing:" >&2
        echo "     sudo apt install xserver-xorg-video-dummy" >&2
        return 1
    }
}

pick_display_number() {
    local n=98
    while [ -e "/tmp/.X11-unix/X${n}" ] && [ "$n" -lt 128 ]; do
        n=$((n + 1))
    done
    echo "$n"
}

write_xorg_conf() {
    local width="${ROADIE_RESOLUTION%x*}" height="${ROADIE_RESOLUTION#*x}"
    local conf modeline="" mode_name generator
    conf="$(mktemp /tmp/roadie-xorg-dummy.XXXXXX.conf)"
    mode_name="${width}x${height}"
    # The dummy driver falls back to a default mode without an explicit one.
    # cvt is not packaged on Debian 13; gtf ships with xserver-xorg-core.
    for generator in cvt gtf; do
        command -v "$generator" >/dev/null || continue
        modeline="$("$generator" "$width" "$height" 60 2>/dev/null \
            | grep -i '^ *Modeline' | head -1)"
        if [ -n "$modeline" ]; then
            # Generators name the mode themselves (gtf makes
            # "1280x720_60.00") and Modes must reference that exact name.
            mode_name="$(printf '%s' "$modeline" \
                | sed -n 's/.*"\([^"]*\)".*/\1/p')"
            break
        fi
    done
    cat > "$conf" <<CONFEOF
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
    ${modeline}
EndSection
Section "Screen"
    Identifier "dummy-screen"
    Device     "dummy-video"
    Monitor    "dummy-monitor"
    DefaultDepth 24
    SubSection "Display"
        Depth  24
        Modes  "${mode_name}" "${width}x${height}"
        Virtual ${width} ${height}
    EndSubSection
EndSection
Section "ServerLayout"
    Identifier "dummy-layout"
    Screen     "dummy-screen"
EndSection
CONFEOF
    echo "$conf"
}

# Start Xorg + window manager on a fresh display. Sets ROADIE_DISPLAY.
start_display() {
    local num conf log sudo_prefix=""
    require_display_deps || return 1
    num="$(pick_display_number)"
    conf="$(write_xorg_conf)"
    log="/tmp/roadie-xorg${num}.log"

    # Whether root is needed depends on /etc/X11/Xwrapper.config, which
    # 'make setup' configures so it is not. Try without, then fall back.
    local attempt
    for attempt in direct sudo; do
        if [ "$attempt" = "sudo" ]; then
            sudo -n true 2>/dev/null || break
            sudo_prefix="sudo -n"
        fi
        setsid $sudo_prefix Xorg ":${num}" -config "$conf" -ac \
            -nolisten tcp -novtswitch >"$log" 2>&1 &
        local xorg_pid=$!
        local i
        for i in $(seq 1 80); do
            [ -e "/tmp/.X11-unix/X${num}" ] && break
            sleep 0.25
        done
        if [ -e "/tmp/.X11-unix/X${num}" ]; then
            : > "$ROADIE_STATE_FILE"
            state_set display "$num"
            state_set xorg "$xorg_pid"
            state_set conf "$conf"
            export DISPLAY=":${num}"
            unset XAUTHORITY   # -ac means no auth file is needed or wanted
            setsid matchbox-window-manager -use_titlebar no \
                >"/tmp/roadie-matchbox${num}.log" 2>&1 &
            state_set wm "$!"
            sleep 2
            ROADIE_DISPLAY=":${num}"
            return 0
        fi
    done

    echo "❌ Xorg failed to start on :${num}. Last lines of ${log}:" >&2
    tail -20 "$log" >&2
    rm -f "$conf"
    return 1
}

start_vnc() {
    local passwd_file port view_flag=""
    passwd_file="${ROADIE_E2E_VNC_PASSWD_FILE:-$HOME/.config/roadie/vncpasswd}"
    if [ ! -f "$passwd_file" ]; then
        echo "❌ No VNC password set yet. Set one with:" >&2
        echo "     make set-vnc-password" >&2
        return 1
    fi
    command -v x11vnc >/dev/null || {
        echo "❌ x11vnc not installed. Run 'make setup'." >&2
        return 1
    }

    port="${ROADIE_E2E_VNC_PORT:-$ROADIE_VNC_PORT_DEFAULT}"
    # View-only by default: a stray click from whoever is watching would move
    # focus on the test's display and break the run.
    [ "${ROADIE_E2E_VNC_CONTROL:-0}" = "1" ] || view_flag="-viewonly"

    setsid x11vnc -display "$ROADIE_DISPLAY" -rfbport "$port" -forever \
        -shared -rfbauth "$passwd_file" $view_flag -quiet \
        >"/tmp/roadie-x11vnc.log" 2>&1 &
    local vnc_pid=$!
    sleep 2
    if ! pid_alive "$vnc_pid"; then
        echo "❌ x11vnc failed on port ${port}. See /tmp/roadie-x11vnc.log" >&2
        tail -10 /tmp/roadie-x11vnc.log >&2
        return 1
    fi
    state_set vnc "$vnc_pid"
    state_set vnc_port "$port"
}

stop_everything() {
    local conf
    stop_pid "$(state_get vnc 2>/dev/null || true)"
    stop_pid "$(state_get wm 2>/dev/null || true)"
    stop_pid "$(state_get xorg 2>/dev/null || true)"
    conf="$(state_get conf 2>/dev/null || true)"
    [ -n "$conf" ] && rm -f "$conf"
    local d
    d="$(state_get display 2>/dev/null || true)"
    if [ -n "$d" ]; then
        rm -f "/tmp/.X11-unix/X${d}" 2>/dev/null \
            || sudo -n rm -f "/tmp/.X11-unix/X${d}" 2>/dev/null
    fi
    rm -f "$ROADIE_STATE_FILE"
}

vnc_address() {
    local host port
    port="$(state_get vnc_port 2>/dev/null || echo "$ROADIE_VNC_PORT_DEFAULT")"
    host="$(hostname -I 2>/dev/null | awk '{print $1}')"
    echo "${host:-$(hostname)}:${port}"
}
