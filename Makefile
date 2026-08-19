.PHONY: build test test-live clean install lint

# Version info injected at build time
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# Build the binary
build:
	go build -ldflags "$(LDFLAGS)" -o syncerd ./main.go

# Run tests
test:
	go test -v ./...

# Run the live provider tests. These talk to a real GitHub account, create
# two throwaway repositories, use them, and delete them again. They are the
# only tests that prove a real API call works: everything else is checked
# against fakes written by the same hand as the code.
#
#   SYNCERD_LIVE_GITHUB_TOKEN=ghp_...   token with repo scope
#   SYNCERD_LIVE_GITHUB_OWNER=acme      account or org to create under
#   SYNCERD_LIVE_KEEP=1                 optional, skip cleanup to inspect
test-live:
	go test -tags live -v -timeout 15m ./internal/livetest/

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
