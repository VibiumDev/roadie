BINARY = roadie
BOARD_DIR = board
CIRCUITPY = /Volumes/CIRCUITPY

.PHONY: build test setup flash-relay flash-hid flash-relay-quick flash-hid-quick clean

build:
	go build -o $(BINARY) .

test:
	go test -race -v ./...

setup:
	python3 -m venv .venv
	.venv/bin/pip install circup adafruit-circuitpython-hid

flash-relay:
	@test -d $(CIRCUITPY) || (echo "CircuitPython board not found at $(CIRCUITPY)" && exit 1)
	cp $(BOARD_DIR)/shared/*.py $(CIRCUITPY)/lib/
	cp $(BOARD_DIR)/relay/*.py $(CIRCUITPY)/

flash-hid:
	@test -d $(CIRCUITPY) || (echo "CircuitPython board not found at $(CIRCUITPY)" && exit 1)
	cp $(BOARD_DIR)/shared/*.py $(CIRCUITPY)/lib/
	cp $(BOARD_DIR)/hid/*.py $(CIRCUITPY)/

flash-relay-quick:
	@test -d $(CIRCUITPY) || (echo "CircuitPython board not found at $(CIRCUITPY)" && exit 1)
	cp $(BOARD_DIR)/relay/code.py $(CIRCUITPY)/code.py

flash-hid-quick:
	@test -d $(CIRCUITPY) || (echo "CircuitPython board not found at $(CIRCUITPY)" && exit 1)
	cp $(BOARD_DIR)/hid/code.py $(CIRCUITPY)/code.py

clean:
	rm -f $(BINARY)
