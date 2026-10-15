BINARY = roadie

# CircuitPython version installed by the flash targets.
# Override to track the newest stable release instead of the pin:
#   make flash-hid CP_VERSION=latest
CP_VERSION ?= 10.3.1

.PHONY: build test setup uninstall flash-relay flash-hid flash-relay-quick flash-hid-quick sync sync-hid sync-relay status set-vnc-password start-vnc-server stop-vnc-server vnc-status test-hid test-hid-quick test-audio test-audio-serial test-audio-api test-audio-browser test-e2e-pi test-e2e-android test-e2e-iphone test-e2e-pc clean

build:
	go build -o $(BINARY) .

test:
	go test -v ./...

setup:
	python3 board/install.py --setup-only
	sudo cp board/99-roadie-hid.rules board/99-roadie-capture.rules /etc/udev/rules.d/ && sudo udevadm control --reload-rules && sudo udevadm trigger
	@# Everything the browser test needs on a Pi with no display attached:
	@# an X server and the dummy video driver, a window manager so the kiosk
	@# window fills the screen, xset/xrandr/xinput for probing and muting,
	@# a browser to drive, and x11vnc to watch it remotely.
	@# Skipped on non-apt systems; only the e2e tests need these.
	@command -v apt-get >/dev/null && { \
		echo "  📦 Installing browser-test dependencies"; \
		sudo apt-get install -y -q \
			xserver-xorg-core xserver-xorg-video-dummy \
			matchbox-window-manager x11-xserver-utils xinput \
			chromium x11vnc \
			|| echo "  ⚠️  Install failed; 'make test-e2e-pi' may not run"; \
	} || true
	@# Lets the browser test start its own X display over ssh, no root needed.
	@if [ ! -f /etc/X11/Xwrapper.config ] \
		|| ! grep -q "allowed_users=anybody" /etc/X11/Xwrapper.config; then \
		echo "  🖥️  Allowing non-root X servers (for the browser test over ssh)"; \
		[ -f /etc/X11/Xwrapper.config ] \
			&& sudo cp /etc/X11/Xwrapper.config /etc/X11/Xwrapper.config.roadie-bak; \
		sudo install -m 644 board/e2e/Xwrapper.config /etc/X11/Xwrapper.config; \
	fi
	@# Reading the injected input events requires group membership.
	@id -nG | tr ' ' '\n' | grep -qx input || { \
		echo "  👤 Adding $(USER) to the 'input' group (to read /dev/input)"; \
		sudo usermod -aG input $(USER); \
		echo "  ⚠️  Log out and back in for that to take effect."; \
	}
	@echo "  🎯 Setup complete. Try: make test-hid"

uninstall:
	sudo rm -f /etc/udev/rules.d/99-roadie-hid.rules /etc/udev/rules.d/99-roadie-capture.rules && sudo udevadm control --reload-rules

flash-relay:
	python3 board/install.py relay --cp-version $(CP_VERSION)

flash-hid:
	python3 board/install.py hid --cp-version $(CP_VERSION)

flash-relay-quick:
	python3 board/install.py relay --skip-firmware

flash-hid-quick:
	python3 board/install.py hid --skip-firmware

sync:
	python3 board/install.py --sync

sync-relay:
	python3 board/install.py relay --sync

sync-hid:
	python3 board/install.py hid --sync

# Voice input, at three depths, so a failure says which layer broke:
# straight at the relay's serial port, through roadie's /audio-in WebSocket,
# and from a real browser's microphone (Chromium plays the tune into a fake
# mic). All three end the same way: recorded back off the HID board's USB
# microphone and checked note by note.
# Plain "make test-audio" runs all three; the suffixed ones pick a layer.
test-audio: test-audio-serial test-audio-api test-audio-browser

test-audio-serial:
	python3 board/test_audio.py --via serial

test-audio-api:
	python3 board/test_audio.py --via api

test-audio-browser:
	board/e2e/ensure-display.sh python3 board/test_audio.py --via browser

# One look at everything roadie talks to: boards, the input it can drive,
# what comes back from the target, and this machine's audio and displays.
status:
	python3 board/status.py

# Board-level: do the two boards inject correct input? Reads this machine's
# own /dev/input, so it only makes sense with the HID board plugged in here.
test-hid:
	python3 board/test_hid.py

test-hid-quick:
	python3 board/test_hid.py --quick

# End-to-end per target device: input is driven through roadie's HTTP API and
# verified in a real browser running on that target.
#
# Runs on this machine's desktop if it has one, taking over the screen while
# it runs, so you can watch over a VNC session you already have. On a Pi with
# no monitor it makes its own display instead. Set ROADIE_E2E_ISOLATED=1 to
# never touch the desktop, which is what you want for an unattended run.
test-e2e-pi:
	board/e2e/ensure-display.sh python3 board/test_e2e.py --target pi

# Set the password used to watch tests over VNC. Stores only x11vnc's
# obfuscated form -- you chose the password, so a readable copy buys nothing.
set-vnc-password:
	board/e2e/set-vnc-password.sh

# Start a long-lived display you can watch over VNC. Connect your viewer
# once and leave it: test runs reuse this display instead of creating their
# own, so every run appears without reconnecting.
start-vnc-server:
	board/e2e/vnc-server.sh start

stop-vnc-server:
	board/e2e/vnc-server.sh stop

vnc-status:
	board/e2e/vnc-server.sh status

test-e2e-android:
	python3 board/test_e2e.py --target android

test-e2e-iphone:
	python3 board/test_e2e.py --target iphone

test-e2e-pc:
	python3 board/test_e2e.py --target pc

clean:
	rm -f $(BINARY)
