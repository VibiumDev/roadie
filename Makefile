BINARY = roadie

.PHONY: build test setup flash-relay flash-hid flash-relay-quick flash-hid-quick clean

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

clean:
	rm -f $(BINARY)
