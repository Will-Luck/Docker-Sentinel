# Docker-Sentinel

[![CI](https://github.com/Will-Luck/Docker-Sentinel/actions/workflows/release.yml/badge.svg)](https://github.com/Will-Luck/Docker-Sentinel/actions/workflows/release.yml)
[![GitHub Release](https://img.shields.io/github/v/release/Will-Luck/Docker-Sentinel)](https://github.com/Will-Luck/Docker-Sentinel/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/Will-Luck/Docker-Sentinel/total)](https://github.com/Will-Luck/Docker-Sentinel/releases)
[![GHCR](https://img.shields.io/badge/ghcr.io-docker--sentinel-blue?logo=github)](https://github.com/Will-Luck/Docker-Sentinel/pkgs/container/docker-sentinel)

A container update orchestrator with a web dashboard, written in Go. Replaces Watchtower with per-container update policies, pre-update snapshots, post-update health validation, automatic rollback, and real-time notifications.

## Features

- **Per-container update policies** via Docker labels (`auto`, `manual`, `pinned`)
- **Pre-update snapshots** — full `docker inspect` captured before every update
- **Automatic rollback** — if a container fails health checks after updating, Sentinel rolls back to the snapshot
- **Web dashboard** — real-time SSE updates, stack grouping, accordion detail panels, bulk policy management, update history and queue
- **Registry intelligence** — digest comparison for mutable tags (`:latest`), semver tag discovery for versioned images
- **Notifications** — Gotify, generic webhooks, structured log output
- **[Docker-Guardian](https://github.com/Will-Luck/Docker-Guardian) integration** — maintenance labels prevent restart conflicts during updates
- **REST API** — all dashboard functionality exposed as JSON endpoints

## Quick Start

```bash
docker run -d \
  --name docker-sentinel \
  --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v sentinel-data:/data \
  -p 8080:8080 \
  -e SENTINEL_POLL_INTERVAL=6h \
  ghcr.io/will-luck/docker-sentinel:latest
```

Then open `http://localhost:8080` in your browser.

## Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `SENTINEL_POLL_INTERVAL` | `6h` | How often to scan for updates |
| `SENTINEL_GRACE_PERIOD` | `30s` | Wait time after update before health check |
| `SENTINEL_DEFAULT_POLICY` | `manual` | Policy for unlabelled containers |
| `SENTINEL_DB_PATH` | `/data/sentinel.db` | BoltDB database path |
| `SENTINEL_LOG_LEVEL` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `SENTINEL_LOG_JSON` | `true` | JSON structured logging |
| `SENTINEL_WEB_ENABLED` | `true` | Enable web dashboard |
| `SENTINEL_WEB_ADDR` | `:8080` | Web server listen address |
| `SENTINEL_GOTIFY_URL` | | Gotify server URL for push notifications |
| `SENTINEL_GOTIFY_TOKEN` | | Gotify application token |
| `SENTINEL_WEBHOOK_URL` | | Webhook URL — receives JSON POST (Discord, Slack, N8N, etc.) |

## Container Labels

| Label | Values | Default | Description |
|-------|--------|---------|-------------|
| `sentinel.policy` | `auto`, `manual`, `pinned` | `manual` | Update policy |
| `sentinel.maintenance` | `true` | (absent) | Set during updates, read by Docker-Guardian |
| `sentinel.self` | `true` | (absent) | Prevents Sentinel from updating itself |

- **auto** — updates applied automatically when a new image is detected
- **manual** — updates queued for approval in the dashboard
- **pinned** — container is never checked or updated

## Building from Source

```bash
make build        # Build binary to bin/sentinel
make test         # Run tests with race detector
make lint         # Run golangci-lint
make docker       # Build Docker image
```

Requires Go 1.24+, Docker, and golangci-lint.

<details>
<summary><strong>Update Lifecycle</strong></summary>

1. **Scan** — enumerate running containers, check policies
2. **Check** — query registry for new digests or semver tags
3. **Queue** — auto-policy containers proceed; manual-policy containers wait for approval
4. **Snapshot** — capture full container config via `docker inspect`
5. **Maintenance** — set `sentinel.maintenance=true` label (Docker-Guardian integration)
6. **Update** — pull new image, stop, remove, recreate with identical config, start
7. **Validate** — wait for grace period, check container is still running
8. **Finalise** — on success: clear maintenance label, log, notify. On failure: rollback from snapshot, notify

</details>

<details>
<summary><strong>REST API</strong></summary>

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/containers` | List all containers with policy and status |
| `GET` | `/api/containers/{name}` | Container detail (history, snapshots) |
| `GET` | `/api/containers/{name}/versions` | Available image versions from registry |
| `POST` | `/api/containers/{name}/policy` | Change container policy |
| `POST` | `/api/containers/{name}/rollback` | Rollback to last snapshot |
| `POST` | `/api/update/{name}` | Trigger immediate update |
| `POST` | `/api/approve/{name}` | Approve queued update |
| `POST` | `/api/reject/{name}` | Reject queued update |
| `POST` | `/api/bulk/policy` | Bulk policy change |
| `GET` | `/api/queue` | List pending updates |
| `GET` | `/api/history` | Update history |
| `GET` | `/api/settings` | Current configuration |
| `GET` | `/api/events` | SSE event stream |

</details>

<details>
<summary><strong>Architecture</strong></summary>

```
Docker-Sentinel/
├── cmd/sentinel/          # Entry point
├── internal/
│   ├── clock/             # Time abstraction (testable)
│   ├── config/            # Environment variable configuration
│   ├── docker/            # Docker API client, labels, snapshots
│   ├── engine/            # Scheduler, updater, rollback, queue, policy
│   ├── events/            # Event bus (SSE fan-out)
│   ├── guardian/          # Docker-Guardian maintenance label integration
│   ├── logging/           # Structured slog logger
│   ├── notify/            # Notification providers (Gotify, webhook, log)
│   ├── registry/          # Registry digest checker, semver tag discovery
│   ├── store/             # BoltDB persistence
│   └── web/               # HTTP server, REST API, embedded dashboard
│       └── static/        # HTML templates, CSS, JavaScript
├── Dockerfile             # Multi-stage Alpine build
├── Makefile
└── LICENSE                # Apache 2.0
```

</details>

## License

Apache License 2.0 — see [LICENSE](LICENSE) for details.
