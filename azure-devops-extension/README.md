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

```yaml
- task: SyncerD@1
  inputs:
    version: latest
    command: sync --config syncerd.yaml --once=true
```
