# Build stage
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary for the target platform
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -installsuffix cgo -o syncerd ./main.go

# Final stage
FROM alpine:3.21

RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/syncerd .

# Copy example config
COPY syncerd.yaml.example .

ENTRYPOINT ["./syncerd"]
