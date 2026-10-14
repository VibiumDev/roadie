#!/usr/bin/env bash
# Set the password for watching tests over VNC (make test-e2e-pi-vnc).
#
# Only the password's obfuscated x11vnc form is stored -- you chose the
# password, so there is no reason to keep a readable copy of it on disk.
set -euo pipefail

PASSWD_FILE="${ROADIE_E2E_VNC_PASSWD_FILE:-$HOME/.config/roadie/vncpasswd}"

command -v x11vnc >/dev/null || {
    echo "❌ x11vnc not installed. Run 'make setup', or:" >&2
    echo "     sudo apt install x11vnc" >&2
    exit 1
}

# Allow non-interactive use: ROADIE_E2E_VNC_PASSWORD=secret make set-vnc-password
if [ -n "${ROADIE_E2E_VNC_PASSWORD:-}" ]; then
    password="$ROADIE_E2E_VNC_PASSWORD"
else
    if [ ! -t 0 ]; then
        echo "❌ No terminal to prompt on. Either run this interactively, or:" >&2
        echo "     ROADIE_E2E_VNC_PASSWORD=yourpassword make set-vnc-password" >&2
        exit 1
    fi
    printf 'New VNC password: '
    read -rs password; echo
    printf 'Again: '
    read -rs confirm; echo
    [ "$password" = "$confirm" ] || { echo "❌ They don't match." >&2; exit 1; }
fi

# VNC's protocol only uses the first 8 characters of a password, so say so
# rather than silently accepting something that won't fully apply.
[ -n "$password" ] || { echo "❌ Empty password." >&2; exit 1; }
if [ "${#password}" -gt 8 ]; then
    echo "⚠️  VNC only uses the first 8 characters; the rest is ignored."
fi

mkdir -p "$(dirname "$PASSWD_FILE")"
x11vnc -storepasswd "$password" "$PASSWD_FILE" >/dev/null 2>&1 || {
    echo "❌ Could not write $PASSWD_FILE" >&2
    exit 1
}
chmod 600 "$PASSWD_FILE"

echo "✅ Saved. Use it in your viewer when you run 'make test-e2e-pi-vnc'."
