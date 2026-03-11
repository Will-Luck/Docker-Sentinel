# Docker-Sentinel Design Document

> Date: 2026-02-09 (original), updated 2026-02-14
> Status: As-built (reflects v2.x implementation)
> Author: Will (with Claude)

## Summary

Docker-Sentinel is a container update orchestrator with a web dashboard, written in Go. It replaces Watchtower with full per-container update policies, pre-update container snapshots, post-update health validation, automatic rollback, pluggable notifications, and Docker-Guardian integration via maintenance labels.

## Motivation

Watchtower works but operates as a black box:
- No per-container update policies — it updates everything or nothing
- No visibility into what it updated, when, or whether it broke something
- No rollback capability when a new image is broken
- No dashboard — just log output
- Spams warnings for local images it can't check (e.g. `docker-guardian:v2.3.0`)
- Only detects digest changes on the current tag — no awareness of newer versioned tags

Docker-Guardian proved the value of rewriting a simple community tool into something purpose-built for your infrastructure. Sentinel follows the same philosophy for the update lifecycle.

## Architecture

```
+-------------------------------------+
|          Docker-Sentinel             |
|                                      |
|  +-----------+  +---------------+    |
|  | Scheduler |  | Web Dashboard |    |
|  | (poll     |  | (embedded     |    |
|  |  cycle)   |  |  HTTP server) |    |
|  +-----+-----+  +-------+------+    |
|        |                |            |
|  +-----v----------------v--------+  |
|  |       Update Engine            |  |
|  |  - Registry checker            |  |
|  |  - Container snapshotter       |  |
|  |  - Update executor             |  |
|  |  - Health validator            |  |
|  |  - Rollback handler            |  |
|  +---------------+----------------+  |
|                  |                   |
|  +---------------v----------------+  |
|  |       Integrations             |  |
|  |  - Docker API (socket)         |  |
|  |  - 7 notification providers    |  |
|  |  - Guardian (maint. labels)    |  |
|  |  - BoltDB (history/state)      |  |
|  |  - Auth (passwords/passkeys)   |  |
|  +--------------------------------+  |
+--------------------------------------+
```

### Tech Stack

- **Language:** Go (same as Guardian — shared SDK, toolchain, CI pipeline)
- **Docker SDK:** moby/docker (same as Guardian)
- **Web frontend:** Vanilla JS + CSS — no build toolchain, embedded via `go:embed`, SSE for real-time updates
- **Persistent state:** BoltDB (update history, container snapshots, rollback data)
- **Container runtime:** Alpine-based Docker image, single static binary

## Update Lifecycle

The cycle runs on a configurable poll interval (default 6 hours).

### Step 1: Scan

Enumerate all running containers. Read `sentinel.policy` label:
- `auto` — update automatically when new image found
- `manual` — flag as "update available", wait for dashboard approval
- `pinned` — never update, never check
- Unset — defaults to `manual` (safe by default, opposite of Watchtower)

### Step 2: Check

For each non-pinned container, query the registry:
- **Mutable tags** (`:latest`, rolling tags): compare digest against running image
- **Versioned tags** (`:v2.3.0`): query all available tags, compare semver to find newer versions
- **Local-only images**: detected and skipped gracefully (no warning spam)

### Step 3: Queue

New images found get queued:
- `auto` containers proceed to update immediately
- `manual` containers are flagged in the dashboard and trigger a Gotify notification: "myapp has an update available — approve in Sentinel dashboard"
- Versioned tag containers with newer versions available: notification only, manual approval required regardless of policy

### Step 4: Snapshot

Before touching anything, capture full `docker inspect` JSON output and store in BoltDB. This includes: image, env vars, volumes, networks, labels, restart policy, port mappings, entrypoint, cmd — everything needed to recreate the container exactly. This is the rollback point.

### Step 5: Maintenance Label

Set `sentinel.maintenance=true` on the container. Docker-Guardian reads this label and holds off restart attempts during the update window.

### Step 6: Update

1. Pull new image
2. Stop container (respecting stop signal and timeout from original config)
3. Remove old container
4. Recreate with identical config from snapshot + new image
5. Start new container

### Step 7: Validate

Wait for a configurable grace period (default 30 seconds). Check the container:
- Is it still running?
- Has it exited or entered a restart loop?
- (Future: optional HTTP health check endpoint)

### Step 8: Finalise or Rollback

**If healthy:**
- Remove `sentinel.maintenance` label
- Clean up old image
- Log success to BoltDB
- Notify via configured providers

**If unhealthy:**
- Rollback: recreate container from snapshot with old image
- Remove `sentinel.maintenance` label
- Log failure to BoltDB
- Notify with failure details and rollback confirmation

### Step 9: History

Every update attempt (success or failure) is logged in BoltDB:
- Timestamp
- Container name
- Old image (tag + digest)
- New image (tag + digest)
- Outcome (success / rollback / skipped)
- Duration

## Registry Intelligence

### Mutable Tag Checking (`:latest`)

Standard digest comparison — same as Watchtower. Query the registry for the current tag's manifest digest, compare against the locally running image's digest.

### Versioned Tag Discovery

For containers using semver tags (e.g. `v2.3.0`, `4.0.11`):
1. Query the registry for all available tags
2. Parse as semver
3. Compare against current running version
4. If newer version exists, show in dashboard: "Running v2.3.0, v2.4.0 available"
5. Never auto-update versioned tags — always requires manual approval via dashboard

### Registry Authentication

Support Docker Hub, GHCR, and private registries. Credentials managed via the web UI (Settings > Registries tab) with per-registry add/test/delete. Rate limit tracking for Docker Hub displays remaining quota on the dashboard.

### GHCR Alternative Detection

Automatically discovers GitHub Container Registry mirrors for Docker Hub images. Shows registry source badges (Hub, GHCR, LSCR, Gitea, custom) and offers one-click migration to GHCR alternatives.

### Local Image Handling

Detect images with no registry prefix or that fail registry lookup. Mark as "local" in the dashboard and skip entirely — no warnings, no retries.

## Docker-Guardian Integration

Sentinel communicates with Guardian in one direction: Sentinel tells Guardian to back off during updates.

### Mechanism: Docker Labels

Before an update:
```
sentinel.maintenance=true   (set on the container)
```

After update completes (success or rollback):
```
sentinel.maintenance         (label removed)
```

Guardian checks for this label before attempting restarts. No API, no shared volume, no network communication needed. Labels survive container recreation since Sentinel controls the full lifecycle.

### Future Consideration

The integration is one-directional by design. Guardian does not feed health data back to Sentinel. This keeps coupling minimal and means Guardian needs no code changes beyond checking one additional label.

## Notification System

### Interface

```go
type Notifier interface {
    Send(event UpdateEvent) error
    Name() string
}
```

### Providers

| Provider | Description |
|----------|-------------|
| **Gotify** | Push notifications via Gotify server |
| **Slack** | Incoming webhook integration |
| **Discord** | Webhook integration |
| **Ntfy** | Push notifications via ntfy server |
| **Telegram** | Bot API notifications |
| **Pushover** | Push notifications via Pushover |
| **Generic webhook** | POST JSON to any URL (covers N8N, IFTTT, etc.) |
| **Log** | Structured JSON to stdout (always on, baseline for `docker logs`) |

### Event Types

- `update_available` — new image found for manual-policy container
- `update_started` — update lifecycle beginning
- `update_succeeded` — container updated and healthy
- `update_failed` — update failed, rollback triggered
- `rollback_succeeded` — successfully rolled back to previous image
- `rollback_failed` — rollback also failed (critical alert)
- `version_available` — newer semver tag discovered for versioned container

### Per-Container Notification Modes

Per-container notification behaviour is configured via the web UI (not labels). Four modes: immediate + summary, every scan, summary only, or silent. Set globally in Settings > Notification Behaviour, or per-container from the container detail page.

## Web Dashboard

Single-page app served by the embedded Go HTTP server. Vanilla JS with SSE for real-time updates, no build toolchain.

### Container Overview (Home Page)

- Table of all monitored containers: name, image, current tag, running status, policy badge, last checked, last updated
- Colour-coded status indicators:
  - Green: up to date
  - Amber: update available
  - Red: update failed / rolled back
- Versioned tag containers show "newer version available: v4.0.14" badge with approve button
- Quick-action buttons: update now, change policy, view history

### Update Queue

- Pending manual approvals with image diff info (current tag -> available tag)
- Bulk approve / reject
- For `:latest` images: old digest vs new digest (short hashes)
- For versioned tags: current version vs available version

### History Log

- Chronological list of all update attempts
- Each entry: timestamp, container, old -> new image, outcome, duration
- Click into an entry to see the full container snapshot that was captured

### Settings

- Global defaults: poll interval, grace period, default policy for unlabelled containers
- Per-container policy overrides (writes labels back to Docker)
- Notification provider configuration (Gotify URL/token, webhook URLs)
- Guardian integration toggle

### REST API

All dashboard functionality exposed as JSON endpoints, enabling:
- N8N workflow integration
- CLI scripting
- Future alternative frontends

## Docker Labels Reference

| Label | Values | Default | Description |
|-------|--------|---------|-------------|
| `sentinel.policy` | `auto`, `manual`, `pinned` | `manual` | Update policy |
| `sentinel.maintenance` | `true` | (absent) | Set during updates, read by Guardian |
| `sentinel.self` | `true` | (absent) | Prevents Sentinel from updating itself |

## Project Structure

```
Docker-Sentinel/
├── cmd/sentinel/                # Entry point, config loading, wire-up
├── internal/
│   ├── auth/                    # Authentication, sessions, passkeys, RBAC
│   ├── clock/                   # Time abstraction (testable)
│   ├── config/                  # Global config, env vars, defaults
│   ├── docker/                  # Docker API client, labels, snapshots
│   ├── engine/                  # Scheduler, updater, rollback, queue, policy
│   ├── events/                  # Event bus (SSE fan-out)
│   ├── guardian/                # Docker-Guardian maintenance label integration
│   ├── logging/                 # Structured slog logger
│   ├── notify/                  # Notification providers (7 channels + log)
│   ├── registry/                # Registry digest checker, semver tags, rate limits
│   ├── store/                   # BoltDB persistence layer
│   └── web/                     # HTTP server, REST API, embedded dashboard
│       └── static/              # HTML templates, CSS, JavaScript
├── .gitea/workflows/            # Gitea Actions CI
├── .github/workflows/           # GitHub Actions release
├── Dockerfile                   # Multi-stage Alpine build
├── Makefile
└── LICENSE                      # Apache 2.0
```

## Build & Deployment

- **Makefile** with targets: `build`, `test`, `lint`, `docker`, `clean` (same pattern as Guardian)
- **Multi-stage Dockerfile:** Go builder -> Alpine runtime
- **CI:** Gitea Actions first, GitHub Actions when public
- **Static assets:** embedded via `go:embed`
- **Single binary** — no runtime dependencies

### Container Deployment

```bash
docker run -d \
  --name docker-sentinel \
  --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -p 8080:8080 \
  -e SENTINEL_POLL_INTERVAL=6h \
  -e SENTINEL_GOTIFY_URL=http://gotify.example.com \
  -e SENTINEL_GOTIFY_TOKEN=<token> \
  docker-sentinel:v1.0.0
```

## Implementation Phases (all complete)

### Phase 1: Core Engine
- Docker client wrapper, container scanning, label parsing
- Registry digest checking (mutable tags)
- Basic update lifecycle (snapshot, update, validate, rollback)
- BoltDB storage for history and snapshots
- Structured log output

### Phase 2: Notifications & Guardian
- Notifier interface + 7 providers (Gotify, Slack, Discord, Ntfy, Telegram, Pushover, webhook)
- Guardian maintenance label integration
- Per-container notification modes, daily digest scheduler

### Phase 3: Registry Intelligence
- Versioned tag discovery and semver comparison
- Registry auth management via web UI
- Local image detection, rate limit tracking, GHCR alternative detection
- Registry source badges

### Phase 4: Web Dashboard
- Embedded HTTP server with vanilla JS frontend
- Container overview, update queue, history log, activity logs
- REST API for all operations (50+ endpoints)
- Dashboard-driven policy changes, bulk actions, container controls
- Settings page with scanning, notifications, registries, appearance, security, about tabs
- Stack grouping with drag-and-drop, filter pills, clickable stat cards

### Phase 5: Authentication & Security
- WebAuthn/passkey login, password authentication
- Multi-user RBAC, API tokens, session management
- Built-in TLS support, rate-limited login

### Phase 6: Polish & Release
- CI on Gitea Actions + GitHub Actions
- GitHub releases with multi-arch binaries + GHCR Docker image
- Self-update capability
- About page with runtime stats and GitHub links
