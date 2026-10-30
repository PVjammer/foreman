.PHONY: build install test

build:
	go build -o bin/foreman ./cmd/foreman

# Copies the binary, then sets up the machine: herdr keybindings + plugin, and Claude Code
# settings so Claude agents can run foreman (see `foreman install --help`).
install:
	go build -o $(HOME)/.local/bin/foreman ./cmd/foreman
	$(HOME)/.local/bin/foreman install

test:
	go test ./...
