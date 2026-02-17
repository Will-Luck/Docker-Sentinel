# Cluster Management UI Design

**Date:** 2026-02-17
**Scope:** Approach A — standalone Cluster page + dashboard host groups

## Overview

Add a web UI for Docker-Sentinel's multi-host agent architecture. Two deliverables:

1. **Cluster page** (`/cluster`) — host cards, enrollment token generation, drain/revoke/remove lifecycle actions
2. **Dashboard host groups** — container table gains host-level grouping when cluster mode is active

Cluster mode is enabled from the Settings page. The Cluster nav link only appears once enabled — zero visual impact for non-cluster users.

## User Flow

1. User navigates to Settings → Cluster tab
2. Toggles "Enable cluster mode", configures port/grace period/default policy
3. Server dynamically creates CA and starts gRPC listener (no container restart)
4. "Cluster" link appears in nav bar
5. User generates enrollment token on Cluster page
6. Runs agent on remote host with the token
7. Agent appears as a host card on Cluster page; its containers appear in dashboard host groups

## Cluster Page

Only rendered when cluster mode is enabled. Permission: `auth.PermSettingsModify` (admin-only).

### Stat Cards Row

| Card | Value |
|------|-------|
| Hosts | Total registered host count |
| Connected | Count of online agents (green) |
| Containers | Sum of containers across all remote hosts |
| Server | "This node" + server version |

### Host Cards Grid

Responsive grid (1-3 columns). Each host card shows:

- Status dot: green (connected), red (disconnected), yellow (draining)
- Host name and address
- Container count, agent version
- Enrolled date, last seen timestamp
- Action buttons: Drain, Revoke, Remove (with confirmation modals)

Disconnected hosts show "Last seen: X ago" prominently.

### Enrollment Section

Bottom card with "Generate Token" button. On click:

- Displays the token string with a copy button
- Shows pre-filled agent start command with the server address
- Token is one-time use (consumed on enrollment)

### SSE Integration

Subscribes to existing `/api/events`. New event types:

- `host:connected` — update card status dot + "last seen"
- `host:disconnected` — same in reverse

No polling needed.

## Settings → Cluster Tab

New tab in the existing Settings page.

| Setting | Type | Default | Notes |
|---------|------|---------|-------|
| Enable cluster mode | Toggle | Off | Starts/stops gRPC server dynamically |
| gRPC port | Number input | 9443 | Port change shows "restart required" note |
| Grace period | Dropdown | 30m | Options: 5m, 15m, 30m, 1h, 2h |
| Default remote policy | Dropdown | Manual | auto/manual/pinned — for newly discovered remote containers |

- Fields greyed out until toggle is on
- Toggling off with connected agents shows confirmation warning
- Saved to BoltDB, persisted across restarts
- Env vars (`SENTINEL_CLUSTER=true`) override DB settings

## Dashboard Host Groups

When cluster is active, the container table adds a host-level grouping tier.

### Structure

```
▸ local                              23
  ▸ media-station                    10
  ▸ gitea                             3
  ...

▸ test-agent-1  ●                     6
  ▸ Standalone                        6

▸ test-agent-2  ○  (disconnected)     8
  ▸ Standalone                        8
```

- "local" is always first — this server's own containers
- Remote hosts are collapsible groups with status dot
- Inside each host, existing stack grouping still works (host → stack → container)
- Disconnected hosts show last-known state in muted style
- Stat cards aggregate across all hosts (fleet-wide totals)

### When cluster is off

Dashboard unchanged. No "local" wrapper, no host groups.

### Container actions on remote hosts

Action buttons (restart, approve) route through cluster gRPC internally. The UI doesn't distinguish — API handler checks `HostID` and dispatches accordingly.

### Queue and History integration

Host badge appears next to container name on queue and history pages for remote containers.

## API Changes

### New Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `GET /api/settings/cluster` | GET | Returns cluster config |
| `POST /api/settings/cluster` | POST | Save config, start/stop gRPC |

### Modified Endpoints

| Endpoint | Change |
|----------|--------|
| `GET /api/containers` | Add `host_id`, `host_name` fields for remote containers |

### New SSE Events

| Event | Payload | Trigger |
|-------|---------|---------|
| `host:connected` | `{hostID, hostName}` | Agent connects |
| `host:disconnected` | `{hostID, hostName}` | Agent disconnects |

Existing `container:*` events gain optional `hostID` field.

## Dynamic Cluster Lifecycle

```
Settings toggle ON:
  → Save config to BoltDB
  → Create CA if not exists (lazy init)
  → Start gRPC listener on configured port
  → Return success

Settings toggle OFF:
  → Warn if agents connected
  → Stop gRPC listener gracefully
  → Save disabled state to BoltDB
  → Return success

Startup:
  → Check BoltDB for saved cluster config
  → If enabled, start cluster server
  → Env vars override DB (Docker deployments)
```

## Files to Modify

| File | Change |
|------|--------|
| `static/cluster.html` | New — host cards, enrollment UI |
| `static/settings.html` | New Cluster tab |
| `static/index.html` | Host grouping in container table |
| `static/queue.html` | Host badge on pending items |
| `static/history.html` | Host badge on history entries |
| All `.html` templates | Conditional "Cluster" nav link |
| `static/style.css` | Host card styles, status dots, host badges |
| `internal/web/server.go` | Cluster settings routes, nav visibility flag in pageData |
| `internal/web/api_settings.go` | `handleClusterSettings` GET/POST |
| `internal/web/handlers.go` | `handleCluster` page handler, dashboard host groups |
| `internal/web/sse.go` | Host connected/disconnected events |
| `internal/store/store.go` | Cluster config BoltDB bucket |
| `cmd/sentinel/main.go` | Read cluster config from DB, dynamic start |
| `cmd/sentinel/adapters.go` | Settings adapter wiring |

## Non-Goals

- No visual topology/network diagram (cards are sufficient)
- No agent-side web UI (agents are headless)
- No multi-server HA (single server, multiple agents)
- No container migration between hosts
