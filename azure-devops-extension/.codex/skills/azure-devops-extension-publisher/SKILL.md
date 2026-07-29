---
name: azure-devops-extension-publisher
description: Publish the SyncerD Azure DevOps Marketplace extension. Use when updating, validating, packaging, or publishing the Azure Pipelines task under azure-devops-extension.
---

# SyncerD Azure DevOps Extension Publisher

Use this skill when publishing or updating the SyncerD Azure DevOps Marketplace extension.

## Inputs

- Marketplace PAT with permission to manage the `clouddrove` publisher.
- Optional extension version in `X.Y.Z` format.

## Workflow

1. From the repository root, change to `azure-devops-extension`.
2. Run the publish helper:

```bash
AZURE_DEVOPS_MARKETPLACE_PAT="<PAT>" \
  .codex/skills/azure-devops-extension-publisher/scripts/publish-extension.sh --version 1.0.1
```

If `--version` is omitted, the script bumps the patch version from `vss-extension.json`.

The script:

- Updates `vss-extension.json`, `SyncerD/task.json`, and `SyncerD/package.json`.
- Refreshes `SyncerD/package-lock.json`.
- Validates JSON and JavaScript syntax.
- Runs `npm audit`.
- Creates a VSIX package with `tfx extension create`.
- Publishes with `tfx extension publish --no-wait-validation`.

## Validation Status

After publish, check Marketplace validation:

```bash
tfx extension isvalid \
  --publisher clouddrove \
  --extension-id syncerd-azure-pipelines \
  --version <VERSION> \
  --service-url https://marketplace.visualstudio.com/ \
  --token "<PAT>"
```
