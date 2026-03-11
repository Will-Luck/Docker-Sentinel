# Docker-Sentinel

[![CI](https://github.com/Will-Luck/Docker-Sentinel/actions/workflows/ci.yml/badge.svg)](https://github.com/Will-Luck/Docker-Sentinel/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Will-Luck/Docker-Sentinel)](https://github.com/Will-Luck/Docker-Sentinel/releases/latest)
[![License](https://img.shields.io/github/license/Will-Luck/Docker-Sentinel)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/Will-Luck/Docker-Sentinel)](go.mod)
[![Go Report Card](https://goreportcard.com/badge/github.com/Will-Luck/Docker-Sentinel)](https://goreportcard.com/report/github.com/Will-Luck/Docker-Sentinel)
[![GHCR](https://img.shields.io/badge/ghcr.io-docker--sentinel-blue)](https://github.com/Will-Luck/Docker-Sentinel/pkgs/container/docker-sentinel)
[![GitHub Downloads](https://img.shields.io/github/downloads/Will-Luck/Docker-Sentinel/total)](https://github.com/Will-Luck/Docker-Sentinel/releases)
[![GHCR Pulls](https://img.shields.io/endpoint?url=https%3A%2F%2Fpkgbadge.pphserv.uk%2Fwill-luck%2Fdocker-sentinel%2Fpulls.json)](https://github.com/Will-Luck/Docker-Sentinel/pkgs/container/docker-sentinel)
[![Docker Pulls](https://img.shields.io/docker/pulls/willluck/docker-sentinel)](https://hub.docker.com/r/willluck/docker-sentinel)

A container update orchestrator with a web dashboard, written in Go. Replaces Watchtower with per-container update policies, pre-update snapshots, automatic rollback, and real-time notifications.

![Dashboard](https://raw.githubusercontent.com/wiki/Will-Luck/Docker-Sentinel/images/dashboard.png)

## Features

- **Per-container update policies** via Docker labels: `auto`, `manual`, or `pinned`
- **Pre-update snapshots** with automatic rollback if a container fails health checks after updating
- **Registry checks** with digest comparison for mutable tags and semver tag discovery with constraint pinning
- **Web dashboard** with SSE live updates, stack grouping, container controls, and mobile-responsive layout
- **Cluster mode** for monitoring and updating containers across multiple Docker hosts from a single dashboard
- **11 notification providers** including Gotify, Slack, Discord, Ntfy, Telegram, Pushover, Email, MQTT, Apprise, and webhooks
- **Authentication** with password, WebAuthn/passkeys, OIDC/SSO, and TOTP/2FA support
- **Maintenance windows** with time-range expressions and per-container cron schedules
- **Lifecycle hooks** with Docker-Guardian integration for coordinated maintenance labels
- **Prometheus metrics** endpoint with an official Grafana dashboard template
- **Update queue** for reviewing, approving, or rejecting pending updates with inline release notes
- **Configuration export/import** for full settings backup and restore via the web UI

## Quick Start

```bash
docker run -d \
  --name docker-sentinel \
  --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v sentinel-data:/data \
  -p 8080:8080 \
  -e SENTINEL_POLL_INTERVAL=6h \
  willluck/docker-sentinel:latest

# Or from GitHub Container Registry:
# ghcr.io/will-luck/docker-sentinel:latest
```

Open `http://localhost:8080` in your browser. On first visit you will be guided through the setup wizard to create an admin account.

## Container Labels

Set per-container update behaviour with Docker labels like `sentinel.policy`, `sentinel.semver-constraint`, `sentinel.cron`, and others. See the [Docker Labels](https://github.com/Will-Luck/Docker-Sentinel/wiki/Docker-Labels) wiki page for the full reference.

<details>
<summary><strong>Update Lifecycle</strong></summary>

1. **Scan** containers and check policies
2. **Check** registries for new digests or semver tags
3. **Queue** updates (auto-policy proceeds immediately, manual-policy waits for approval)
4. **Snapshot** the full container config, then pull the new image before stopping anything
5. **Update** the container: stop, remove, recreate with identical config, start
6. **Validate** after the grace period, and rollback from the snapshot if the container is unhealthy

</details>

## Screenshots

| | |
|---|---|
| ![Dashboard](https://raw.githubusercontent.com/wiki/Will-Luck/Docker-Sentinel/images/dashboard.png) | ![Manage Mode](https://raw.githubusercontent.com/wiki/Will-Luck/Docker-Sentinel/images/dashboard-manage.png) |
| ![Container Detail](https://raw.githubusercontent.com/wiki/Will-Luck/Docker-Sentinel/images/container-detail.png) | ![Queue](https://raw.githubusercontent.com/wiki/Will-Luck/Docker-Sentinel/images/queue.png) |
| ![Cluster](https://raw.githubusercontent.com/wiki/Will-Luck/Docker-Sentinel/images/cluster.png) | ![Connectors](https://raw.githubusercontent.com/wiki/Will-Luck/Docker-Sentinel/images/connectors.png) |
| ![Images](https://raw.githubusercontent.com/wiki/Will-Luck/Docker-Sentinel/images/images.png) | ![Settings](https://raw.githubusercontent.com/wiki/Will-Luck/Docker-Sentinel/images/settings-scanning.png) |

## Documentation

Full documentation is available in the **[Wiki](https://github.com/Will-Luck/Docker-Sentinel/wiki)**, covering:

- [Installation Guide](https://github.com/Will-Luck/Docker-Sentinel/wiki/Installation)
- [Configuration Reference](https://github.com/Will-Luck/Docker-Sentinel/wiki/Configuration-Reference)
- [Docker Labels](https://github.com/Will-Luck/Docker-Sentinel/wiki/Docker-Labels)
- [Web UI Guide](https://github.com/Will-Luck/Docker-Sentinel/wiki/Web-UI-Guide)
- [REST API Reference](https://github.com/Will-Luck/Docker-Sentinel/wiki/REST-API-Reference)
- [Authentication & Security](https://github.com/Will-Luck/Docker-Sentinel/wiki/Authentication-and-Security)
- [Notifications](https://github.com/Will-Luck/Docker-Sentinel/wiki/Notifications)
- [Cluster Mode](https://github.com/Will-Luck/Docker-Sentinel/wiki/Cluster-Mode)
- [Lifecycle Hooks](https://github.com/Will-Luck/Docker-Sentinel/wiki/Lifecycle-Hooks)
- [Troubleshooting](https://github.com/Will-Luck/Docker-Sentinel/wiki/Troubleshooting)

## Building from Source

```bash
make build      # Build binary to bin/sentinel
make frontend   # Build JS/CSS bundles (esbuild)
make docker     # Build Docker image
```

Requires Go 1.24+, Node.js, and Docker.

## Licence

Apache Licence 2.0. See [LICENSE](LICENSE) for details.
