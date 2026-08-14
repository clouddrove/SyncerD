# SyncerD Azure Pipelines

Install and run the SyncerD CLI from Azure Pipelines.

SyncerD does two things: `sync` copies Docker images from Docker Hub to ECR, ACR, GCR, and GHCR so CI and production workloads can pull from your own registries; `git-sync` mirrors git repositories across GitHub, GitLab, Bitbucket, Azure DevOps, and AWS CodeCommit.

## Usage

Install SyncerD only:

```yaml
- task: SyncerD@1
  inputs:
    version: latest
```

Run an image sync:

```yaml
- task: SyncerD@1
  inputs:
    version: v0.0.11
    command: sync --config syncerd.yaml --once=true
```

Add registry authentication steps before this task. SyncerD uses the default Docker keychain, so `docker login`, cloud CLI login helpers, and existing Docker credentials work as they do on the command line.

Run a git mirror, with tokens supplied as secret pipeline variables mapped into `env:` on the task:

```yaml
- task: SyncerD@1
  inputs:
    version: v0.0.11
    command: git-sync --config syncerd.yaml --once=true --dry-run=true
  env:
    SYNCERD_GIT_GH_TOKEN: $(SYNCERD_GH_TOKEN)
    SYNCERD_GIT_GL_TOKEN: $(SYNCERD_GL_TOKEN)
```

`SYNCERD_GH_TOKEN` and `SYNCERD_GL_TOKEN` are pipeline (or variable group) variables marked **secret**; Azure Pipelines only exposes secret variables to a step through an explicit `env:` mapping like the one above, never automatically. Each provider's variable name follows `SYNCERD_GIT_<PROVIDER_NAME>_TOKEN`, upper cased, with non alphanumeric characters replaced by underscore, matching the provider's `name` in `syncerd.yaml`. Run once with `--dry-run=true` before dropping it, since the default push mode deletes destination refs absent at the source.

## Inputs

| Input | Default | Description |
|-------|---------|-------------|
| `version` | `latest` | SyncerD version to install. Use `latest` or a release tag such as `v0.0.11`. |
| `command` | `sync --config syncerd.yaml --once=true` | SyncerD command and arguments to run after installation. Leave blank to install only. Use `sync ...` for image sync or `git-sync ...` for git mirroring. |
| `workingDirectory` | `$(System.DefaultWorkingDirectory)` | Directory where the SyncerD command runs. |

## Links

- [SyncerD GitHub repository](https://github.com/clouddrove/SyncerD)
- [SyncerD releases](https://github.com/clouddrove/SyncerD/releases)
