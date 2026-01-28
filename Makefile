.PHONY: build test clean install lint

# Build the binary
build:
	go build -o syncerd ./main.go

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	rm -f syncerd
	go clean

# Install to GOPATH/bin
install:
	go install ./...

# Lint code
lint:
	golangci-lint run

# Run with example config
run-example:
	cp syncerd.yaml.example syncerd.yaml
	./syncerd sync --once

# Build for multiple platforms
build-all:
	GOOS=linux GOARCH=amd64 go build -o syncerd-linux-amd64 ./main.go
	GOOS=darwin GOARCH=amd64 go build -o syncerd-darwin-amd64 ./main.go
	GOOS=darwin GOARCH=arm64 go build -o syncerd-darwin-arm64 ./main.go
	GOOS=windows GOARCH=amd64 go build -o syncerd-windows-amd64.exe ./main.go
