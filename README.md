# Docker-Sentinel

[![CI](https://github.com/Will-Luck/Docker-Sentinel/actions/workflows/release.yml/badge.svg)](https://github.com/Will-Luck/Docker-Sentinel/actions/workflows/release.yml)
[![GitHub Release](https://img.shields.io/github/v/release/Will-Luck/Docker-Sentinel)](https://github.com/Will-Luck/Docker-Sentinel/releases/latest)
[![Docker Pulls](https://img.shields.io/docker/pulls/willluck/docker-sentinel)](https://hub.docker.com/r/willluck/docker-sentinel)
[![Downloads](https://img.shields.io/github/downloads/Will-Luck/Docker-Sentinel/total)](https://github.com/Will-Luck/Docker-Sentinel/releases)
[![Last Commit](https://img.shields.io/github/last-commit/Will-Luck/Docker-Sentinel)](https://github.com/Will-Luck/Docker-Sentinel/commits)
[![GHCR](https://img.shields.io/badge/ghcr.io-docker--sentinel-blue?logo=github)](https://github.com/Will-Luck/Docker-Sentinel/pkgs/container/docker-sentinel)

A container update orchestrator with a web dashboard, written in Go. Replaces Watchtower with per-container update policies, pre-update snapshots, post-update health validation, automatic rollback, and real-time notifications.

## Features

### Update Engine
- **Per-container update policies** via Docker labels (`auto`, `manual`, `pinned`)
- **Pre-update snapshots** — full `docker inspect` captured before every update
- **Automatic rollback** — if a container fails health checks after updating, Sentinel rolls back to the snapshot
- **Version resolution** — resolves actual versions behind mutable tags like `:latest` so you can see what version is really running
- **Self-update** — Sentinel can check and update its own image (with opt-out via `sentinel.self` label)
- **[Docker-Guardian](https://github.com/Will-Luck/Docker-Guardian) integration** — maintenance labels prevent restart conflicts during updates

### Registry Intelligence
- **Digest comparison** for mutable tags (`:latest`) — detects upstream changes without pulling
- **Semver tag discovery** — finds newer versioned tags for images using semver conventions
- **Registry source badges** — each container shows where its image comes from (Docker Hub, GHCR, LSCR, Gitea, or custom registries)
- **GHCR alternative detection** — automatically finds GitHub Container Registry mirrors for Docker Hub images and offers one-click migration
- **Rate limit tracking** — monitors Docker Hub API rate limits and displays remaining quota on the dashboard
- **Registry credential management** — add, test, and manage credentials for private registries from the web UI

### Web Dashboard
- **Real-time updates** — SSE-powered inline row updates with live ticking scan timer, no full page reloads
- **Stack grouping** — containers sharing a Docker network are grouped into collapsible stacks with drag-and-drop reordering
- **Filter pills** — filter the container list by Running, Stopped, Updatable, or sort alphabetically / by status
- **Clickable stat cards** — dashboard header cards (Total, Running, Updates Pending, Rate Limits) filter the view when clicked
- **Accordion UI** — collapsible detail panels across all pages with persistent open/close state
- **Container controls** — start, stop, restart, rollback containers directly from the dashboard
- **Bulk actions** — select multiple containers to change policies at once
- **Update queue** — review, approve, reject, or ignore pending updates for manual-policy containers
- **History & logs** — full update history with timestamps and searchable activity logs
- **About page** — version info, uptime, runtime stats, integration status, and GitHub links
- **Dashboard footer** — persistent version number and beta notice with link to issue tracker

### Notifications
- **7 providers** — Gotify, Slack, Discord, Ntfy, Telegram, Pushover, generic webhooks — each with per-event filtering
- **Smart deduplication** — never re-notifies for the same update already seen
- **Per-container modes** — immediate + summary, every scan, summary only, or silent
- **Daily digest** — consolidated summary of all pending updates at a configurable time

### Settings & Appearance
- **Runtime configuration** — adjust poll interval, grace period, default policy, pause scanning, notification behaviour, container filters, and more from the web UI without restart
- **Appearance settings** — light mode, dark mode, or automatic (follows system preference)
- **Scanning tab** — configure poll interval, grace period, default policy, container filters, and `:latest` auto-update behaviour
- **Notification channels** — add/edit/test/delete notification providers with per-event filtering
- **Registry credentials** — manage authentication for private registries (Docker Hub, GHCR, custom)
- **Per-container overrides** — set notification mode per container from settings or container detail

### Authentication & Security
- **WebAuthn/passkey login** — passwordless authentication via hardware keys, biometrics, or platform authenticators
- **Password authentication** — traditional username/password with bcrypt hashing
- **Multi-user support** — create and manage users with role-based access control
- **API tokens** — generate long-lived tokens for programmatic access
- **Session management** — view and revoke active sessions, configurable session expiry
- **Built-in TLS** — serve HTTPS directly with your own certificates or automatic self-signed
- **Rate limiting** — login attempt throttling to prevent brute force attacks

### REST API
All dashboard functionality is exposed as JSON endpoints. See the [full API reference](#rest-api-1) below.

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

Then open `http://localhost:8080` in your browser. On first visit you'll be prompted to create an admin account.

## Configuration

All configuration is via environment variables. Settings marked with * can also be changed at runtime from the web UI.

### Core

| Variable | Default | Description |
|----------|---------|-------------|
| `SENTINEL_POLL_INTERVAL` | `6h` | How often to scan for updates * |
| `SENTINEL_GRACE_PERIOD` | `30s` | Wait time after update before health check * |
| `SENTINEL_DEFAULT_POLICY` | `manual` | Policy for unlabelled containers * |
| `SENTINEL_LATEST_AUTO_UPDATE` | `true` | Auto-update `:latest`-tagged containers when digest changes * |
| `SENTINEL_DB_PATH` | `/data/sentinel.db` | BoltDB database path |
| `SENTINEL_LOG_JSON` | `true` | JSON structured logging |
| `SENTINEL_DOCKER_SOCK` | `/var/run/docker.sock` | Docker socket path |

### Web & Authentication

| Variable | Default | Description |
|----------|---------|-------------|
| `SENTINEL_WEB_ENABLED` | `true` | Enable web dashboard |
| `SENTINEL_WEB_PORT` | `8080` | Web dashboard port |
| `SENTINEL_AUTH_ENABLED` | (auto) | Force auth on/off. Omit to auto-detect (enabled when users exist) |
| `SENTINEL_SESSION_EXPIRY` | `720h` | Session lifetime (30 days default) |
| `SENTINEL_COOKIE_SECURE` | `true` | Set `Secure` flag on session cookies (disable for HTTP-only setups) |
| `SENTINEL_TLS_CERT` | | Path to TLS certificate file |
| `SENTINEL_TLS_KEY` | | Path to TLS private key file |
| `SENTINEL_TLS_AUTO` | `false` | Generate self-signed TLS certificate automatically |
| `SENTINEL_WEBAUTHN_RPID` | | WebAuthn Relying Party ID (your domain, e.g. `sentinel.example.com`) |
| `SENTINEL_WEBAUTHN_DISPLAY_NAME` | `Docker-Sentinel` | WebAuthn display name shown during registration |
| `SENTINEL_WEBAUTHN_ORIGINS` | | Allowed WebAuthn origins, comma-separated |

### Legacy Notifications

| Variable | Default | Description |
|----------|---------|-------------|
| `SENTINEL_GOTIFY_URL` | | Gotify server URL (prefer web UI for new setups) |
| `SENTINEL_GOTIFY_TOKEN` | | Gotify application token |
| `SENTINEL_WEBHOOK_URL` | | Webhook URL for JSON POST |
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

## Notifications

### Channels

Notification channels are configured from the **Settings > Notification Channels** section in the web UI. Each channel supports per-event filtering so you can, for example, send only failures to Slack while sending everything to a webhook.

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

### Notification Modes

Control how each container triggers notifications. Set a global default from **Settings > Notification Behaviour**, or override per-container from the container detail page or **Settings > Per-Container Overrides**.

| Mode | Description |
|------|-------------|
| **Immediate + summary** | Alert as soon as an update is detected, plus a scheduled summary. Won't re-alert for the same update. |
| **Every scan** | Alert every time a scan finds updates, even if already notified. No summary. |
| **Summary only** | No immediate alerts — only the scheduled summary. |
| **Silent** | No notifications of any kind. |

### Daily Digest

When using a mode that includes a summary (immediate + summary, or summary only), Sentinel compiles a consolidated report of all pending updates and sends it at a configurable time and frequency (default: daily at 09:00). The dashboard also shows a dismissible banner when updates are pending.

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
| `GET` | `/api/containers` | List all containers with policy, status, and registry info |
| `GET` | `/api/containers/{name}` | Container detail (history, snapshots, versions) |
| `GET` | `/api/containers/{name}/versions` | Available image versions from registry |
| `GET` | `/api/containers/{name}/row` | HTML partial for live SSE row update |
| `GET` | `/api/containers/{name}/ghcr` | GHCR alternative info for this container |
| `GET` | `/api/containers/{name}/notify-pref` | Get per-container notification mode |
| `POST` | `/api/containers/{name}/policy` | Set policy override |
| `DELETE` | `/api/containers/{name}/policy` | Remove policy override (fall back to label) |
| `POST` | `/api/containers/{name}/rollback` | Rollback to last snapshot |
| `POST` | `/api/containers/{name}/restart` | Restart container |
| `POST` | `/api/containers/{name}/stop` | Stop container |
| `POST` | `/api/containers/{name}/start` | Start container |
| `POST` | `/api/containers/{name}/switch-ghcr` | Switch container image to GHCR alternative |
| `POST` | `/api/containers/{name}/notify-pref` | Set per-container notification mode |
| `POST` | `/api/check/{name}` | Manual registry check for single container |
| `POST` | `/api/update/{name}` | Trigger immediate update |
| `POST` | `/api/approve/{name}` | Approve queued update |
| `POST` | `/api/reject/{name}` | Reject queued update |
| `POST` | `/api/ignore/{name}` | Ignore this specific version (skip update) |
| `POST` | `/api/bulk/policy` | Bulk policy change for multiple containers |
| `POST` | `/api/scan` | Trigger a full scan immediately |

**Queue, History & Logs**

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/queue` | List pending updates |
| `GET` | `/api/history` | Update history |
| `GET` | `/api/logs` | Activity log entries |
| `GET` | `/api/events` | SSE event stream (real-time updates) |
| `GET` | `/api/last-scan` | Timestamp of last completed scan |

**Settings**

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/settings` | Current configuration |
| `POST` | `/api/settings/poll-interval` | Update poll interval at runtime |
| `POST` | `/api/settings/default-policy` | Set default policy at runtime |
| `POST` | `/api/settings/grace-period` | Set grace period at runtime |
| `POST` | `/api/settings/pause` | Pause or unpause the scheduler |
| `POST` | `/api/settings/filters` | Set container name filter patterns |
| `POST` | `/api/settings/stack-order` | Save custom stack ordering |
| `POST` | `/api/settings/latest-auto-update` | Toggle auto-update for `:latest` tags |

**Notifications**

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/settings/notifications` | Get notification channels (secrets masked) |
| `PUT` | `/api/settings/notifications` | Save notification channels |
| `POST` | `/api/settings/notifications/test` | Send a test notification |
| `GET` | `/api/settings/notifications/event-types` | List available event types |
| `GET` | `/api/settings/digest` | Get digest scheduler config |
| `POST` | `/api/settings/digest` | Save digest config (mode, time, interval) |
| `GET` | `/api/settings/container-notify-prefs` | Get all per-container notification modes |
| `POST` | `/api/digest/trigger` | Trigger an immediate digest |
| `GET` | `/api/digest/banner` | Get pending updates for dashboard banner |
| `POST` | `/api/digest/banner/dismiss` | Dismiss the digest banner |
| `DELETE` | `/api/notify-states` | Clear all notification dedup state |

**Registry**

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/settings/registries` | List registry credentials |
| `PUT` | `/api/settings/registries` | Save registry credentials |
| `POST` | `/api/settings/registries/test` | Test a registry credential |
| `DELETE` | `/api/settings/registries/{id}` | Delete a registry credential |
| `GET` | `/api/ratelimits` | Docker Hub rate limit status |
| `GET` | `/api/ghcr/alternatives` | GHCR alternatives for all Docker Hub images |

**About**

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/about` | Version, uptime, runtime stats, integrations |

**Authentication**

| Method | Endpoint | Description |
|--------|----------|-------------|
| `POST` | `/login` | Password login |
| `POST` | `/setup` | Initial admin account creation |
| `POST` | `/logout` | End session |
| `POST` | `/api/auth/change-password` | Change current user's password |
| `GET` | `/api/auth/sessions` | List active sessions |
| `DELETE` | `/api/auth/sessions/{token}` | Revoke a session |
| `DELETE` | `/api/auth/sessions` | Revoke all sessions |
| `POST` | `/api/auth/tokens` | Create API token |
| `DELETE` | `/api/auth/tokens/{id}` | Delete API token |
| `GET` | `/api/auth/me` | Current user info and permissions |
| `GET` | `/api/auth/users` | List all users (admin only) |
| `POST` | `/api/auth/users` | Create user (admin only) |
| `DELETE` | `/api/auth/users/{id}` | Delete user (admin only) |
| `POST` | `/api/auth/settings` | Update auth settings (admin only) |

**WebAuthn / Passkeys**

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/auth/passkeys/available` | Check if passkey login is available |
| `POST` | `/api/auth/passkeys/login/begin` | Begin passkey authentication |
| `POST` | `/api/auth/passkeys/login/finish` | Complete passkey authentication |
| `POST` | `/api/auth/passkeys/register/begin` | Begin passkey registration |
| `POST` | `/api/auth/passkeys/register/finish` | Complete passkey registration |
| `GET` | `/api/auth/passkeys` | List registered passkeys |
| `DELETE` | `/api/auth/passkeys/{id}` | Delete a passkey |

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
│   ├── auth/              # Authentication, sessions, passkeys, permissions
│   ├── clock/             # Time abstraction (testable)
│   ├── config/            # Environment variable configuration
│   ├── docker/            # Docker API client, labels, snapshots
│   ├── engine/            # Scheduler, updater, rollback, queue, policy
│   ├── events/            # Event bus (SSE fan-out)
│   ├── guardian/          # Docker-Guardian maintenance label integration
│   ├── logging/           # Structured slog logger
│   ├── notify/            # Notification providers (7 channels + log)
│   ├── registry/          # Registry digest checker, semver tag discovery, rate limits
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
