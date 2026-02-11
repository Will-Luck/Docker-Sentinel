# Docker-Sentinel

[![CI](https://github.com/Will-Luck/Docker-Sentinel/actions/workflows/release.yml/badge.svg)](https://github.com/Will-Luck/Docker-Sentinel/actions/workflows/release.yml)
[![GitHub Release](https://img.shields.io/github/v/release/Will-Luck/Docker-Sentinel)](https://github.com/Will-Luck/Docker-Sentinel/releases/latest)
[![Docker Pulls](https://img.shields.io/docker/pulls/willluck/docker-sentinel)](https://hub.docker.com/r/willluck/docker-sentinel)
[![Downloads](https://img.shields.io/github/downloads/Will-Luck/Docker-Sentinel/total)](https://github.com/Will-Luck/Docker-Sentinel/releases)
[![Last Commit](https://img.shields.io/github/last-commit/Will-Luck/Docker-Sentinel)](https://github.com/Will-Luck/Docker-Sentinel/commits)
[![GHCR](https://img.shields.io/badge/ghcr.io-docker--sentinel-blue?logo=github)](https://github.com/Will-Luck/Docker-Sentinel/pkgs/container/docker-sentinel)

A container update orchestrator with a web dashboard, written in Go. Replaces Watchtower with per-container update policies, pre-update snapshots, post-update health validation, automatic rollback, and real-time notifications.

## Features

- **Per-container update policies** via Docker labels (`auto`, `manual`, `pinned`)
- **Pre-update snapshots** — full `docker inspect` captured before every update
- **Automatic rollback** — if a container fails health checks after updating, Sentinel rolls back to the snapshot
- **Web dashboard** — real-time SSE updates, stack grouping, accordion detail panels, bulk policy management, update history and queue
- **Registry intelligence** — digest comparison for mutable tags (`:latest`), semver tag discovery for versioned images
- **Notifications** — Gotify, Slack, Discord, Ntfy, Telegram, Pushover, generic webhooks — with per-channel event filtering
- **Runtime settings** — adjust poll interval, grace period, default policy, pause scanning, and container filters from the web UI without restart
- **Container controls** — start, stop, restart containers directly from the dashboard
- **Live updates** — SSE-powered inline row updates with spinners, no full page reloads
- **Self-update** — Sentinel can check and update its own image (with opt-out via `sentinel.self` label)
- **[Docker-Guardian](https://github.com/Will-Luck/Docker-Guardian) integration** — maintenance labels prevent restart conflicts during updates
- **REST API** — all dashboard functionality exposed as JSON endpoints

## Quick Start

```bash
# From Docker Hub
docker run -d \
  --name docker-sentinel \
  --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v sentinel-data:/data \
  -p 8080:8080 \
  -e SENTINEL_POLL_INTERVAL=6h \
  willluck/docker-sentinel:latest

# Or from GitHub Container Registry
# ghcr.io/will-luck/docker-sentinel:latest
```

Then open `http://localhost:8080` in your browser.

## Configuration

All configuration is via environment variables. Settings marked with * can also be changed at runtime from the web UI.

| Variable | Default | Description |
|----------|---------|-------------|
| `SENTINEL_POLL_INTERVAL` | `6h` | How often to scan for updates * |
| `SENTINEL_GRACE_PERIOD` | `30s` | Wait time after update before health check * |
| `SENTINEL_DEFAULT_POLICY` | `manual` | Policy for unlabelled containers * |
| `SENTINEL_DB_PATH` | `/data/sentinel.db` | BoltDB database path |
| `SENTINEL_LOG_JSON` | `true` | JSON structured logging |
| `SENTINEL_WEB_ENABLED` | `true` | Enable web dashboard |
| `SENTINEL_WEB_PORT` | `8080` | Web dashboard port |
| `SENTINEL_DOCKER_SOCK` | `/var/run/docker.sock` | Docker socket path |
| `SENTINEL_DOCKER_CONFIG` | `/root/.docker/config.json` | Docker config for private registry auth |
| `SENTINEL_GOTIFY_URL` | | Gotify server URL (legacy — prefer web UI) |
| `SENTINEL_GOTIFY_TOKEN` | | Gotify application token (legacy — prefer web UI) |
| `SENTINEL_WEBHOOK_URL` | | Webhook URL for JSON POST (legacy — prefer web UI) |
| `SENTINEL_WEBHOOK_HEADERS` | | Custom webhook headers, comma-separated `Key:Value` pairs |

## Container Labels

| Label | Values | Default | Description |
|-------|--------|---------|-------------|
| `sentinel.policy` | `auto`, `manual`, `pinned` | `manual` | Update policy |
| `sentinel.maintenance` | `true` | (absent) | Set during updates, read by Docker-Guardian |
| `sentinel.self` | `true` | (absent) | Prevents Sentinel from updating itself |

- **auto** — updates applied automatically when a new image is detected
- **manual** — updates queued for approval in the dashboard
- **pinned** — container is never checked or updated

## Notification Channels

Notification channels are configured from the **Settings** page in the web UI. Each channel supports per-event filtering so you can, for example, send only failures to Slack while sending everything to a webhook.

| Provider | Required Settings |
|----------|-------------------|
| **Gotify** | Server URL, application token |
| **Slack** | Incoming webhook URL |
| **Discord** | Webhook URL |
| **Ntfy** | Server URL, topic, priority |
| **Telegram** | Bot token, chat ID |
| **Pushover** | App token, user key |
| **Webhook** | URL, optional custom headers |

Filterable event types: `update_available`, `update_started`, `update_succeeded`, `update_failed`, `rollback_succeeded`, `rollback_failed`, `container_state`.

Legacy environment variables (`SENTINEL_GOTIFY_*`, `SENTINEL_WEBHOOK_*`) are still supported but the web UI is recommended for new setups.

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

**Containers**

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/containers` | List all containers with policy and status |
| `GET` | `/api/containers/{name}` | Container detail (history, snapshots) |
| `GET` | `/api/containers/{name}/versions` | Available image versions from registry |
| `GET` | `/api/containers/{name}/row` | HTML partial for live row update |
| `POST` | `/api/containers/{name}/policy` | Set policy override |
| `DELETE` | `/api/containers/{name}/policy` | Remove policy override (fall back to label) |
| `POST` | `/api/containers/{name}/rollback` | Rollback to last snapshot |
| `POST` | `/api/containers/{name}/restart` | Restart container |
| `POST` | `/api/containers/{name}/stop` | Stop container |
| `POST` | `/api/containers/{name}/start` | Start container |
| `POST` | `/api/check/{name}` | Manual registry check |
| `POST` | `/api/update/{name}` | Trigger immediate update |
| `POST` | `/api/approve/{name}` | Approve queued update |
| `POST` | `/api/reject/{name}` | Reject queued update |
| `POST` | `/api/bulk/policy` | Bulk policy change |

**Queue, History & Logs**

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/queue` | List pending updates |
| `GET` | `/api/history` | Update history |
| `GET` | `/api/logs` | Activity log entries |
| `GET` | `/api/events` | SSE event stream |

**Settings**

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/settings` | Current configuration |
| `POST` | `/api/settings/poll-interval` | Update poll interval at runtime |
| `POST` | `/api/settings/default-policy` | Set default policy at runtime |
| `POST` | `/api/settings/grace-period` | Set grace period at runtime |
| `POST` | `/api/settings/pause` | Pause or unpause the scheduler |
| `POST` | `/api/settings/filters` | Set container name filter patterns |

**Notifications**

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/settings/notifications` | Get notification channels (secrets masked) |
| `PUT` | `/api/settings/notifications` | Save notification channels |
| `POST` | `/api/settings/notifications/test` | Send a test notification |
| `GET` | `/api/settings/notifications/event-types` | List available event types |

**Self-Update**

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/api/self-update` | Trigger self-update via ephemeral helper |

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
│   ├── notify/            # Notification providers (7 channels + log)
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
