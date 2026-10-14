BINARY = roadie

# CircuitPython version installed by the flash targets.
# Override to track the newest stable release instead of the pin:
#   make flash-hid CP_VERSION=latest
CP_VERSION ?= 10.3.1

.PHONY: build test setup uninstall flash-relay flash-hid flash-relay-quick flash-hid-quick sync sync-hid sync-relay test-hid test-hid-quick test-audio test-e2e-pi test-e2e-pi-vnc test-e2e-android test-e2e-iphone test-e2e-pc clean

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

# Voice input: PCM over the data port to the HID board's USB microphone.
test-audio:
	python3 board/test_audio.py

# Board-level: do the two boards inject correct input? Reads this machine's
# own /dev/input, so it only makes sense with the HID board plugged in here.
test-hid:
	python3 board/test_hid.py

test-hid-quick:
	python3 board/test_hid.py --quick

# End-to-end per target device: input is driven through roadie's HTTP API and
# verified in a real browser running on that target.
#
# Runs on an isolated throwaway display by default. Roadie's input goes to
# whatever has focus, so giving the browser a display of its own is what makes
# the result trustworthy -- and keeps the typed test string out of your
# desktop. Works the same on a Pi with a screen and one without.
test-e2e-pi:
	board/e2e/ensure-display.sh python3 board/test_e2e.py --target pi

# Watch it happen: the isolated display is served over VNC, and the run waits
# until a viewer actually connects. Works with or without a monitor attached.
# See docs/how-to/watch-tests.md.
#
# (To run on an attached monitor instead, set ROADIE_E2E_USE_ATTACHED=1. There
# is no make target for it because input then goes wherever focus is, which
# can drop the test's keystrokes into your desktop.)
test-e2e-pi-vnc:
	ROADIE_E2E_VNC=1 board/e2e/ensure-display.sh python3 board/test_e2e.py --target pi

test-e2e-android:
	python3 board/test_e2e.py --target android

test-e2e-iphone:
	python3 board/test_e2e.py --target iphone

test-e2e-pc:
	python3 board/test_e2e.py --target pc

clean:
	rm -f $(BINARY)
