BINARY = roadie

# "make" on its own builds, as the README says; "make help" lists targets.
.DEFAULT_GOAL := build

# CircuitPython version installed by the flash targets.
# Override to track the newest stable release instead of the pin:
#   make flash-hid CP_VERSION=latest
CP_VERSION ?= 10.3.1

.PHONY: help build test test-unit clean setup uninstall flash-hid flash-relay flash-hid-quick flash-relay-quick sync sync-hid sync-relay test-hid test-hid-quick test-e2e-pi test-audio test-audio-serial test-audio-api test-audio-browser test-view test-e2e-android test-e2e-iphone test-e2e-pc set-vnc-password start-vnc-server stop-vnc-server vnc-status status unstick

# Every target carries a "## description"; help prints them by section.
help:  ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next } \
		/^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' \
		$(MAKEFILE_LIST)
	@echo ""

##@ Build

build:  ## Build the roadie binary
	go build -o $(BINARY) .

test:  ## Unit tests, plus the hardware tests if the boards are connected
	@$(MAKE) --no-print-directory test-unit
	@# The hardware tests need both boards, with the HID board plugged into
	@# this machine so the input it injects comes back here. Where that is
	@# not the case -- a laptop, a CI runner -- say so and stop, rather than
	@# failing in a way that looks like a fault.
	@if ls /dev/serial/by-id/usb-Adafruit_Roadie-Relay_*-if02 >/dev/null 2>&1 \
		&& ls /dev/input/by-id/usb-Adafruit_Roadie-HID_*-event-kbd >/dev/null 2>&1; then \
		echo ""; echo "  🔌 Boards are here — running the hardware tests too"; echo ""; \
		$(MAKE) --no-print-directory test-hid test-e2e-pi test-audio; \
		if command -v chromedriver >/dev/null 2>&1 \
			&& { command -v vibium >/dev/null 2>&1 || command -v npx >/dev/null 2>&1; }; then \
			$(MAKE) --no-print-directory test-view; \
		else \
			echo ""; \
			echo "  ⏭️  Skipping test-view: it needs chromedriver and node."; \
			echo "     Run 'make setup' to install them."; \
		fi; \
	else \
		echo ""; \
		echo "  ⏭️  Skipping the hardware tests: no boards on this machine."; \
		echo "     They need the relay plugged in and the HID board plugged"; \
		echo "     into this machine. See 'make status'."; \
	fi

test-unit:  ## Run only the Go unit tests
	go test -v ./...

clean:  ## Remove the built binary
	rm -f $(BINARY)

##@ Set up this machine

setup:  ## Install dependencies, udev rules and group membership
	python3 board/install.py --setup-only
	sudo cp board/99-roadie-hid.rules board/99-roadie-capture.rules /etc/udev/rules.d/ && sudo udevadm control --reload-rules && sudo udevadm trigger
	@# Everything the browser test needs on a Pi with no display attached:
	@# an X server and the dummy video driver, a window manager so the kiosk
	@# window fills the screen, xset/xrandr/xinput for probing and muting,
	@# a browser to drive, and x11vnc to watch it remotely. chromedriver
	@# and node are for 'make test-view', which drives roadie's own viewer
	@# page over WebDriver BiDi using the vibium CLI.
	@# Skipped on non-apt systems; only the e2e tests need these.
	@command -v apt-get >/dev/null && { \
		echo "  📦 Installing browser-test dependencies"; \
		sudo apt-get install -y -q \
			xserver-xorg-core xserver-xorg-video-dummy \
			matchbox-window-manager x11-xserver-utils xinput \
			chromium chromium-driver x11vnc \
			nodejs npm \
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

uninstall:  ## Remove the udev rules installed by setup
	sudo rm -f /etc/udev/rules.d/99-roadie-hid.rules /etc/udev/rules.d/99-roadie-capture.rules && sudo udevadm control --reload-rules

##@ Flash and sync the boards

flash-hid:  ## Flash CircuitPython and the HID board's code
	python3 board/install.py hid --cp-version $(CP_VERSION)

flash-relay:  ## Flash CircuitPython and the relay board's code
	python3 board/install.py relay --cp-version $(CP_VERSION)

flash-hid-quick:  ## Re-copy the HID board's code, keep its firmware
	python3 board/install.py hid --skip-firmware

flash-relay-quick:  ## Re-copy the relay board's code, keep its firmware
	python3 board/install.py relay --skip-firmware

sync:  ## Copy changed board code to both boards
	python3 board/install.py --sync

sync-hid:  ## Copy changed board code to the HID board
	python3 board/install.py hid --sync

# Voice input, at three depths, so a failure says which layer broke:
# straight at the relay's serial port, through roadie's /audio-in WebSocket,
# and from a real browser's microphone (Chromium plays the tune into a fake
# mic). All three end the same way: recorded back off the HID board's USB
# microphone and checked note by note.
# Plain "make test-audio" runs all three; the suffixed ones pick a layer.

sync-relay:  ## Copy changed board code to the relay board
	python3 board/install.py relay --sync

##@ Test the hardware

test-hid:  ## Keyboard, mouse and touch, checked at /dev/input
	python3 board/test_hid.py

test-hid-quick:  ## The same, skipping the slower checks
	python3 board/test_hid.py --quick

# End-to-end per target device: input is driven through roadie's HTTP API and
# verified in a real browser running on that target.
#
# Runs on this machine's desktop if it has one, taking over the screen while
# it runs, so you can watch over a VNC session you already have. On a Pi with
# no monitor it makes its own display instead. Set ROADIE_E2E_ISOLATED=1 to
# never touch the desktop, which is what you want for an unattended run.

test-e2e-pi:  ## The whole stack into a browser on this machine
	board/e2e/ensure-display.sh python3 board/test_e2e.py --target pi

# Set the password used to watch tests over VNC. Stores only x11vnc's
# obfuscated form -- you chose the password, so a readable copy buys nothing.

test-audio: test-audio-serial test-audio-api test-audio-browser  ## Voice input through all three layers below

test-audio-serial:  ## Voice input straight at the relay's serial port
	python3 board/test_audio.py --via serial

test-audio-api:  ## Voice input through roadie's /audio-in WebSocket
	python3 board/test_audio.py --via api

test-audio-browser:  ## Voice input from a real browser's microphone
	board/e2e/ensure-display.sh python3 board/test_audio.py --via browser

# The tests above supply their own page. This one drives roadie's own
# viewer: it finds the mic button over WebDriver BiDi (with the vibium
# CLI, attached to a browser the test launches) and clicks it with the
# real USB mouse, so the whole operator path is under test at once.

test-view:  ## Voice input through roadie's own /view page, clicked for real
	board/e2e/ensure-display.sh python3 board/test_view.py

# One look at everything roadie talks to: boards, the input it can drive,
# what comes back from the target, and this machine's audio and displays.

##@ Test against another device

test-e2e-android:  ## The whole stack into Chrome on an Android phone
	python3 board/test_e2e.py --target android

test-e2e-iphone:  ## The whole stack into Safari on an iPhone
	python3 board/test_e2e.py --target iphone

test-e2e-pc:  ## The whole stack into a browser on another computer
	python3 board/test_e2e.py --target pc

##@ Watch a run

set-vnc-password:  ## Set the password for watching over VNC
	board/e2e/set-vnc-password.sh

# Start a long-lived display you can watch over VNC. Connect your viewer
# once and leave it: test runs reuse this display instead of creating their
# own, so every run appears without reconnecting.

start-vnc-server:  ## Start a display you can watch test runs in
	board/e2e/vnc-server.sh start

stop-vnc-server:  ## Stop that display
	board/e2e/vnc-server.sh stop

vnc-status:  ## Is it running, and is anything watching?
	board/e2e/vnc-server.sh status

##@ Diagnose

status:  ## What's connected: boards, input, audio, displays
	python3 board/status.py

# Recover from a button or contact left held, and from an X server that has
# stopped delivering that device's clicks.

unstick:  ## Recover input that has got stuck
	board/unstick.sh

# Board-level: do the two boards inject correct input? Reads this machine's
# own /dev/input, so it only makes sense with the HID board plugged in here.
