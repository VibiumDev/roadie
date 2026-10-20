BINARY = roadie

# "make" on its own builds, as the README says; "make help" lists targets.
.DEFAULT_GOAL := build

# CircuitPython version installed by the flash targets.
# Override to track the newest stable release instead of the pin:
#   make flash-hid CP_VERSION=latest
CP_VERSION ?= 10.3.1

.PHONY: help build test test-unit clean setup uninstall flash-hid flash-relay flash-hid-quick flash-relay-quick sync sync-hid sync-relay test-hid test-hid-quick test-e2e-pi test-audio test-audio-serial test-audio-api test-audio-browser test-view multitouch-on multitouch-off multitouch-status test-multitouch test-e2e-android test-e2e-iphone test-e2e-pc set-vnc-password start-vnc-server stop-vnc-server vnc-status status unstick gadget-install gadget-uninstall gadget-status test-gadget test-gadget-install test-gadget-loopback test-phone-net

# Every target carries a "## description"; help prints them by section.
help:  ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next } \
		/^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 }' \
		$(MAKEFILE_LIST)
	@echo ""

RULE := ──────────────────────────────────────────────────────────────

##@ Build

build:  ## Build the roadie binary
	go build -o $(BINARY) .

test:  ## Unit tests, plus the hardware tests if the boards are connected
	@# Not "go test -v": two hundred lines of PASS buries everything after
	@# it. "make test-unit" is there when the detail is what you want.
	@printf "\n▶ unit  ·  the Go unit tests\n"
	@if out=$$(go test ./... 2>&1); then \
		printf "  ✅ %-18s %s\n" "unit" \
			"$$(printf '%s' "$$out" | tail -1 | tr -s '\t' ' ')"; \
	else \
		printf '%s\n' "$$out" | sed 's/^/  /'; \
		printf "\n  ❌ unit tests failed\n\n"; exit 1; \
	fi
	@if out=$$(gadget/test_install 2>&1); then \
		printf "  ✅ %-18s %s\n" "setup scripts" "install, uninstall, machine-setup (dry runs)"; \
	else \
		printf '%s\n' "$$out" | sed 's/^/  /'; exit 1; \
	fi
	@# A machine running the linux-gadget backend gets its gadget checked.
	@if [ -d /sys/kernel/config/usb_gadget/roadie ]; then \
		$(MAKE) --no-print-directory test-gadget || exit 1; \
	fi
	@# Every sub-make below ends in "|| exit 1". Without it a failing test
	@# did not stop this compound line, the recipe took its status from the
	@# last printf, and the run ended by announcing that everything passed.
	@# The hardware tests need both boards, with the HID board plugged into
	@# this machine so the input it injects comes back here. Where that is
	@# not the case -- a laptop, a CI runner -- say so and stop, rather than
	@# failing in a way that looks like a fault.
	@if ls /dev/serial/by-id/usb-Adafruit_Roadie-Relay_*-if02 >/dev/null 2>&1 \
		&& ls /dev/input/by-id/usb-Adafruit_Roadie-HID_*-event-kbd >/dev/null 2>&1; then \
		printf "  🔌 %-18s running the hardware tests too\n" "boards detected"; \
		$(MAKE) --no-print-directory test-hid test-e2e-pi test-audio || exit 1; \
		if command -v chromedriver >/dev/null 2>&1 \
			&& { command -v vibium >/dev/null 2>&1 || command -v npx >/dev/null 2>&1; }; then \
			$(MAKE) --no-print-directory test-view || exit 1; \
		else \
			printf "\n▶ test-view\n  ⏭️  %-18s needs chromedriver and node; run 'make setup'\n" "skipped"; \
		fi; \
		printf "\n$(RULE)\n  🎸 all tests passed\n$(RULE)\n\n"; \
	else \
		printf "\n  ⏭️  %-18s no boards on this machine\n" "hardware tests"; \
		printf "     They need the relay plugged in and the HID board\n"; \
		printf "     plugged into this machine. See 'make status'.\n"; \
		printf "\n$(RULE)\n  ✅ unit tests passed (hardware tests skipped)\n$(RULE)\n\n"; \
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
	@python3 board/test_hid.py

test-hid-quick:  ## The same, skipping the slower checks
	@python3 board/test_hid.py --quick

# End-to-end per target device: input is driven through roadie's HTTP API and
# verified in a real browser running on that target.
#
# Runs on this machine's desktop if it has one, taking over the screen while
# it runs, so you can watch over a VNC session you already have. On a Pi with
# no monitor it makes its own display instead. Set ROADIE_E2E_ISOLATED=1 to
# never touch the desktop, which is what you want for an unattended run.

test-e2e-pi:  ## The whole stack into a browser on this machine
	@board/e2e/ensure-display.sh python3 board/test_e2e.py --target pi

# Set the password used to watch tests over VNC. Stores only x11vnc's
# obfuscated form -- you chose the password, so a readable copy buys nothing.

test-audio: test-audio-serial test-audio-api test-audio-browser  ## Voice input through all three layers below

test-audio-serial:  ## Voice input straight at the relay's serial port
	@python3 board/test_audio.py --via serial

test-audio-api:  ## Voice input through roadie's /audio-in WebSocket
	@python3 board/test_audio.py --via api

test-audio-browser:  ## Voice input from a real browser's microphone
	@board/e2e/ensure-display.sh python3 board/test_audio.py --via browser

# The tests above supply their own page. This one drives roadie's own
# viewer: it finds the mic button over WebDriver BiDi (with the vibium
# CLI, attached to a browser the test launches) and clicks it with the
# real USB mouse, so the whole operator path is under test at once.

test-view:  ## Voice input through roadie's own /view page, clicked for real
	@board/e2e/ensure-display.sh python3 board/test_view.py

# One look at everything roadie talks to: boards, the input it can drive,
# what comes back from the target, and this machine's audio and displays.

##@ Linux gadget backend (Raspberry Pi 4 as the phone's USB devices)

# Instead of the two boards, the Pi's USB-C port becomes the phone's
# keyboard, mouse, touchscreen, microphone, speaker and Ethernet adapter.
# See docs/how-to/linux-gadget.md, including how to power the Pi safely.

gadget-install: build  ## Set this Pi up as the phone's USB devices (then reboot)
	sudo gadget/install --user $(USER)

gadget-uninstall:  ## Remove it all, without touching the Pi's own network
	sudo gadget/uninstall

gadget-status:  ## The gadget, its device nodes, and whether a phone is attached
	@gadget/roadie-gadget status

test-gadget:  ## Check the installed gadget: functions, nodes, audio, network
	@printf "\n▶ test-gadget  ·  the USB gadget on this machine\n"
	@gadget/roadie-gadget check

test-gadget-install:  ## Install, uninstall and machine-setup, changing nothing (no hardware)
	@gadget/test_install

test-gadget-loopback:  ## The gadget end to end through this Pi's own USB host (no phone; needs sudo)
	@python3 gadget/test_loopback.py

test-phone-net:  ## The phone's network (DHCP, DNS, NAT, isolation) on a virtual link (needs sudo)
	@python3 gadget/test_loopback.py --phone-net-only

##@ Multi-touch (Linux hosts only)

# The board always sends two contacts. What decides whether the host sees
# them is which kernel driver claims it, and one driver claims the whole
# USB HID interface -- so multi-touch and a separate mouse device cannot
# both be had at once. A phone target chooses for itself and needs none of
# this.

multitouch-on:  ## Let this host see two contacts (loses the mouse device)
	@board/multitouch.sh on

multitouch-off:  ## Back to a separate mouse and single-touch
	@board/multitouch.sh off

multitouch-status:  ## Which mode this host is in
	@board/multitouch.sh status

test-multitouch:  ## Two contacts and gestures, checked at /dev/input
	@board/e2e/ensure-display.sh python3 board/test_multitouch.py

##@ Test against another device

test-e2e-android:  ## The whole stack into Chrome on an Android phone (opens it by keyboard)
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
