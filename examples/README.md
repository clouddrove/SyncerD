# SyncerD Examples

This directory contains small, self-contained examples for the main SyncerD use cases.

## Available examples

### Docker sync

- `docker-sync/dockerhub-to-acr`
  - Syncs `library/nginx:alpine` from Docker Hub to Azure Container Registry.
  - Includes a SyncerD config and a GitHub Actions workflow.

- `docker-sync/dockerhub-to-ecr`
  - Syncs `library/nginx:alpine` from Docker Hub to Amazon ECR.
  - Includes a SyncerD config and a GitHub Actions workflow.

- `docker-sync/dockerhub-to-ghcr`
  - Syncs `library/nginx:alpine` from Docker Hub to GitHub Container Registry.
  - Includes a SyncerD config and a GitHub Actions workflow.

### Git sync

- `git-sync/azure-to-github`
  - Mirrors Azure Repos into GitHub.
  - Includes a SyncerD config and a GitHub Actions workflow.

- `git-sync/github-to-codecommit`
  - Mirrors GitHub repositories into AWS CodeCommit.
  - Includes a SyncerD config and a GitHub Actions workflow.

- `git-sync/github-to-gitlab`
  - Mirrors GitHub repositories into GitLab.
  - Includes a SyncerD config and a GitHub Actions workflow.

## Notes

- Each example keeps its config and workflow together in the same directory.
- Replace placeholder values such as registry names, AWS IDs, GitHub owners, GitLab groups, Azure repos, and CodeCommit settings before running.
- The workflow files are written as a starting point for GitHub Actions and may require minor adjustments for your secrets and environment variables.
