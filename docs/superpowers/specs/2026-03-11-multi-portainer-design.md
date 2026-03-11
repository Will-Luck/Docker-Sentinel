# Multi-Instance Portainer Integration

## Problem

The current Portainer connector has three issues:

1. **Single instance limit.** Only one Portainer URL + token can be configured. Users with multiple Portainer instances (e.g. office and home networks) cannot connect them all.
2. **Local socket creates duplicates.** When Portainer's "local" endpoint points at the same Docker socket Sentinel monitors, every container is scanned twice, creating duplicate queue entries.
3. **Portainer containers missing from dashboard.** Containers discovered via Portainer do not appear in the main container table, only in the queue when updates are found.

## Design

### 1. Data Model

New `portainer_instances` BoltDB bucket. Each entry keyed by a generated ID (`p1`, `p2`, ...):

```json
{
  "id": "p1",
  "name": "Office",
  "url": "https://portainer.office.com",
  "token": "ptr_abc...",
  "enabled": true,
  "endpoints": {
    "3": {"enabled": true},
    "5": {"enabled": false},
    "7": {"blocked": true, "reason": "local_socket"}
  }
}
```

Fields:
- `id` — auto-generated, stable across edits
- `name` — user-chosen label, displayed on dashboard tabs and host groups
- `url` — Portainer API base URL
- `token` — API access token
- `enabled` — master toggle for the whole instance
- `endpoints` — per-endpoint config, populated after first successful connection test
  - `enabled` — user toggle for remote endpoints (default `true`)
  - `blocked` — auto-set for local socket endpoints, not overridable via UI
  - `reason` — why it was blocked (e.g. `"local_socket"`)

### 2. Local Socket Detection

On connection test or endpoint refresh, each endpoint is checked:

- `URL` starts with `unix://` or is empty with `Type == 1` (Docker environment) -> blocked as local socket
- All other URLs (TCP, agent) -> allowed

Blocked endpoints appear greyed out in the UI with explanation: "Sentinel already monitors this host directly via the Docker socket. Scanning it via Portainer would create duplicates."

No toggle is shown for blocked endpoints. If ALL endpoints on an instance are blocked, a message explains: "All endpoints on this instance use the local Docker socket. Sentinel monitors these containers directly, so no Portainer connector is needed."

The existing container ID dedup logic stays as a safety net for edge cases (e.g. TCP URL that actually resolves to localhost).

### 3. Connector UI

The connectors page Portainer section becomes a list of instance cards with an "Add Instance" button.

Each card shows:
- Instance name (editable)
- URL (editable)
- Token (editable, password field)
- Connection status indicator
- Endpoint list with toggles (populated after successful test)
- Test Connection button
- Remove button (with confirmation dialog)

**Add flow:** Click "Add Instance", empty card appears with Name/URL/Token fields. Fill in, click "Test Connection". On success: endpoints populate with auto-detection, instance is saved and enabled. On failure: error shown, nothing saved.

**Edit flow:** Name, URL, token editable inline (onchange/blur). Changing URL or token prompts re-test since endpoints may have changed.

**Remove flow:** Confirmation dialog. On confirm, instance deleted from DB, scanner removed from engine, any queue entries from that instance pruned on next scan.

**Migration:** Old `portainer_url`/`portainer_token`/`portainer_enabled` settings auto-migrate to a single instance named "Portainer" on first boot. Old settings cleared after migration.

### 4. Dashboard Integration

Remote Portainer endpoints appear on the dashboard as host groups, same pattern as cluster hosts.

**Tab navigation:**
- Each Portainer instance gets one tab, labelled with the user-chosen name (e.g. "Office", "Home")
- Within a tab, each enabled endpoint is a collapsible host group
- If only one instance with one endpoint, the tab uses just the instance name

**Stat cards** include Portainer containers in totals when viewing "All", show per-tab counts when a specific tab is selected.

**Queue entries** show instance and endpoint context (e.g. "nginx, Office / server-2").

**Container detail pages** use the URL format `/container/{name}?host=portainer:{instanceID}:{endpointID}`. The handler fetches from the correct Portainer instance.

### 5. Engine Changes

**Current:** Single `PortainerScanner` on the updater.

**New:** List of `PortainerInstance` structs, each holding a scanner and endpoint config:

```go
type PortainerInstance struct {
    ID        string
    Name      string
    Scanner   PortainerScanner
    Endpoints map[int]EndpointConfig
}
```

**Scan flow:**
1. `Scan()` builds `localIDs` set (unchanged)
2. Iterates `u.portainerInstances` instead of single scanner
3. For each instance: fetch endpoints, skip blocked/disabled, scan remaining
4. Existing per-container logic (policy, rate limits, registry checks) unchanged

**Queue/history key format:** Changes from `portainer:{endpointID}::name` to `portainer:{instanceID}:{endpointID}::name` to avoid collisions between instances.

**Hot-reload:** Factory pattern becomes list-aware. Adding/removing/editing an instance rebuilds just that instance's scanner. Other instances unaffected.

### 6. Error Handling

- Instance connection failure: log warning, skip instance, continue with others
- Single endpoint failure: log warning, skip endpoint, continue with remaining on same instance
- Token expiry: connection test fails, shown as "Disconnected" on connector card. Does not block other instances.
- Instance removed while containers in queue: prune logic handles it (containers no longer in `liveNames` get pruned)

### 7. Migration

On first boot after upgrade:
1. Check for old `portainer_url`/`portainer_token` settings
2. If present, create instance `{id: "p1", name: "Portainer", url: ..., token: ..., enabled: true}`
3. Fetch endpoints and populate endpoint config with auto-detection
4. Migrate existing queue/history keys from `portainer:{epID}::name` to `portainer:p1:{epID}::name`
5. Clear old settings keys

## Scope

| Component | Changes |
|-----------|---------|
| `internal/store/` | New `portainer_instances` bucket, CRUD operations, migration |
| `internal/portainer/` | No changes (client/scanner already instance-scoped) |
| `internal/engine/` | Multi-instance scanner list, updated scan loop, new key format |
| `internal/web/` | New API endpoints for instance CRUD, dashboard handler changes |
| `cmd/sentinel/main.go` | Multi-instance factory wiring, migration on boot |
| `static/connectors.html` | Instance cards UI, endpoint toggles |
| `static/index.html` | Portainer host groups and tabs |
| `static/src/js/` | Instance management JS, dashboard tab updates |

## Non-Goals

- Portainer user/team management (out of scope, Sentinel uses API tokens)
- Kubernetes endpoints (only Docker endpoints are scanned)
- Portainer stack editing from Sentinel UI (Sentinel only triggers redeployments)
