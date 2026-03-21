.PHONY: build install clean run devices status sniff udev

BINARY  := scape-ctl
PKG     := ./cmd/scape-ctl
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.version=$(VERSION)

export GOCACHE ?= $(shell echo $${TMPDIR:-/tmp})/go-cache

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

run: build
	./$(BINARY)

install: build
	install -Dm755 $(BINARY) $(DESTDIR)/usr/local/bin/$(BINARY)

# CLI shortcuts
devices: build
	./$(BINARY) devices

status: build
	./$(BINARY) status

sniff: build
	./$(BINARY) sniff

# Linux udev rule (run with sudo)
udev:
	@echo 'SUBSYSTEMS=="usb*", ATTRS{idVendor}=="36bc", MODE="0666"' | \
		sudo tee /etc/udev/rules.d/50-fractal.rules
	sudo udevadm control --reload-rules
	sudo udevadm trigger
	@echo "Done. Replug your device."

clean:
	rm -f $(BINARY)
	go clean

# Build for all platforms (requires CGO cross-compilation toolchains for hidapi)
# For pure distribution, consider using Docker or Nix.
build-linux:
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-amd64 $(PKG)

build-darwin:
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-darwin-arm64 $(PKG)

build-windows:
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-windows-amd64.exe $(PKG)
