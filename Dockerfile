# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o syncerd ./main.go

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/syncerd .

# Copy example config
COPY syncerd.yaml.example .

ENTRYPOINT ["./syncerd"]
