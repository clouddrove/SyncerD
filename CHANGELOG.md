# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.11] - 2026-06-29

### Changed
- Slack notifications now render as branded Block Kit messages: a colored bar (green for new syncs, red for failures), an emoji header with the count, code-formatted image refs grouped per destination in detailed mode, and a small SyncerD logo footer reading "SyncerD — powered by CloudDrove" (#31)

## [0.0.10] - 2026-06-29

### Fixed
- `make install` still produced `dev (commit: unknown)` because the `install` target ran a plain `go install` without ldflags — now injects version/commit like the other build targets (#29)
- Local build version string now uses the nearest tag without the `v` prefix (e.g. `0.0.9`), matching the released binary format instead of the verbose `git describe` output (#29)

## [0.0.9] - 2026-06-29

### Fixed
- `syncerd -v` always printed `dev (commit: unknown)` because `make build`, `docker build`, and the CI image were built with a plain `go build` that never injected `main.version`/`main.commit`. Version and commit metadata are now injected via ldflags in the Makefile (git-derived), Dockerfile (`VERSION`/`COMMIT` build args), and release workflow (git tag + SHA) (#29)

## [0.0.8] - 2026-06-29

### Fixed
- Slack notifications never fired when only `slack.webhook_url` (or `SYNCERD_SLACK_WEBHOOK_URL`) was set, because `slack.enabled` defaulted to `false` and the notifier was never constructed — no success or failure alerts were sent (#27)

### Changed
- Slack is now opt-out: configuring a webhook URL enables notifications automatically. Set `slack.enabled: false` (or `SYNCERD_SLACK_ENABLED=false`) to suppress
- Bound the previously unbound `slack.enabled`, `slack.notify_on_new`, and `slack.notify_on_error` keys to environment variables

## [0.0.7] - 2026-06-28

### Added
- Helm: `values.schema.json` so chart values are validated on install/template and Artifact Hub shows the "Values schema" badge
- Helm release pipeline now signs the published OCI chart with keyless cosign (Sigstore OIDC), enabling the Artifact Hub "Signed" badge

## [0.0.6] - 2026-06-28

### Security
- Resolved 38 vulnerabilities (1 critical, 14 high, 20 medium, 2 low, 1 unknown) reported by the Artifact Hub / Trivy image scan; 35 were Go `stdlib` CVEs fixed by rebuilding with a newer Go toolchain
- `github.com/docker/cli` bumped `v27.5.1` → `v29.6.1` (CVE-2025-15558)
- `golang.org/x/sys` bumped `v0.15.0` → `v0.46.0` (CVE-2026-39824)

### Changed
- Go 1.23 bumped to Go 1.26 across `go.mod`, `Dockerfile`, and CI workflows (`ci.yml`, `release.yml`, `syncerd.yml`) to pick up patched standard-library crypto/tls, crypto/x509, net, net/url, and net/http fixes

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
