.PHONY: build test clean install lint

# Version info injected at build time
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# Build the binary
build:
	go build -ldflags "$(LDFLAGS)" -o syncerd ./main.go

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	rm -f syncerd
	go clean

# Install to GOPATH/bin
install:
	go install -ldflags "$(LDFLAGS)" ./...

# Lint code
lint:
	golangci-lint run

# Run with example config
run-example:
	cp syncerd.yaml.example syncerd.yaml
	./syncerd sync --once

# Build for multiple platforms
build-all:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o syncerd-linux-amd64 ./main.go
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o syncerd-darwin-amd64 ./main.go
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o syncerd-darwin-arm64 ./main.go
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o syncerd-windows-amd64.exe ./main.go
