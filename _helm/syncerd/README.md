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
| `cronjob.enabled` | Run the image sync CronJob. Set `false` for a deployment that only wants git mirroring | `true` |
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

**`DOCKER_CONFIG=/var/lib/syncerd/.docker` is set automatically** when `dockerConfigSecret` is configured. The secret is mounted at that path (not under `/root`) so the credentials are readable by the non-root pod user.

> **Note:** `dockerConfigSecret` is only for destination registry auth. To pull the SyncerD image itself from a private registry, use `imagePullSecrets` instead — these are separate concerns.

#### AWS ECR

Give the pod AWS credentials (IRSA, EKS Pod Identity, or a node instance role) and skip `dockerConfigSecret` entirely: SyncerD requests an ECR token itself through the standard AWS credential chain and refreshes it as it expires. The role needs `ecr:GetAuthorizationToken` plus push permissions on the destination repositories.

```yaml
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/syncerd
```

Without AWS credentials, a Docker config secret still works, but ECR tokens expire every **12 hours**, so the secret has to be refreshed before each sync window.

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

## Git mirroring

The chart can also run `syncerd git-sync` as a **second, independent CronJob**, so it can keep its own schedule apart from image sync. Disabled by default. All five provider types are supported: GitHub, GitLab, Bitbucket, Azure DevOps, and AWS CodeCommit, and any of them can be a source or a destination.

| Key | Description | Default |
|-----|-------------|---------|
| `gitSync.enabled` | Run the git mirroring CronJob | `false` |
| `gitSync.schedule` | Cron schedule for the gitsync CronJob | `0 */6 * * *` |
| `gitSync.persistence.enabled` | Use a PVC for the clone cache and mirror state; `emptyDir` when false (one-shot testing only, see below) | `true` |
| `gitSync.persistence.size` | PVC size when persistence enabled | `10Gi` |
| `gitSync.workDir` | Clone cache path inside the container | `/gitdata/cache` |
| `gitSync.statePath` | Mirror state file path inside the container | `/gitdata/cache/git-state.json` |
| `gitSync.concurrency` | Parallel repository mirrors per run | `4` |
| `gitSync.providers` | List of git providers (`name`, `type`, `owner`, ...); tokens are never set here | `[]` |
| `gitSync.mirrors` | List of mirrors (`name`, `source`, `destination`, `push_mode`, ...) | `[]` |
| `gitSync.resources` | CPU/memory limits for the gitsync job | see values.yaml |

`cronjob.enabled` and `gitSync.enabled` are independent: set `cronjob.enabled: false` and `gitSync.enabled: true` for a deployment that only mirrors git repositories.

### Why the cache and state share one volume

`gitSync.workDir` (the clone cache) and `gitSync.statePath` (the ref-fingerprint state) both live under `/gitdata`, backed by the same PVC. Losing state while keeping the cache makes every repository fail the adopt guard on the next run, because SyncerD no longer knows it populated those destinations itself. Keeping both on one volume means a lost volume loses both together, which is a clean first-run state, not a partial one.

### Git provider credentials

Provider tokens and git credentials are never written to the ConfigMap. They are read from a Secret as environment variables named `SYNCERD_GIT_<KEY>_TOKEN`, `SYNCERD_GIT_<KEY>_GIT_USERNAME`, and `SYNCERD_GIT_<KEY>_GIT_PASSWORD`, where `<KEY>` is derived from the provider's `name` in `gitSync.providers`: upper cased, with every non-alphanumeric character replaced by underscore. A provider named `gh` reads `SYNCERD_GIT_GH_TOKEN`; a provider named `gh-mirrors` reads `SYNCERD_GIT_GH_MIRRORS_TOKEN`.

| Provider | Secret key(s) it reads |
|---|---|
| GitHub | `SYNCERD_GIT_<KEY>_TOKEN` (PAT) |
| GitLab | `SYNCERD_GIT_<KEY>_TOKEN` (group access token) |
| Bitbucket | `SYNCERD_GIT_<KEY>_TOKEN` (API token; the username is the `email` set in `gitSync.providers`, not a secret) |
| Azure DevOps | `SYNCERD_GIT_<KEY>_TOKEN` (org-scoped PAT, or an operator supplied Entra ID access token when `auth: entra`) |
| AWS CodeCommit | `SYNCERD_GIT_<KEY>_GIT_USERNAME` and `SYNCERD_GIT_<KEY>_GIT_PASSWORD` (static IAM HTTPS Git credentials for the git transport); the API half authenticates via IRSA, an instance role, or the standard AWS credential chain, not a secret here |

Set them with `existingSecret` (recommended) or inline via `secret.gitTokens` / `secret.gitCredentials`:

```bash
kubectl create secret generic syncerd-creds -n syncerd \
  --from-literal=SYNCERD_GIT_GH_TOKEN=ghp_your_token \
  --from-literal=SYNCERD_GIT_GL_TOKEN=glpat-your_token
```

```yaml
existingSecret: syncerd-creds
```

Or inline (not recommended for production):

```yaml
secret:
  gitTokens:
    gh: "ghp_your_token"
    gl: "glpat-your_token"
  # gitCredentials keys map to SYNCERD_GIT_<KEY>_GIT_USERNAME / _GIT_PASSWORD.
  # This is CodeCommit's intended git transport credential, not a future or
  # optional one: SyncerD does not derive SigV4 git credentials, so a
  # static IAM HTTPS Git credential pair is how a CodeCommit provider
  # authenticates git push and clone, even when the API half authenticates
  # via IRSA or an instance role.
  # gitCredentials:
  #   cc:
  #     username: "..."
  #     password: "..."
```

### Limitations

- **CodeCommit**: SyncerD does not derive SigV4 git credentials, so IRSA and instance roles cover listing and creating repositories but the git transport needs static IAM HTTPS Git credentials, set via `secret.gitCredentials` or `existingSecret`.
- **Azure DevOps Entra mode**: the operator supplies the access token; SyncerD does not acquire one from Azure AD. Set `auth: entra` on the provider and supply the token through the same `SYNCERD_GIT_<KEY>_TOKEN` key used for a PAT.
- **Bitbucket**: Cloud only. The `api_url` override changes the host but the request paths are Cloud shaped, so Bitbucket Data Center is not supported. Bitbucket also has no archived concept, so `skip_archived` has no effect for a Bitbucket source.

### Minimal example

```yaml
cronjob:
  enabled: false

gitSync:
  enabled: true
  providers:
    - name: gh
      type: github
      owner: my-org
    - name: gl
      type: gitlab
      owner: my-group
  mirrors:
    - name: gh-to-gl
      source: gh
      destination: gl
      create_missing: true

existingSecret: syncerd-creds
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
