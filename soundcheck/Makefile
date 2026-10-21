# Builds the soundcheck server and puts it on its exe.dev VM. See README.md.
#
#   make deploy                  build for the VM, copy it over, (re)start it
#   make deploy VM=othername     the same, for a VM with another name

# The exe.dev VM, its address, and the domain the page is served at.
VM ?= soundcheck
HOST ?= $(VM).exe.xyz
DOMAIN ?= soundcheck.vibium.dev

# The VM's processor (amd64 or arm64). Left empty, vm-build asks the VM.
ARCH ?=

ADDR ?= :8000

.DEFAULT_GOAL := build
.PHONY: help build test run vm-build deploy check

help:  ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?##/ \
		{ printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build:  ## Build it for this machine
	go build -o soundcheck .

test:  ## Run its tests
	go test .

run: build  ## Serve it at http://localhost:8000/ (ADDR to change)
	./soundcheck -addr $(ADDR)

vm-build:  ## Build it for the VM's processor, as soundcheck-vm
	@arch="$(ARCH)"; \
	if [ -z "$$arch" ]; then \
		arch=$$(ssh $(HOST) dpkg --print-architecture) || { \
			echo "couldn't ask $(HOST) its processor; set ARCH=amd64 or ARCH=arm64"; exit 1; }; \
	fi; \
	echo "building for linux/$$arch"; \
	GOOS=linux GOARCH=$$arch CGO_ENABLED=0 go build -o soundcheck-vm .

deploy: test vm-build  ## Build for the VM, copy it over with its service, and (re)start it
	scp soundcheck-vm soundcheck.service $(HOST):~/
	ssh $(HOST) ' \
		set -e; \
		sudo install -m 755 ~/soundcheck-vm /usr/local/bin/soundcheck; \
		sudo install -m 644 ~/soundcheck.service /etc/systemd/system/; \
		rm ~/soundcheck-vm ~/soundcheck.service; \
		sudo systemctl daemon-reload; \
		sudo systemctl enable soundcheck; \
		sudo systemctl restart soundcheck; \
		curl -sf -o /dev/null --retry 5 --retry-connrefused http://localhost:8000/; \
		echo "serving on 8000"'

check:  ## Check the hosted pages and the microphone header
	@curl -sfI https://$(DOMAIN)/ | grep -i -e '^HTTP' -e '^permissions-policy'
	@curl -sfI https://$(DOMAIN)/white | grep -i '^HTTP'
