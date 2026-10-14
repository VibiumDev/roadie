BINARY = roadie

.PHONY: build test setup uninstall install-service uninstall-service flash-relay flash-hid flash-relay-quick flash-hid-quick sync sync-hid sync-relay test-circular test-audio clean

build:
	go build -o $(BINARY) .

test:
	go test -v ./...

setup:
	python3 board/install.py --setup-only
	sudo cp board/99-roadie-hid.rules board/99-roadie-capture.rules /etc/udev/rules.d/ && sudo udevadm control --reload-rules && sudo udevadm trigger

uninstall:
	sudo rm -f /etc/udev/rules.d/99-roadie-hid.rules /etc/udev/rules.d/99-roadie-capture.rules && sudo udevadm control --reload-rules

install-service: build
	test -f roadie.env || cp roadie.env.example roadie.env
	sed -e "s|@USER@|$$(id -un)|g" -e "s|@HOME@|$(HOME)|g" roadie.service | sudo tee /etc/systemd/system/roadie.service > /dev/null
	sudo systemctl daemon-reload
	sudo systemctl enable --now roadie
	systemctl status roadie --no-pager -l

uninstall-service:
	sudo systemctl disable --now roadie || true
	sudo rm -f /etc/systemd/system/roadie.service
	sudo systemctl daemon-reload

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

test-audio:
	python3 board/test_audio.py

test-circular:
	python3 board/test_circular.py

clean:
	rm -f $(BINARY)
