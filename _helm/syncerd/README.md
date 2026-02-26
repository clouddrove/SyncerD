# SyncerD Helm Chart

Run [SyncerD](https://github.com/clouddrove/syncerd) on Kubernetes as a **CronJob**. Each run executes `syncerd sync --once`; the schedule is controlled by the CronJob (default: every 3 weeks). **Stateless by default** (no PVC): SyncerD checks each destination registry and skips tags that already exist. Set `persistence.enabled: true` to persist state across runs for faster "already synced" checks.

## Prerequisites

- Kubernetes 1.19+
- Helm 3+
- (Optional) PVC provisioner for state persistence

## Install

```bash
# From the syncerd repo root
helm install syncerd ./_helm/syncerd -n syncerd --create-namespace

# With custom values
helm install syncerd ./_helm/syncerd -n syncerd --create-namespace -f my-values.yaml
```

## Configuration

| Key | Description | Default |
|-----|-------------|---------|
| `image.repository` | SyncerD image | `ghcr.io/clouddrove/syncerd` |
| `image.tag` | Image tag | `latest` |
| `imagePullSecrets` | Secrets to pull the SyncerD image itself (e.g. from a private registry) | `[]` |
| `cronjob.schedule` | Cron schedule (K8s CronJob) | `0 0 */21 * *` (every 3 weeks) |
| `persistence.enabled` | Use PVC for state file; stateless when false | `false` |
| `persistence.size` | PVC size when persistence enabled | `1Gi` |
| `config.source` | Docker Hub source config | see values.yaml |
| `config.destinations` | List of destination registries | `[]` |
| `config.images` | Images to sync | `[]` |
| `config.state_path` | State file path (only used when `persistence.enabled: true`) | `/data/.syncerd-state.json` |
| `config.slack` | Slack notification settings | see values.yaml |
| `existingSecret` | Name of existing Secret for Docker Hub / Slack credentials | `""` |
| `secret.dockerhubUsername` / `dockerhubPassword` / `dockerhubToken` | Inline Docker Hub creds (use `existingSecret` in production) | `""` |
| `secret.slackWebhookUrl` / `slackChannel` | Inline Slack settings | `""` |
| `dockerConfigSecret` | Secret containing `.dockerconfigjson` for **destination** registry auth (ECR, ACR, GCR, GHCR) | `""` |
| `resources` | CPU/memory limits for the job | see values.yaml |

## Credentials

### Docker Hub (source)

Use `existingSecret` (recommended for production):

```bash
kubectl create secret generic syncerd-creds -n syncerd \
  --from-literal=DOCKERHUB_USERNAME=myuser \
  --from-literal=DOCKERHUB_PASSWORD=mytoken
```

```yaml
existingSecret: syncerd-creds
```

Or for Slack notifications, add to the same secret:

```bash
kubectl create secret generic syncerd-creds -n syncerd \
  --from-literal=DOCKERHUB_USERNAME=myuser \
  --from-literal=DOCKERHUB_PASSWORD=mytoken \
  --from-literal=SYNCERD_SLACK_WEBHOOK_URL=https://hooks.slack.com/...
```

### Destination registries (ECR, ACR, GCR, GHCR)

SyncerD uses the Docker credential config for destination registry auth. Create a secret from your local Docker config after logging in, then set `dockerConfigSecret`.

**`DOCKER_CONFIG` is set automatically** when `dockerConfigSecret` is configured, so credentials are found even though the pod runs as a non-root user.

> **Note:** `dockerConfigSecret` is only for destination registry auth. To pull the SyncerD image itself from a private registry, use `imagePullSecrets` instead — these are separate concerns.

#### AWS ECR

ECR tokens expire every **12 hours**. Refresh the secret before each sync window.

```bash
# Log in and create/update the secret
aws ecr get-login-password --region <region> | \
  docker login --username AWS --password-stdin <account>.dkr.ecr.<region>.amazonaws.com

kubectl create secret generic syncerd-docker-config \
  --from-file=.dockerconfigjson=$HOME/.docker/config.json \
  --type=kubernetes.io/dockerconfigjson \
  -n syncerd --dry-run=client -o yaml | kubectl apply -f -
```

```yaml
dockerConfigSecret: syncerd-docker-config
```

#### Azure ACR

```bash
docker login myregistry.azurecr.io \
  --username <client-id> --password <client-secret>

kubectl create secret generic syncerd-docker-config \
  --from-file=.dockerconfigjson=$HOME/.docker/config.json \
  --type=kubernetes.io/dockerconfigjson \
  -n syncerd --dry-run=client -o yaml | kubectl apply -f -
```

#### Google GCR / Artifact Registry

```bash
gcloud auth configure-docker

kubectl create secret generic syncerd-docker-config \
  --from-file=.dockerconfigjson=$HOME/.docker/config.json \
  --type=kubernetes.io/dockerconfigjson \
  -n syncerd --dry-run=client -o yaml | kubectl apply -f -
```

#### GitHub Container Registry (GHCR)

```bash
echo $GITHUB_TOKEN | docker login ghcr.io \
  --username <github-username> --password-stdin

kubectl create secret generic syncerd-docker-config \
  --from-file=.dockerconfigjson=$HOME/.docker/config.json \
  --type=kubernetes.io/dockerconfigjson \
  -n syncerd --dry-run=client -o yaml | kubectl apply -f -
```

#### Multiple destination registries

`docker login` accumulates credentials in `~/.docker/config.json`, so log in to all registries before creating the secret:

```bash
# Log in to all destinations
aws ecr get-login-password --region us-east-1 | \
  docker login --username AWS --password-stdin 123456789012.dkr.ecr.us-east-1.amazonaws.com
docker login myregistry.azurecr.io --username <id> --password <secret>

# Create one secret covering all of them
kubectl create secret generic syncerd-docker-config \
  --from-file=.dockerconfigjson=$HOME/.docker/config.json \
  --type=kubernetes.io/dockerconfigjson \
  -n syncerd --dry-run=client -o yaml | kubectl apply -f -
```

## Run a sync manually

```bash
kubectl create job --from=cronjob/<release-name> syncerd-manual-$(date +%s) -n syncerd
```

## Uninstall

```bash
helm uninstall syncerd -n syncerd
# Optionally delete PVC: kubectl delete pvc -l app.kubernetes.io/name=syncerd -n syncerd
```
