# Portainer Integration Design

## Goal

Integrate with Portainer CE/BE to discover and update containers managed by Portainer, using Portainer's native API rather than bypassing it through raw Docker API calls. This respects Portainer's stack management, resource controls, and internal state.

## Architecture

Custom Portainer client in `internal/portainer/` that speaks Portainer's API directly. Two update paths depending on whether containers belong to a Compose stack or are standalone.

### Portainer API Surface Used

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/endpoints` | GET | List all environments/endpoints |
| `/api/endpoints/{id}/docker/containers/json` | GET | List containers on an endpoint |
| `/api/stacks` | GET | List all stacks |
| `/api/stacks/{id}` | PUT | Redeploy stack (with `pullImage=true`) |
| `/api/endpoints/{id}/docker/containers/{cid}/stop` | POST | Stop standalone container |
| `/api/endpoints/{id}/docker/containers/{cid}` | DELETE | Remove standalone container |
| `/api/endpoints/{id}/docker/containers/create` | POST | Create standalone container |
| `/api/endpoints/{id}/docker/containers/{cid}/start` | POST | Start standalone container |
| `/api/endpoints/{id}/docker/images/create` | POST | Pull image on endpoint |

Auth: `X-API-Key: {token}` header on all requests.

### Package Layout

```
internal/portainer/
  client.go    - HTTP client: list endpoints, list stacks, list containers,
                 trigger stack redeploy, Docker proxy calls for standalone updates
  scanner.go   - PortainerScanner: converts Portainer data into engine types,
                 called from scan loop after local + cluster scanning
  types.go     - Portainer-specific types (Endpoint, Stack, etc.)
```

### Integration Points

- `internal/engine/updater.go` - New `SetPortainer(PortainerScanner)` + `scanPortainerEndpoints()` called at end of `Scan()`
- `internal/config/config.go` - `SENTINEL_PORTAINER_URL`, `SENTINEL_PORTAINER_TOKEN`
- `internal/web/api_settings.go` - Settings handlers for URL/token, test connection
- `internal/web/api_portainer.go` - REST endpoints for Portainer page data
- `internal/web/static/portainer.html` - Dashboard page
- `cmd/sentinel/main.go` - Wire up Portainer client if URL configured
- `cmd/sentinel/adapters.go` - Adapter for web interfaces

## Data Flow

### Discovery (every scan cycle)

```
Scan() starts
  -> local containers (existing)
  -> cluster agents (existing)
  -> scanPortainerEndpoints() (new)
       -> client.ListEndpoints() -> filter Status=Up, Type=Docker
       -> for each endpoint:
            client.ListContainers(endpointID)
            client.ListStacks(endpointID)
            match containers to stacks via com.docker.compose.project label
            feed into same policy/registry check logic as cluster scanning
```

### Update Dispatch (two paths)

**Stack containers**: `PUT /api/stacks/{stackID}` with `pullImage=true`. Portainer handles the full redeploy with built-in rollback. One API call updates all containers in the stack.

**Standalone containers**: Orchestrate via Portainer's Docker proxy: stop, remove, pull, create, start through `/api/endpoints/{id}/docker/*`. Same sequence Portainer's UI uses internally.

### Keying

Portainer containers use `portainer:{endpointID}::{containerName}` as their key in the queue and policy store. Distinct prefix prevents collision with cluster keys (`{hostID}::{name}`).

## Error Handling

- **Portainer down**: Skip all Portainer endpoints for that scan cycle. Log warning. Portainer containers disappear from dashboard until it's back.
- **Endpoint offline**: Portainer reports `Status=Down` (EndpointStatus 2). Skip that endpoint, show greyed out in UI.
- **Stack redeploy fails**: Portainer's `PUT /stacks/{id}` has built-in rollback. Record outcome as `failed` in history, fire notification.
- **Standalone update fails**: Record failure. No rollback for standalone containers (matches Portainer's own UI behaviour).
- **Token invalid/expired**: First API call fails 401/403. Log error, skip Portainer scanning, surface warning banner in dashboard.
- **Duplicate hosts**: If a host has both a Sentinel agent AND a Portainer endpoint, detect by comparing endpoint URLs against connected agent addresses. Skip Portainer endpoint to avoid double-scanning.

## UI

### Portainer Page (new nav item)

- Connection status indicator (green/red) with Portainer instance name
- Endpoints as expandable cards: name, URL, status, container count
- Within each endpoint: container table (same layout as main dashboard)
- Stack containers grouped under stack name headers
- Update actions: same approve/auto-update flow as main dashboard

### Settings Page (Portainer section)

- URL field
- Token field (masked)
- Test connection button
- Enable/disable toggle
- Endpoint filter (optional regex to include/exclude endpoints by name)

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `SENTINEL_PORTAINER_URL` | (empty) | Portainer instance URL |
| `SENTINEL_PORTAINER_TOKEN` | (empty) | API access token |

Both can also be set at runtime via settings store (same pattern as other settings).

## Phase 2 (future)

Agent auto-enrollment: use Portainer's endpoint list to discover hosts and auto-deploy Sentinel agents, allowing users to migrate from Portainer-proxied updates to native agent-based updates.
