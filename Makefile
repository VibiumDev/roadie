BINARY = roadie

.PHONY: build test setup flash-relay flash-hid flash-relay-quick flash-hid-quick test-circular clean

build:
	go build -o $(BINARY) .

test:
	go test -v ./...

setup:
	python3 board/install.py --setup-only

flash-relay:
	python3 board/install.py relay

flash-hid:
	python3 board/install.py hid

flash-relay-quick:
	python3 board/install.py relay --skip-firmware

flash-hid-quick:
	python3 board/install.py hid --skip-firmware

test-circular:
	python3 board/test_circular.py

clean:
	rm -f $(BINARY)
