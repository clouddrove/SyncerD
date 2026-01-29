<p align="center">
  <img src="assets/syncerd-logo.png" alt="SyncerD logo" width="300">
</p>
<h1 align="center">
  SyncerD
</h1>
<p align="center">Your lightweight Docker registry sync engine.</p>
<p align="center">
  <a href="https://goreportcard.com/report/github.com/clouddrove/syncerd">
    <img src="https://goreportcard.com/badge/github.com/clouddrove/syncerd" alt="Go Report Card">
  </a>
  <a href="https://opensource.org/licenses/MIT">
    <img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="License: MIT">
  </a>
</p>

SyncerD is a powerful Go tool for synchronizing Docker images from Docker Hub to other container registries (ECR, ACR, GCR, GitHub Container Registry). Automatically monitors for new versions and syncs them on a schedule.

## Features

- 🔄 **Multi-Registry Support**: Sync to AWS ECR, Azure ACR, Google GCR, and GitHub Container Registry
- 🔍 **Automatic Version Detection**: Monitors Docker Hub for new tags and automatically syncs them
- ⏰ **Scheduled Syncs**: Built-in cron scheduler (default: every 3 weeks)
- 🚀 **GitHub Actions Ready**: Use as a GitHub Action for automated syncing
- 🖥️ **CLI Tool**: Run from terminal for manual or scheduled syncing
- 🔒 **Secure**: Supports multiple authentication methods for all registries
- 📝 **Configurable**: YAML-based configuration with environment variable support

## Installation

### From Source

```bash
git clone https://github.com/clouddrove/syncerd.git
cd syncerd
go build -o syncerd ./main.go
```

### Using Go Install

```bash
go install github.com/clouddrove/syncerd@latest
```

## Use as a GitHub Action (Marketplace)

Add SyncerD to your workflow:

```yaml
- uses: clouddrove/syncerd@v1
  with:
    config: syncerd.yaml
    once: "true"
```

Notes:
- SyncerD reads destination registry auth from **Docker credentials**. In GitHub Actions, run the appropriate login steps (`docker/login-action`, `aws-actions/amazon-ecr-login`, `azure/docker-login`, `gcloud auth configure-docker`) *before* SyncerD.

## Quick Start

1. **Create a configuration file** (`syncerd.yaml`):

```yaml
source:
  type: dockerhub
  registry: docker.io

destinations:
  - name: my-ecr
    type: ecr
    registry: 123456789012.dkr.ecr.us-east-1.amazonaws.com
    region: us-east-1

images:
  - name: library/nginx
    watch_tags: true
```

2. **Set up authentication** (via environment variables or config file):

```bash
export SYNCERD_SOURCE_USERNAME=your-dockerhub-username
export SYNCERD_SOURCE_PASSWORD=your-dockerhub-password
```

3. **Run the sync**:

```bash
# Run once
./syncerd sync --once

# Run continuously with cron schedule
./syncerd sync
```

## Configuration

See `syncerd.yaml.example` for a complete configuration example.

### Source Configuration

```yaml
source:
  type: dockerhub
  registry: docker.io
  username: your-username  # Optional
  password: your-password  # Optional
  token: your-token        # Optional (preferred)
```

### Destination Configuration

#### AWS ECR

```yaml
destinations:
  - name: my-ecr
    type: ecr
    registry: 123456789012.dkr.ecr.us-east-1.amazonaws.com
    region: us-east-1
```

**Authentication**: SyncerD reads destination credentials from your **Docker credential config**
(what `docker login` writes). In GitHub Actions, use `aws-actions/amazon-ecr-login` (or `docker login`)
before running SyncerD.

#### Azure ACR

```yaml
destinations:
  - name: my-acr
    type: acr
    registry: myregistry.azurecr.io
```

**Authentication**: use `azure/docker-login` (or `docker login`) so credentials are available to SyncerD.

#### Google GCR

```yaml
destinations:
  - name: my-gcr
    type: gcr
    registry: gcr.io
```

**Authentication**: use `gcloud auth configure-docker` (or `docker login`) so credentials are available to SyncerD.

#### GitHub Container Registry

```yaml
destinations:
  - name: my-ghcr
    type: ghcr
    registry: ghcr.io
```

**Authentication**: use `docker/login-action` to `ghcr.io` (or `docker login`) so credentials are available to SyncerD.

### Image Configuration

```yaml
images:
  - name: library/nginx
    tags: []           # Empty = all tags (if watch_tags is true)
    watch_tags: true   # Monitor for new tags

  - name: library/alpine
    tags:              # Specific tags to sync
      - latest
      - 3.18
      - 3.19
    watch_tags: false  # Only sync specified tags
```

### Schedule Configuration

```yaml
# Cron format: minute hour day month weekday
schedule: "0 0 */21 * *"  # Every 3 weeks at midnight UTC
```

### State Configuration

```yaml
# Persistent state file (tracks which tags were already synced)
state_path: ".syncerd-state.json"
```

### Slack Notifications (optional)

```yaml
slack:
  enabled: true
  webhook_url: "https://hooks.slack.com/services/XXX/YYY/ZZZ"
  channel: "#platform-alerts"  # optional
  notify_on_new: true
  notify_on_error: true
  message_format: "compact" # "compact" | "detailed"
```

### Failure behavior

```yaml
# false = best-effort per image/tag (default)
# true  = stop the whole run on first error
fail_fast: false
```

## Usage

### Command Line

```bash
# Run sync once and exit
syncerd sync --once

# Run continuously with cron schedule (from config)
syncerd sync

# Use custom config file
syncerd sync --config /path/to/config.yaml
```

### GitHub Actions

See `.github/workflows/syncerd.yml` for an example workflow.

```yaml
- name: Run syncerd
  env:
    SYNCERD_SOURCE_USERNAME: ${{ secrets.DOCKERHUB_USERNAME }}
    SYNCERD_SOURCE_PASSWORD: ${{ secrets.DOCKERHUB_PASSWORD }}
  run: |
    syncerd sync --once
```

### CI & Releases

- **CI**: `.github/workflows/ci.yml` runs `gofmt`, `go test`, and `go build`.
- **Go releases**: `.github/workflows/release.yml` uses GoReleaser when you push a `v*` tag.
- **Docker image**: the same release workflow builds & pushes to `ghcr.io/<org>/syncerd`.

## Environment Variables

All configuration can be overridden via environment variables with the `SYNCERD_` prefix:

- `SYNCERD_SOURCE_USERNAME`
- `SYNCERD_SOURCE_PASSWORD`
- `SYNCERD_SOURCE_TOKEN`
- `SYNCERD_SCHEDULE`
- `SYNCERD_STATE_PATH`
- `SYNCERD_SLACK_WEBHOOK_URL`
- `SYNCERD_SLACK_CHANNEL`
- `SYNCERD_SLACK_MESSAGE_FORMAT`
- `SYNCERD_FAIL_FAST`

## Authentication

### Docker Hub

- Username/Password
- Personal Access Token (recommended)

### Destination registries (ECR/ACR/GCR/GHCR)

SyncerD uses `authn.DefaultKeychain`, meaning it will pick up credentials from:

- `docker login` (writes `~/.docker/config.json`)
- Docker credential helpers (osxkeychain, wincred, pass, etc.)
- GitHub Actions login steps (recommended per registry)

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Acknowledgments

- Built with [go-containerregistry](https://github.com/google/go-containerregistry)
- Inspired by the need to work around Docker Hub rate limits

## Support

For issues, questions, or contributions, please open an issue on [GitHub](https://github.com/clouddrove/syncerd/issues).
