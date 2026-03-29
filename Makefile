BINARY = roadie

.PHONY: build test setup flash-relay flash-hid flash-relay-quick flash-hid-quick sync sync-hid sync-relay test-circular clean

build:
	go build -o $(BINARY) .

test:
	go test -v ./...

setup:
	python3 board/install.py --setup-only
	sudo cp board/99-roadie-hid.rules /etc/udev/rules.d/ && sudo udevadm control --reload-rules

flash-relay:
	python3 board/install.py relay

flash-hid:
	python3 board/install.py hid

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

test-circular:
	python3 board/test_circular.py

clean:
	rm -f $(BINARY)
