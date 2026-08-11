<p align="center">
  <img src="assets/syncerd-logo.png" alt="SyncerD logo" width="280">
</p>

<h1 align="center">SyncerD</h1>
<p align="center">
  <strong>Your lightweight Docker registry and git repository sync engine.</strong>
</p>
<p align="center">
  Sync images from Docker Hub to ECR, ACR, GCR & GHCR, and mirror git repositories between GitHub and GitLab. Beat rate limits, run anywhere.
</p>

<p align="center">
  <a href="https://github.com/clouddrove/syncerd"><img src="https://img.shields.io/github/stars/clouddrove/syncerd?style=social" alt="GitHub stars"></a>
  <a href="https://goreportcard.com/report/github.com/clouddrove/syncerd"><img src="https://goreportcard.com/badge/github.com/clouddrove/syncerd" alt="Go Report Card"></a>
  <a href="https://github.com/clouddrove/syncerd/releases"><img src="https://img.shields.io/github/v/release/clouddrove/syncerd?include_prereleases" alt="Release"></a>
  <a href="https://opensource.org/licenses/MIT"><img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="License: MIT"></a>
  <a href="https://artifacthub.io/packages/helm/syncerd/syncerd"><img src="https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/syncerd" alt="Artifact Hub"></a>
</p>

---

## Why SyncerD?

Docker Hub's [rate limits](https://docs.docker.com/docker-hub/download-rate-limit/) can block pulls in CI and production. **SyncerD** copies images from Docker Hub to your own registries (AWS ECR, Azure ACR, Google GCR, GitHub Container Registry) so you pull from your registry instead — no rate-limit headaches, same images. SyncerD also mirrors git repositories between GitHub and GitLab, so a backup or internal copy of your repos stays in sync on its own schedule.

- **One config, many registries** — Sync the same set of images to ECR, ACR, GCR, and GHCR from a single YAML.
- **Runs everywhere** — CLI, GitHub Actions, Kubernetes (Helm CronJob). Stateless by default; no DB required.
- **New tags, automatically** — Watches source tags and syncs only what's missing; optional branded Slack alerts (color-coded Block Kit) on new syncs or failures.
- **Git mirroring**: Mirror repositories between GitHub and GitLab with filtered discovery, safe push modes, and a guard against overwriting a destination that already has content. See [Git mirroring](#git-mirroring).

---

## Table of contents

- [Quick start](#quick-start)
- [Features](#features)
- [Installation](#installation)
- [Usage](#usage)
- [Examples](#examples)
- [Configuration](#configuration)
- [Git mirroring](#git-mirroring)
- [Contributing & support](#contributing--support)

---

## Quick start

**1. Install** (binary or Go)

```bash
# Binary (see Releases for your OS)
go install github.com/clouddrove/syncerd@latest
```

**2. Add a config** — e.g. `syncerd.yaml`:

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

**3. Run** (use Docker credentials for destinations; [see Auth](#authentication))

```bash
export SYNCERD_SOURCE_USERNAME=your-dockerhub-user
export SYNCERD_SOURCE_PASSWORD=your-dockerhub-token
./syncerd sync --once
```

That's it. Use the same config in [GitHub Actions](#use-as-a-github-action-marketplace), [Azure DevOps](#azure-devops-pipeline), or [Kubernetes (Helm)](#run-with-helm-kubernetes) for scheduled syncs.

---

## Features

| Feature | Description |
|--------|-------------|
| **Multi-registry** | Sync to AWS ECR, Azure ACR, Google GCR, GitHub Container Registry |
| **Auto tag watch** | Detects new tags and syncs only what's missing (optional persistent state) |
| **Scheduled runs** | Built-in cron (e.g. every 3 weeks) or use K8s CronJob / GitHub Actions |
| **GitHub Action** | [Marketplace action](https://github.com/marketplace) — drop into workflows |
| **Helm chart** | Run as a CronJob on Kubernetes; stateless by default (no PVC) |
| **Slack** | Optional branded Block Kit alerts (color-coded, per-destination grouping) on new syncs and failures (compact/detailed) |
| **Secure** | Docker Hub via env/secret; destinations via Docker credential config |
| **Git mirroring** | Mirror repositories between GitHub and GitLab (`git-sync`); filtered discovery, safe push modes, dry run |

---

## Installation

| Method | Command |
|--------|---------|
| **Go install** | `go install github.com/clouddrove/syncerd@latest` |
| **From source** | `git clone https://github.com/clouddrove/syncerd.git && cd syncerd && go build -o syncerd ./main.go` |
| **Releases** | Download the [latest release](https://github.com/clouddrove/syncerd/releases) for your OS/arch |
| **Docker** | `docker run ghcr.io/clouddrove/syncerd:latest syncerd sync --once` (mount config + auth as needed) |

The Docker image is a **multi-arch manifest** supporting `linux/amd64` and `linux/arm64` (AWS Graviton, Apple Silicon).

Requires Go 1.23+ to build from source.

---

## Usage

### Use as a GitHub Action (Marketplace)

Add SyncerD to your workflow:

```yaml
- uses: clouddrove/syncerd@v1
  with:
    config: syncerd.yaml
    once: "true"
```

Add Docker credential steps (e.g. `docker/login-action`, `aws-actions/amazon-ecr-login`) *before* SyncerD so destination registries are authenticated.

### Use as an Azure DevOps Marketplace extension

Install and run SyncerD from Azure Pipelines:

```yaml
- task: SyncerD@1
  inputs:
    version: latest
    command: sync --config syncerd.yaml --once=true
```

Add registry authentication steps before this task. SyncerD uses the default Docker keychain, so `docker login`, Azure CLI, and other registry login helpers work as they do on the command line.

See [azure-devops-extension/examples/azure-devops](azure-devops-extension/examples/azure-devops) for a working ACR smoke-test pipeline, variable group setup, and checked-in config example. Extension source and publishing notes are in [azure-devops-extension/README.md](azure-devops-extension/README.md).

### Run with Helm (Kubernetes)

Run SyncerD as a **CronJob** (stateless by default; no PVC):

```bash
helm install syncerd ./_helm/syncerd -n syncerd --create-namespace
```

Set `config.destinations` and `config.images` in `values.yaml` or via `--set`.

**Credentials:**
- Docker Hub (source): use `existingSecret` (recommended) or `secret.*` in values.
- Destination registries (ECR/ACR/GCR/GHCR): create a Docker config secret and set `dockerConfigSecret`. SyncerD automatically sets `DOCKER_CONFIG=/var/lib/syncerd/.docker` so the credentials are found even when the pod runs as a non-root user.
- To pull the SyncerD image itself from a private registry, use `imagePullSecrets` — this is separate from `dockerConfigSecret`.

**ECR note:** ECR tokens expire every 12 hours. Refresh `dockerConfigSecret` before expiry:

```bash
aws ecr get-login-password --region <region> | \
  docker login --username AWS --password-stdin <account>.dkr.ecr.<region>.amazonaws.com

kubectl create secret generic syncerd-docker-config \
  --from-file=.dockerconfigjson=$HOME/.docker/config.json \
  --type=kubernetes.io/dockerconfigjson \
  -n syncerd --dry-run=client -o yaml | kubectl apply -f -
```

See [_helm/syncerd/README.md](_helm/syncerd/README.md) for all chart options.

### Command line

```bash
syncerd sync --once                    # Run once and exit
syncerd sync                           # Run with built-in cron (from config)
syncerd sync --config /path/to/config.yaml
```

---

## Examples

| Example | Description |
|---------|-------------|
| [Azure DevOps Pipeline](azure-devops-extension/examples/azure-devops) | Run SyncerD in Azure DevOps and mirror a Docker Hub image into Azure Container Registry. |

---

## Configuration

Full example: [syncerd.yaml.example](syncerd.yaml.example).

| Section | Purpose |
|---------|---------|
| `source` | Docker Hub (username/password or token via env or config) |
| `destinations` | List of registries (ECR, ACR, GCR, GHCR); auth via Docker credential config |
| `images` | Images to sync; optional `tags`, `watch_tags` for new tag detection |
| `schedule` | Cron expression when running without `--once` |
| `state_path` | Optional state file for "already synced" tracking; leave empty for fully stateless |
| `slack` | Optional webhook; notify on new syncs and/or failures. Enabled automatically once a webhook URL is set — set `slack.enabled: false` (or `SYNCERD_SLACK_ENABLED=false`) to suppress |
| `fail_fast` | `true` = stop on first error; `false` = best-effort per image/tag |

### Environment variables

Override with `SYNCERD_` prefix:
`SYNCERD_SOURCE_USERNAME`, `SYNCERD_SOURCE_PASSWORD`, `SYNCERD_SOURCE_TOKEN`,
`SYNCERD_STATE_PATH`, `SYNCERD_SLACK_ENABLED`, `SYNCERD_SLACK_WEBHOOK_URL`,
`SYNCERD_SLACK_CHANNEL`, `SYNCERD_SLACK_NOTIFY_ON_NEW`, `SYNCERD_SLACK_NOTIFY_ON_ERROR`,
`SYNCERD_SLACK_MESSAGE_FORMAT`, `SYNCERD_FAIL_FAST`.

### Authentication

- **Docker Hub (source):** Username/password or Personal Access Token (env or config). Credentials are validated at startup.
- **Destinations (ECR/ACR/GCR/GHCR):** SyncerD uses the default Docker keychain — `docker login`, credential helpers, or GitHub Actions login steps.
- **ECR:** Tokens expire every 12 hours. Ensure credentials are refreshed before each scheduled sync.

---

## Git mirroring

`syncerd git-sync` mirrors git repositories between hosting providers, replicating branches and tags as a full mirror in either direction. GitHub and GitLab are supported today; Bitbucket, Azure DevOps, and AWS CodeCommit are planned.

**Minimal config:**

```yaml
git:
  providers:
    - name: gh
      type: github
      owner: your-github-org
    - name: gl
      type: gitlab
      owner: your-gitlab-group

  mirrors:
    - name: gh-to-gl
      source: gh
      destination: gl
      create_missing: true

  work_dir: /var/lib/syncerd/git
  state_path: /var/lib/syncerd/git/git-state.json
```

```bash
export SYNCERD_GIT_GH_TOKEN=your-github-token
export SYNCERD_GIT_GL_TOKEN=your-gitlab-token
./syncerd git-sync --once
```

**Tokens:** each provider's token is read from an environment variable named after the provider: the provider name, upper cased, with non alphanumeric characters replaced by underscore. A provider named `gh` reads `SYNCERD_GIT_GH_TOKEN`; a provider named `gh-mirrors` reads `SYNCERD_GIT_GH_MIRRORS_TOKEN`.

**Push modes** (`push_mode` on a mirror, default `mirror`):

| Mode | Behavior |
|------|----------|
| `mirror` | Replicates the source exactly, deleting destination branches and tags absent at source |
| `additive` | Pushes new and updated refs, never deletes |
| `fast-forward` | Refuses any non fast-forward update and reports it as a failure |

**Adopt guard:** a mirror refuses to push to a destination that already has content and no prior mirror state, so a misconfigured mirror cannot silently overwrite existing work; set `adopt: true` on the mirror to opt in once you've confirmed the destination is right.

Run with `--dry-run` to print the ref changes each mirror would make, per repository, without creating, pushing, or deleting anything.

For the full local test procedure, including config validation, idempotency, pruning, the adopt guard, and Slack, see [docs/git-sync-runbook.md](docs/git-sync-runbook.md).

---

## Contributing & support

- **Bugs & ideas:** [Open an issue](https://github.com/clouddrove/syncerd/issues).
- **Code:** See [CONTRIBUTING.md](CONTRIBUTING.md). PRs welcome.
- **Star the repo** if SyncerD helps you — it helps others discover the project.

---

## License

[MIT](LICENSE). Built with [go-containerregistry](https://github.com/google/go-containerregistry).
Inspired by the need to work around Docker Hub rate limits.
