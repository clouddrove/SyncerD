# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Initial release of SyncerD
- Support for syncing images from Docker Hub to multiple registries:
  - AWS ECR
  - Azure ACR
  - Google GCR
  - GitHub Container Registry (GHCR)
- Automatic tag detection and monitoring
- Cron-based scheduling for periodic syncs (default: every 3 weeks)
- YAML-based configuration with environment variable support
- GitHub Actions workflow support
- CLI tool with `--once` flag for one-time syncs
- Support for watching all tags or specific tags per image
- Authentication support for all registry types

### Features
- Multi-registry destination support
- Automatic version detection
- Configurable sync schedules
- Secure authentication methods
- Comprehensive logging
