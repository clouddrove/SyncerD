# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.4] - 2026-02-26

### Fixed
- Helm: docker config secret was mounted at `/root/.docker` which is inside `/root` (mode 700); non-root pod user (uid 1000) could not traverse the directory even with `fsGroup` set — mount path changed to `/var/lib/syncerd/.docker` and `DOCKER_CONFIG` updated to match

### Changed
- Docker image is now built for `linux/amd64` and `linux/arm64` (multi-arch manifest); QEMU added to the release pipeline and `--platform=$BUILDPLATFORM` used in the builder stage for native-speed cross-compilation; GitHub Actions layer cache enabled

## [0.0.3] - 2026-02-26

### Fixed
- Helm: `dockerConfigSecret` was incorrectly used as `imagePullSecrets`; separated into a dedicated `imagePullSecrets` value for pulling the SyncerD image and `dockerConfigSecret` for destination registry auth
- Helm: set `DOCKER_CONFIG=/root/.docker` when `dockerConfigSecret` is configured so credentials are found when the pod runs as non-root
- Helm: `state_path` and `SYNCERD_STATE_PATH` are now empty when `persistence.enabled: false`, making stateless mode truly stateless (no file I/O)
- Helm: removed unused `emptyDir` data volume and mount in stateless mode
- `DockerHubRegistry.Authenticate` was a no-op; now validates credentials against the Docker Hub API at startup
- `ListTags` pagination silently swallowed errors and ignored context cancellation; now returns errors and checks `ctx.Err()` between pages; page size increased to 100
- `ImageExists` masked all errors as image-not-found; now only treats HTTP 404 as not-found and propagates all other errors
- Removed deprecated `rand.Seed` call (global source is auto-seeded since Go 1.20)
- Removed dead `getDestinationImageName` no-op switch; removed unused `GetDefaultConfigPath` function
- `--once` flag moved from root persistent flags to the `sync` subcommand where it belongs
- `action.yml`: fixed `--once` boolean flag passing to use `--once=<value>` syntax
- GitHub Actions `syncerd.yml`: removed invalid `secrets` context usage in `if` conditions; replaced with `continue-on-error: true` on optional registry login steps

### Changed
- Go 1.21 (EOL) bumped to Go 1.23 across `go.mod`, `Dockerfile`, and CI workflows
- Docker base image pinned from `alpine:latest` to `alpine:3.21`
- Fixed `go.sum*` glob to `go.sum` in Dockerfile `COPY`

### Added
- Initial release of SyncerD
- Support for syncing images from Docker Hub to multiple registries:
  - AWS ECR
  - Azure ACR
  - Google GCR
  - GitHub Container Registry (GHCR)
- Automatic tag detection and monitoring
- Cron-based scheduling for periodic syncs (default: every 3 weeks)
- YAML-based configuration with environment variable support
- GitHub Actions workflow support
- CLI tool with `--once` flag for one-time syncs
- Support for watching all tags or specific tags per image
- Authentication support for all registry types

### Features
- Multi-registry destination support
- Automatic version detection
- Configurable sync schedules
- Secure authentication methods
- Comprehensive logging
