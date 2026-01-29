# SyncerD Helm Chart

Run [SyncerD](https://github.com/clouddrove/syncerd) on Kubernetes as a **CronJob**. Each run executes `syncerd sync --once`; the schedule is controlled by the CronJob (default: every 3 weeks). **Stateless by default** (no PVC): SyncerD checks each destination registry and skips tags that already exist. Set `persistence.enabled: true` to persist state across runs for faster "already synced" checks.

## Prerequisites

- Kubernetes 1.19+
- Helm 3+
- (Optional) PVC provisioner for state persistence

## Install

```bash
# From the syncerd repo root
helm install syncerd ./helm/syncerd -n syncerd --create-namespace

# With custom values
helm install syncerd ./helm/syncerd -n syncerd --create-namespace -f my-values.yaml
```

## Configuration

| Key | Description | Default |
|-----|-------------|---------|
| `image.repository` | SyncerD image | `ghcr.io/clouddrove/syncerd` |
| `image.tag` | Image tag | `latest` |
| `cronjob.schedule` | Cron schedule (K8s CronJob) | `0 0 */21 * *` (every 3 weeks) |
| `persistence.enabled` | Use PVC for state file (stateless if false) | `false` |
| `persistence.size` | PVC size when persistence enabled | `1Gi` |
| `config.source` | Docker Hub source config | see values.yaml |
| `config.destinations` | List of destination registries | `[]` |
| `config.images` | Images to sync | `[]` |
| `config.state_path` | Path to state file in container | `/data/.syncerd-state.json` |
| `config.slack` | Slack notification settings | see values.yaml |
| `existingSecret` | Name of existing Secret for credentials | `""` |
| `secret.dockerhubUsername` / `dockerhubPassword` / `dockerhubToken` | Inline Docker Hub creds | `""` |
| `secret.slackWebhookUrl` | Inline Slack webhook | `""` |
| `dockerConfigSecret` | Name of Secret containing `.dockerconfigjson` for destination registries | `""` |
| `resources` | CPU/memory limits for the job | see values.yaml |

## Credentials

- **Docker Hub (source):** Set `existingSecret` to a Secret with `DOCKERHUB_USERNAME` and `DOCKERHUB_PASSWORD`, or `DOCKERHUB_TOKEN`. Or use `secret.dockerhubUsername` / `secret.dockerhubPassword` / `secret.dockerhubToken`.
- **Slack:** Optional. In existing Secret use `SYNCERD_SLACK_WEBHOOK_URL` and `SYNCERD_SLACK_CHANNEL`, or set `secret.slackWebhookUrl` / `secret.slackChannel`.
- **Destination registries (ECR, ACR, GCR, GHCR):** Create a Docker config secret (e.g. from `docker login`) and set `dockerConfigSecret` so the job can push to those registries.

Example Secret for Docker Hub + Slack:

```bash
kubectl create secret generic syncerd-creds -n syncerd \
  --from-literal=DOCKERHUB_USERNAME=myuser \
  --from-literal=DOCKERHUB_PASSWORD=mytoken \
  --from-literal=SYNCERD_SLACK_WEBHOOK_URL=https://hooks.slack.com/...
```

Then set `existingSecret: syncerd-creds` in values.

## Run a sync manually

```bash
kubectl create job --from=cronjob/<release-name> syncerd-manual-$(date +%s) -n syncerd
```

## Uninstall

```bash
helm uninstall syncerd -n syncerd
# Optionally delete PVC: kubectl delete pvc -l app.kubernetes.io/name=syncerd -n syncerd
```
