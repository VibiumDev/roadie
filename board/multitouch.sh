#!/usr/bin/env bash
# Switch the HID board between single-touch and multi-touch on this host.
#
# The board always sends two contacts; what changes is which kernel driver
# claims it:
#
#   hid-generic    mouse and touchscreen are separate input devices, and
#                  touch is single-contact. This is what roadie's udev rule
#                  forces, and what the mouse tests need.
#   hid-multitouch a real MT protocol B device (ABS_MT_SLOT, two slots), so
#                  gestures work -- but the separate mouse node disappears,
#                  merged into one device.
#
# You cannot have both at once, so this is a toggle rather than a setting.
# Only the host the HID board is plugged into is affected; a phone or a PC
# as the target picks its own driver and is not touched by any of this.
set -u

VID=239a
PID=80f8
HID_DRIVERS=/sys/bus/hid/drivers

die() { echo "  ❌ $*" >&2; exit 1; }

hid_devices() {
    local d
    for d in /sys/bus/hid/devices/*; do
        [ -e "$d/uevent" ] || continue
        grep -qi "HID_NAME=Adafruit Roadie-HID" "$d/uevent" 2>/dev/null && echo "$(basename "$d")"
    done
}

driver_of() {
    local link; link=$(readlink "/sys/bus/hid/devices/$1/driver" 2>/dev/null) || return
    basename "$link"
}

# Binding hid-generic needs the kernel told to ignore the driver it would
# otherwise prefer -- hid-multitouch is its natural choice for this
# descriptor. This is the same dance as 99-roadie-hid.rules.
to_generic() {
    local dev=$1
    sudo sh -c "
        echo 1 > /sys/module/hid/parameters/ignore_special_drivers
        echo -n '$dev' > $HID_DRIVERS/hid-multitouch/unbind 2>/dev/null
        echo -n '$dev' > /sys/bus/hid/drivers_probe 2>/dev/null
        echo 0 > /sys/module/hid/parameters/ignore_special_drivers"
}

to_multitouch() {
    local dev=$1
    sudo sh -c "
        echo -n '$dev' > $HID_DRIVERS/hid-generic/unbind 2>/dev/null
        echo -n '$dev' > $HID_DRIVERS/hid-multitouch/bind 2>/dev/null"
}

report() {
    local dev drv mouse slots
    for dev in $(hid_devices); do
        drv=$(driver_of "$dev")
        case "$drv" in
            hid-multitouch) mode="multi-touch" ;;
            hid-generic)    mode="single-touch" ;;
            "")             mode="NO DRIVER BOUND" ;;
            *)              mode="$drv" ;;
        esac
        mouse=no
        ls /dev/input/by-id/*Roadie-HID*event-mouse >/dev/null 2>&1 && mouse=yes
        echo "  $dev  $mode  (separate mouse device: $mouse)"
    done
}

case "${1:-status}" in
  on)
    for dev in $(hid_devices); do
        [ "$(driver_of "$dev")" = "hid-multitouch" ] && continue
        to_multitouch "$dev"
    done
    sleep 2
    report
    echo "  ⚠️  The separate mouse device is gone until you switch back."
    ;;
  off)
    for dev in $(hid_devices); do
        [ "$(driver_of "$dev")" = "hid-generic" ] && continue
        to_generic "$dev"
    done
    sleep 2
    report
    ;;
  status)
    report
    ;;
  *)
    echo "usage: $(basename "$0") [on|off|status]" >&2; exit 2 ;;
esac

# A device left with no driver has no input nodes at all, which looks like
# the board has died. Never exit in that state.
for dev in $(hid_devices); do
    if [ -z "$(driver_of "$dev")" ]; then
        echo "  ⚠️  $dev had no driver; restoring single-touch"
        to_generic "$dev"
    fi
done
