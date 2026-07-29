# Azure DevOps Pipeline Example

This example installs SyncerD with the Azure DevOps Marketplace task, then syncs a public Docker Hub image into Azure Container Registry (ACR).

The pipeline is useful for:

- Testing SyncerD in Azure DevOps without GitHub Actions.
- Mirroring public Docker Hub images into ACR for CI or AKS workloads.
- Running SyncerD as a scheduled or manually triggered Azure Pipeline.

## Files

| File | Purpose |
| --- | --- |
| `azure-pipelines.yml` | Azure DevOps pipeline that installs SyncerD with `SyncerD@1` and runs a smoke test. |
| `syncerd.azure.yaml.example` | Checked-in SyncerD config for an ACR smoke test. |

## Required Variables

Create an Azure DevOps variable group named `syncer-d` with these variables:

| Variable | Secret | Description |
| --- | --- | --- |
| `ACR_LOGIN_SERVER` | No | ACR login server, for example `myregistry.azurecr.io`. |
| `ACR_USERNAME` | No | ACR admin username or service principal client ID. |
| `ACR_PASSWORD` | Yes | ACR admin password or service principal client secret. |

Docker Hub credentials are not required for this example because it pulls the public `library/nginx:alpine` image. Add `SYNCERD_SOURCE_USERNAME`, `SYNCERD_SOURCE_PASSWORD`, or `SYNCERD_SOURCE_TOKEN` if your source registry requires authentication.

## How It Works

The pipeline:

1. Checks out the repository.
2. Installs SyncerD with the Azure DevOps Marketplace task.
3. Logs in to ACR with `docker login`.
4. Renders `syncerd.azure.yaml.example` with the ACR login server.
5. Runs `syncerd --help` to verify the CLI is on `PATH`.
6. Runs `syncerd sync --config ... --once=true`.

The default test syncs:

```text
docker.io/library/nginx:alpine
```

to:

```text
<ACR_LOGIN_SERVER>/library/nginx:alpine
```

## Config

The checked-in example config syncs:

```yaml
source:
  type: dockerhub
  registry: docker.io

destinations:
  - name: syncerd-acr-test
    type: acr
    registry: <acr-login-server>
    region: East US

images:
  - name: library/nginx
    tags:
      - alpine
    watch_tags: false

fail_fast: true
```

Run the pipeline manually and optionally override:

| Parameter | Default | Description |
| --- | --- | --- |
| `configPath` | `azure-devops-extension/examples/azure-devops/syncerd.azure.yaml.example` | SyncerD config path from the repository. |
| `syncerdVersion` | `v0.0.11` | SyncerD release tag installed by `SyncerD@1`. |

## Azure DevOps Setup

1. Install the `clouddrove.syncerd-azure-pipelines` extension in your Azure DevOps organization.
2. Copy `azure-pipelines.yml` into the root of your repository or create a pipeline that points to this file.
3. Create the `syncer-d` variable group.
4. Add `ACR_LOGIN_SERVER`, `ACR_USERNAME`, and secret `ACR_PASSWORD`.
5. Run the pipeline manually.

For scheduled syncs, add a schedule to the top of the pipeline:

```yaml
schedules:
  - cron: "0 0 */21 * *"
    displayName: Every 21 days
    branches:
      include:
        - main
    always: true
```

## Notes

- The pipeline does not build SyncerD; it installs a released binary with `SyncerD@1`.
- `command: ""` on the task installs SyncerD and leaves it available on `PATH` for later script steps.
- The ACR login server is injected into the checked-in config at runtime so secrets and environment-specific values stay out of source.
