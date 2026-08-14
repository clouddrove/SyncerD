# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.2] - 2026-08-14

### Added
- Native authentication for private Amazon ECR destinations: when a destination host matches `<account>.dkr.ecr.<region>.amazonaws.com`, SyncerD calls `ecr:GetAuthorizationToken` through the standard AWS credential chain (environment variables, shared config, IRSA, instance role) instead of requiring a `docker login`. Tokens are cached per account and region and refetched before they expire, so a long-running sync no longer needs an external refresh loop, and a Kubernetes deployment using IRSA no longer needs a `dockerConfigSecret` for ECR. An existing docker credential entry for the same registry still wins, so setups that log in explicitly are unchanged. Public ECR (`public.ecr.aws`) is a different service and is not covered

### Fixed
- The GitHub Action could not authenticate to any destination registry that was logged into with `docker/login-action` or `aws-actions/amazon-ecr-login`: it runs as a Docker container action, which does not receive the runner's `~/.docker/config.json`, so every request failed with `401 Unauthorized`. ECR now works with AWS credentials alone, and the README documents the `DOCKER_CONFIG` workspace path that makes the other registries' login steps visible to the container

## [0.1.1] - 2026-08-14

### Added
- `--report <path>` writes a machine readable JSON run summary after each run, one schema shared by `sync` and `git-sync` so a consumer does not need to know which command produced the file; written on failed runs too, since those are the ones worth inspecting, and a write failure never fails the run
- `--log-format json` opt in structured logging for `sync` and `git-sync`; text remains the default and reproduces the historical `log.Printf` output byte for byte, since operators grep it. JSON carries per repository and per run values as real fields
- `--metrics-file <path>` writes Prometheus textfile collector metrics (`syncerd_last_run_unixtime`, `syncerd_last_success_unixtime`, `syncerd_last_run_success`, `syncerd_last_run_duration_seconds`, `syncerd_last_run_items`) after each run, for both `sync` and `git-sync`, including failed runs; `sync` and `git-sync` series coexist in the same file. A `git-sync --dry-run` writes no metrics, since it created, pushed, and deleted nothing
- GitHub Action: new `dry-run`, `report`, `metrics-file`, and `log-format` inputs. `dry-run` is the safety guard for git mirroring, whose default push mode deletes destination refs absent at the source, and it was previously unreachable from a workflow (#38)
- Azure DevOps extension: documented git mirroring, including how provider tokens are supplied through pipeline variables. No code change was needed, since the task's `command` input already accepted any subcommand (#38)

### Changed
- A run time failure (a bad config, an unreachable provider) now prints only the error, without cobra's flag usage dump that used to follow it; the error is routed through the same logger as every other line, so a `--log-format json` run stays parseable end to end. A flag parse error (a typo like `--bogus-flag`) is unaffected and still prints the usage block, since that failure never reaches the point where usage gets silenced

### Fixed
- `internal/state`'s atomic write no longer leaves a stray `.tmp` file behind when the rename fails, the same fix already applied to the run report writer
- The README's GitHub Action example for git mirroring omitted the `env:` block carrying provider tokens, so anyone copying it got `token is required` (#38)

## [0.1.0] - 2026-08-11

### Added
- New `syncerd git-sync` subcommand mirrors git repositories across GitHub, GitLab, Bitbucket, Azure DevOps, and AWS CodeCommit, in any direction. Repositories are discovered from the source provider and filtered by name glob, archived, and fork status; unchanged repositories are detected via a source ref fingerprint and skipped without cloning
- CodeCommit's git transport needs static IAM HTTPS Git credentials (`git_username` / `git_password`, or `SYNCERD_GIT_<NAME>_GIT_USERNAME` / `SYNCERD_GIT_<NAME>_GIT_PASSWORD`): SyncerD does not derive SigV4 git credentials, so IRSA and instance roles only cover the API (listing and creating repositories)
- Azure DevOps supports an `entra` auth mode where the operator supplies a Microsoft Entra ID access token through the same `SYNCERD_GIT_<NAME>_TOKEN` variable used for a PAT; SyncerD never acquires one itself
- Bitbucket support is Cloud only (`api_url` changes the host but not the Cloud shaped request paths, so Bitbucket Data Center is not supported), and Bitbucket has no archived concept, so `skip_archived` has no effect on a Bitbucket source
- Three push modes control how a mirror updates its destination: `mirror` (default) replicates the source exactly, deleting destination branches and tags absent at source; `additive` never deletes; `fast-forward` refuses any non fast-forward update and reports it as a failure
- An adopt guard refuses to push to a destination that already has content and no prior mirror state, so a misconfigured mirror cannot silently overwrite existing work; set `adopt: true` on the mirror to opt in
- `--dry-run` reports what would be created, pushed, and pruned, per repository and per ref; it creates, pushes, and deletes nothing at the destination, though it does populate the local clone cache, since `git push --dry-run` needs the objects available locally
- Helm chart: opt-in `gitSync` CronJob (`gitSync.enabled: false` by default) runs on its own schedule, separate from image sync, with its own persistence for the clone cache and mirror state
- Docker image now installs `git`, required for `git-sync`; image sync behaviour is unchanged
- GitHub Action: new `command` input selects `sync` or `git-sync` (defaults to `sync`)
- Helm chart: `secret.gitTokens` and `secret.gitCredentials` hold inline git provider tokens and CodeCommit static credentials, keyed by provider name; prefer `existingSecret` in production
- Helm chart: `cronjob.enabled` (default `true`) runs the image sync CronJob; set `false` for a deployment that only wants git mirroring

### Fixed
- Docker image `ENTRYPOINT` was relative (`./syncerd`), so container actions running with `--workdir /github/workspace` failed with `exec: "./syncerd": stat ./syncerd: no such file or directory`; now absolute (`/app/syncerd`)
- The Action referenced image tag `v1`, which was never published; pinned to `v0`, the floating major tag the release workflow actually publishes for the current 0.x line

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
