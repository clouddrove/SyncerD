# Azure DevOps Pipeline Example

This example runs SyncerD from an Azure DevOps pipeline and syncs a public Docker Hub image into Azure Container Registry (ACR).

The pipeline is useful for:

- Testing SyncerD in Azure DevOps without GitHub Actions.
- Mirroring public Docker Hub images into ACR for CI or AKS workloads.
- Running SyncerD as a scheduled or manually triggered Azure Pipeline.

## Files

| File | Purpose |
| --- | --- |
| `azure-pipelines.yml` | Azure DevOps pipeline that runs the published SyncerD container. |
| `syncerd.azure.yaml.example` | Optional checked-in SyncerD config for an ACR smoke test. |

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
2. Generates a small SyncerD config unless `configPath` is supplied.
3. Logs in to ACR with `docker login`.
4. Runs `ghcr.io/clouddrove/syncerd:latest`.
5. Mounts the Azure Pipelines Docker credential config into the SyncerD container.
6. Syncs the requested source image tag into ACR.

The default test syncs:

```text
docker.io/library/nginx:alpine
```

to:

```text
<ACR_LOGIN_SERVER>/library/nginx:alpine
```

## Use The Generated Config

By default, the pipeline generates this config at runtime:

```yaml
source:
  type: dockerhub
  registry: docker.io

destinations:
  - name: syncerd-acr-test
    type: acr
    registry: ${ACR_LOGIN_SERVER}
    region: East US

images:
  - name: library/nginx
    tags:
      - alpine
    watch_tags: false

fail_fast: false
```

Run the pipeline manually and optionally override:

| Parameter | Default | Description |
| --- | --- | --- |
| `imageName` | `library/nginx` | Source image name. |
| `imageTag` | `alpine` | Source image tag. |
| `syncerdImage` | `ghcr.io/clouddrove/syncerd:latest` | SyncerD container image to run. |

## Use A Checked-In Config

If you want full control over the SyncerD config, copy the example file:

```bash
cp examples/azure-devops/syncerd.azure.yaml.example syncerd.azure.yaml
```

Replace `<acr-login-server>` with your ACR login server, then run the pipeline with:

```text
configPath: syncerd.azure.yaml
```

When `configPath` is set, the pipeline uses that file instead of generating a temporary config.

## Azure DevOps Setup

1. Copy `azure-pipelines.yml` into the root of your repository or create a pipeline that points to this file.
2. Create the `syncer-d` variable group.
3. Add `ACR_LOGIN_SERVER`, `ACR_USERNAME`, and secret `ACR_PASSWORD`.
4. Run the pipeline manually.

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

- The pipeline does not build SyncerD; it runs the published container image.
- The Docker config from the Azure Pipelines agent is mounted into the container so SyncerD can push to ACR.
- Do not set `-w /workspace` on `docker run`; the SyncerD image entrypoint is relative and expects the image default working directory.
