# Build stage
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary for the target platform
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -installsuffix cgo \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o syncerd ./main.go

# Final stage
FROM alpine:3.21

# git is required by the git-sync subcommand, which shells out to it
RUN apk --no-cache add ca-certificates git \
    && mkdir -p /var/lib/syncerd/git \
    && chown -R 1000:1000 /var/lib/syncerd/git

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/syncerd .

# Copy example config
COPY syncerd.yaml.example .

ENTRYPOINT ["./syncerd"]
