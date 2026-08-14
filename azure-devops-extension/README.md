# SyncerD Azure DevOps Extension

This directory contains the Azure DevOps Marketplace extension for SyncerD.

## Local Validation

```bash
cd azure-devops-extension
npm --prefix SyncerD ci --ignore-scripts
jq empty vss-extension.json SyncerD/task.json SyncerD/package.json SyncerD/package-lock.json
node --check SyncerD/index.js
npm --prefix SyncerD audit
tfx extension create --manifest-globs vss-extension.json
```

## Publish

Use the bundled Codex skill script:

```bash
cd azure-devops-extension
AZURE_DEVOPS_MARKETPLACE_PAT="<PAT>" \
  .codex/skills/azure-devops-extension-publisher/scripts/publish-extension.sh --version 1.0.0
```

The script updates extension versions, validates the package, publishes with `tfx`, and exits without waiting for Marketplace validation.

## Pipeline Usage

Image sync:

```yaml
- task: SyncerD@1
  inputs:
    version: latest
    command: sync --config syncerd.yaml --once=true
```

Git mirroring, with provider tokens supplied as secret pipeline variables mapped into `env:` on the task (Azure Pipelines never exposes a secret variable to a step without this explicit mapping):

```yaml
- task: SyncerD@1
  inputs:
    version: latest
    command: git-sync --config syncerd.yaml --once=true --dry-run=true
  env:
    SYNCERD_GIT_GH_TOKEN: $(SYNCERD_GH_TOKEN)
    SYNCERD_GIT_GL_TOKEN: $(SYNCERD_GL_TOKEN)
```

`command` is passed straight through to the `syncerd` binary, so any flag it accepts works here, including `--dry-run` (git-sync only), `--report`, `--metrics-file`, and `--log-format`. Run once with `--dry-run=true` before trusting a new mirror config: the default push mode deletes destination refs absent at the source.
