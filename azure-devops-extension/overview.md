# SyncerD Azure Pipelines

Install and run the SyncerD CLI from Azure Pipelines.

SyncerD is a lightweight Docker registry sync engine. It copies images from Docker Hub to ECR, ACR, GCR, and GHCR so CI and production workloads can pull from your own registries.

## Usage

Install SyncerD only:

```yaml
- task: SyncerD@1
  inputs:
    version: latest
```

Run a SyncerD command:

```yaml
- task: SyncerD@1
  inputs:
    version: v0.0.11
    command: sync --config syncerd.yaml --once=true
```

Add registry authentication steps before this task. SyncerD uses the default Docker keychain, so `docker login`, cloud CLI login helpers, and existing Docker credentials work as they do on the command line.

## Inputs

| Input | Default | Description |
|-------|---------|-------------|
| `version` | `latest` | SyncerD version to install. Use `latest` or a release tag such as `v0.0.11`. |
| `command` | `sync --config syncerd.yaml --once=true` | SyncerD command and arguments to run after installation. Leave blank to install only. |
| `workingDirectory` | `$(System.DefaultWorkingDirectory)` | Directory where the SyncerD command runs. |

## Links

- [SyncerD GitHub repository](https://github.com/clouddrove/SyncerD)
- [SyncerD releases](https://github.com/clouddrove/SyncerD/releases)
