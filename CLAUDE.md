# Docker-Sentinel

Container update orchestrator with web dashboard, written in Go.

## Project Overview

Replaces Watchtower with per-container update policies, container snapshots with rollback, a web dashboard, pluggable notifications, and Docker-Guardian integration.

Design document is in `docs/DESIGN.md` (local-only, gitignored).

## Tech Stack

- **Language:** Go 1.24
- **Docker SDK:** moby/docker
- **Web frontend:** Vanilla JS + CSS (embedded via `go:embed`), SSE for real-time updates
- **Persistent state:** BoltDB
- **Container runtime:** Alpine-based Docker image

## Build

```bash
make build    # Build binary to bin/sentinel
make test     # Run tests with race detector
make lint     # golangci-lint
make docker   # Build Docker image
```

## Project Structure

- `cmd/sentinel/` — Entry point
- `internal/config/` — Configuration and env vars
- `internal/docker/` — Docker API client, snapshots, label management
- `internal/registry/` — Registry digest checking, semver tag discovery, release notes, tag filtering
- `internal/engine/` — Scheduler, update lifecycle, rollback, approval queue
- `internal/auth/` — Authentication, sessions, passkeys, RBAC permissions
- `internal/notify/` — Notification interface + providers (10 channels + log), HA MQTT discovery
- `internal/guardian/` — Docker-Guardian maintenance label integration
- `internal/store/` — BoltDB persistence layer
- `internal/web/` — Embedded HTTP server, REST API, dashboard
- `internal/events/` — Event bus for SSE fan-out
- `internal/clock/` — Time abstraction for testability
- `internal/logging/` — Structured slog logger

## Conventions

- Same patterns as Docker-Guardian where applicable
- UK English throughout
- Apache 2.0 licence
- CI runs on Gitea Actions (push to Gitea first, GitHub only for public release)
- No `docker compose` -- single container deployed via `docker run`
- **CLAUDE.md is living documentation:** Any change to API routes, data types, keying conventions, SSE events, or request flows must update the Architecture Reference section below before committing

## Key Design Decisions

- **Safe by default:** Unlabelled containers default to `manual` policy (not auto-update)
- **Labels as source of truth:** Policies set via Docker labels, dashboard can read them
- **One-directional Guardian integration:** Sentinel sets `sentinel.maintenance=true` during updates; Guardian reads it. Label is removed after successful validation via `finaliseContainer()`.
- **Pluggable notifications:** Interface-based — 10 providers (Gotify, Slack, Discord, Ntfy, Telegram, Pushover, webhook, SMTP, Apprise, MQTT) + structured log. Events fired at 6 lifecycle points. Rich markdown formatting for Discord, Slack, Telegram, ntfy.
- **Full container snapshots:** `docker inspect` JSON stored in BoltDB before every update, enabling exact rollback
- **Web adapter pattern:** Web package uses mirror types to avoid import cycles; main.go has adapter structs bridging store/engine/docker to web interfaces
- **innerHTML for server HTML only:** innerHTML is used exclusively for inserting server-rendered HTML fragments (row replacements, modal content). Never use innerHTML with user-supplied or client-constructed strings.

## Release & CI

- **GitHub repo:** `Will-Luck/Docker-Sentinel` (remote name: `github`)
- **Gitea repo:** `GiteaLN/Docker-Sentinel` (remote name: `origin`)
- **Release workflow:** `.github/workflows/release.yml` — triggered by `v*` tags pushed to GitHub
- **Release artifacts:** Multi-arch binaries (linux/amd64, linux/arm64, linux/arm/v7, darwin/amd64, darwin/arm64) + GHCR Docker image (amd64 + arm64)
- **GHCR image:** `ghcr.io/will-luck/docker-sentinel` — tagged `latest`, `x.y.z`, `x.y`, `x`
- **Multi-arch build takes ~5 min** (QEMU emulation for arm64); binary cross-compile takes ~1 min
- **Cache restore tar warnings** in GitHub Actions annotations are harmless — non-fatal
- **Tag cleanup procedure:** `gh release delete` → `git push github --delete <tag>` → `git tag -d <tag>` → re-tag → push
- **Current release:** v1.12.2

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SENTINEL_DOCKER_SOCK` | `/var/run/docker.sock` | Docker socket path |
| `SENTINEL_POLL_INTERVAL` | `6h` | Scan interval |
| `SENTINEL_GRACE_PERIOD` | `30s` | Wait before health check |
| `SENTINEL_DEFAULT_POLICY` | `manual` | Default update policy |
| `SENTINEL_DB_PATH` | `/data/sentinel.db` | BoltDB path |
| `SENTINEL_LOG_JSON` | `true` | JSON structured logging |
| `SENTINEL_GOTIFY_URL` | (empty) | Gotify server URL |
| `SENTINEL_GOTIFY_TOKEN` | (empty) | Gotify app token |
| `SENTINEL_WEBHOOK_URL` | (empty) | Webhook endpoint |
| `SENTINEL_WEBHOOK_HEADERS` | (empty) | Comma-separated `Key:Value` pairs |
| `SENTINEL_LATEST_AUTO_UPDATE` | `false` | Auto-update `:latest` tags on digest change |
| `SENTINEL_WEB_ENABLED` | `true` | Enable web dashboard |
| `SENTINEL_WEB_PORT` | `8080` | Web dashboard port |
| `SENTINEL_AUTH_ENABLED` | (auto) | Force auth on/off |
| `SENTINEL_SESSION_EXPIRY` | `720h` | Session lifetime |
| `SENTINEL_COOKIE_SECURE` | `true` | Secure cookie flag |
| `SENTINEL_TLS_CERT` | (empty) | TLS certificate path |
| `SENTINEL_TLS_KEY` | (empty) | TLS private key path |
| `SENTINEL_TLS_AUTO` | `false` | Auto self-signed TLS |
| `SENTINEL_WEBAUTHN_RPID` | (empty) | WebAuthn Relying Party ID |
| `SENTINEL_WEBAUTHN_DISPLAY_NAME` | `Docker-Sentinel` | WebAuthn display name |
| `SENTINEL_WEBAUTHN_ORIGINS` | (empty) | Allowed WebAuthn origins |
| `SENTINEL_CLUSTER` | `false` | Enable cluster mode |
| `SENTINEL_CLUSTER_PORT` | `9443` | gRPC cluster port |
| `SENTINEL_MODE` | `standalone` | `standalone`, `controller`, or `agent` |
| `SENTINEL_SERVER_ADDR` | (empty) | Controller address (agent mode) |
| `SENTINEL_ENROLL_TOKEN` | (empty) | Enrollment token (agent mode) |
| `SENTINEL_HOST_NAME` | (hostname) | Agent display name |
| `SENTINEL_IMAGE_CLEANUP` | `true` | Remove old images after update |
| `SENTINEL_SCHEDULE` | (empty) | Cron expression (overrides poll interval) |
| `SENTINEL_HOOKS` | `false` | Enable update lifecycle hooks |
| `SENTINEL_HOOKS_WRITE_LABELS` | `false` | Allow hooks to write Docker labels |
| `SENTINEL_DEPS` | `true` | Dependency-aware restart ordering |
| `SENTINEL_ROLLBACK_POLICY` | (empty) | Policy after rollback: `manual` or `pinned` |
| `SENTINEL_METRICS` | `false` | Enable Prometheus metrics endpoint |
| `SENTINEL_METRICS_TEXTFILE` | (empty) | Path for Prometheus textfile collector output |
| `SENTINEL_IMAGE_BACKUP` | `false` | Retag current image before update |
| `SENTINEL_SHOW_STOPPED` | `false` | Include stopped containers in dashboard |
| `SENTINEL_REMOVE_VOLUMES` | `false` | Remove anonymous volumes during update |
| `SENTINEL_SCAN_CONCURRENCY` | `1` | Parallel registry check workers |
| `SENTINEL_CLUSTER_DIR` | `/data/cluster` | CA/cert storage directory |
| `SENTINEL_GRACE_PERIOD_OFFLINE` | `30m` | Agent autonomous mode threshold |

## Docker Labels

| Label | Values | Description |
|-------|--------|-------------|
| `sentinel.policy` | `auto`, `manual`, `pinned` | Update policy for the container |
| `sentinel.semver` | `patch`, `minor`, `major` | Semver scope constraint for version filtering |
| `sentinel.include-tags` | regex | Only consider tags matching this pattern |
| `sentinel.exclude-tags` | regex | Ignore tags matching this pattern |
| `sentinel.delay` | duration (`72h`, `7d`) | Only update to images older than this |
| `sentinel.pull-only` | `true` | Pull new image without restarting container |
| `sentinel.notify-snooze` | duration (`12h`, `3d`) | Suppress repeat notifications per version |
| `sentinel.schedule` | cron expression | Per-container scan schedule |
| `sentinel.remove-volumes` | `true` | Remove anonymous volumes during update |
| `sentinel.maintenance` | `true` | Set during updates for Guardian integration |

Labels are parsed in `internal/docker/labels.go`. Duration values support `d` suffix for days.

## Architecture Reference

> **Keep this section current.** Any change to API routes, data types, keying conventions,
> SSE events, or request flows must update the relevant subsection here before committing.

### System Overview

Docker-Sentinel runs in two modes:

- **Controller** (standalone or cluster leader): runs the web dashboard, scan engine, update queue, and gRPC server. Makes all update decisions.
- **Agent** (cluster member): connects to the controller via mTLS gRPC, reports container lists, executes actions (stop/start/restart/update) on command.

Package dependency flow (no import cycles):

```
cmd/sentinel/         -- entry point + adapters (bridges packages)
  -> internal/web/    -- HTTP server, REST API, templates (defines interfaces)
  -> internal/engine/ -- scan loop, update lifecycle, queue
  -> internal/cluster/server/ -- gRPC server, registry, streams
  -> internal/cluster/agent/  -- gRPC client, enrollment, command handlers
  -> internal/store/  -- BoltDB persistence
  -> internal/events/ -- SSE pub/sub bus
  -> internal/docker/ -- Docker API client
  -> internal/config/ -- env var parsing, runtime-mutable settings
```

The `web` package never imports `store`, `engine`, or `cluster` directly. It defines interfaces (`ContainerLister`, `UpdateQueue`, `ClusterProvider`, etc.) and mirror types. The `cmd/sentinel/adapters.go` file contains adapter structs that bridge concrete types to web interfaces, preventing import cycles.

### Request Flow: Remote Container Action

Canonical flow for stop/start/restart on a remote container:

```
User clicks "Stop" on remote container row
  -> JS: triggerAction(name, "stop", hostId)
     -> fetch POST /api/containers/{name}/stop?host={hostId}
  -> Go: apiStop (api_control.go)
     -> reads ?host= query param
     -> go Cluster.RemoteContainerAction(ctx, hostID, name, "stop")
     -> returns HTTP 200 immediately (async)
  -> Adapter: clusterAdapter.RemoteContainerAction (adapters.go)
     -> srv.ContainerActionSync(ctx, hostID, name, "stop")
  -> gRPC Server (server.go):
     -> registerPending(hostID, requestID) -- create response channel
     -> SendCommand(hostID, ServerMessage{ContainerActionRequest})
     -> awaitPending() -- block until agent responds
  -> Wire: ServerMessage sent on agent's bidirectional stream
  -> Agent (agent.go):
     -> receiveLoop reads ServerMessage, dispatches handleContainerAction
     -> docker.StopContainer()
     -> stream.Send(AgentMessage{ContainerActionResult{outcome, requestID}})
     -> handleListContainers() -- push fresh container list immediately
  -> gRPC Server receives AgentMessage:
     -> handleContainerActionResult (server.go)
        -> registry.UpdateContainerState(hostID, name, "exited") -- patch cache
        -> bus.Publish(SSEEvent{EventContainerState, name, hostID})
        -> deliverPending(hostID, requestID) -- unblock awaitPending
  -> SSE event reaches browser:
     -> JS: updateContainerRow(name, hostId)
        -> fetch /api/containers/{name}/row?host={hostId}
        -> handleContainerRow reads from AllHostContainers() cache
        -> returns server-rendered HTML fragment
        -> DOM: replaces <tr> in-place
```

### Request Flow: Update Lifecycle

```
Scan loop:
  scheduler.go: Scheduler.Run() fires Updater.Scan() on interval/cron
  updater.go: Scan()
    -> docker.ListContainers()
    -> For each container:
       -> ResolvePolicy() (DB override > label > latest-auto > default)
       -> Skip if pinned, filtered, or rate-limited
       -> checker.CheckVersioned(image)
          -> local digest vs remote digest
          -> if semver tag: ListTags + NewerVersions()
       -> Route by policy:
          -> auto: UpdateContainer() immediately
          -> manual: queue.Add(PendingUpdate{...})
    -> scanRemoteHosts() for cluster agents

Queue approval:
  api_queue.go: apiApprove
    -> queue.Approve(key) -- returns PendingUpdate, removes from queue
    -> Dispatch based on PendingUpdate fields:
       -> HostID != "": cluster.UpdateRemoteContainer(hostID, name, image)
       -> Type == "service": swarm.UpdateService()
       -> else: updater.UpdateContainer(name, image)

Local update (update.go):
  InspectContainer -> SaveSnapshot -> PullImage
  -> Stop -> Remove -> Create (with maintenance label) -> Start
  -> gracePeriod wait -> validateContainer
  -> finaliseContainer (stop/remove/create/start without maintenance label)
  -> RecordUpdate(outcome) -> cleanup old images -> restart dependents

Remote update:
  adapters.go: UpdateRemoteContainer -> srv.UpdateContainerSync
  -> agent: recreateContainer (pull, stop, remove, create, start)
  -> server: handleUpdateResult -> SSE event + deliverPending
```

### The ?host= Routing Pattern

All cluster-aware handlers follow this pattern:

```go
hostID := r.URL.Query().Get("host")
if hostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
    // Look up container from AllHostContainers() cache
    // Dispatch via Cluster.RemoteContainerAction() or UpdateRemoteContainer()
    return
}
// Fall through to local Docker path
```

**Handlers with ?host= support:**
`apiRestart`, `apiStop`, `apiStart`, `apiUpdate`, `apiCheck`, `apiUpdateToVersion`,
`apiChangePolicy`, `apiDeletePolicy`, `handleContainerDetail`, `handleContainerRow`

**Local-only handlers:**
`apiRollback` (needs local snapshot), `apiApprove` (routes on PendingUpdate.HostID, not query param),
all settings, all auth

**JS functions that pass hostId:**
`triggerAction`, `triggerUpdate`, `triggerCheck`, `updateToVersion`, `changePolicy`

### Key Data Types

| Type | Location | Persisted? | Key Format |
|------|----------|-----------|------------|
| `ContainerSummary` | `web/server.go` | No | - |
| `ContainerInfo` | `cluster/types.go` | No (in-memory cache) | - |
| `PendingUpdate` | `engine/queue.go` | Yes (BoltDB `queue`) | `name` or `hostID::name` |
| `UpdateRecord` | `store/bolt.go` | Yes (BoltDB `history`) | RFC3339Nano |
| `LogEntry` | `store/bolt.go` | Yes (BoltDB `logs`) | RFC3339Nano |
| `HostInfo` | `cluster/types.go` | Yes (BoltDB `cluster_hosts`) | host UUID |
| `HostState` | `cluster/server/registry.go` | Partial (Info persisted, Containers/Connected ephemeral) | host UUID |
| `containerView` | `web/handlers.go` | No (template data) | - |
| `SSEEvent` | `events/bus.go` | No (pub/sub) | - |

### Keying Conventions

| Scope | Local key | Remote key | Notes |
|-------|----------|-----------|-------|
| Queue entries | `name` | `hostID::name` | `PendingUpdate.Key()` auto-selects |
| Policy overrides | `name` | `hostID::name` | Same pattern as queue |
| Snapshots | `name::RFC3339Nano` | N/A | Snapshots are local-only |
| History records | RFC3339Nano | RFC3339Nano | Global, HostID field on record |
| Pending gRPC requests | - | `hostID:requestID` | Single colon, not double |

### SSE Events

| Event | Emitted By | JS Handler |
|-------|-----------|-----------|
| `container_update` | scan, update start/end | `updateContainerRow(name, hostId)` |
| `container_state` | stop/start/restart result | `updateContainerRow(name, hostId)` |
| `queue_change` | queue add/remove/approve | `updateQueueBadge()` + `refreshDashboardStats()` |
| `scan_complete` | scan loop finish | clear spinner, `refreshLastScan()`, show warnings |
| `policy_change` | policy set/delete | `updateContainerRow()` |
| `cluster_host` | agent connect/disconnect | cluster page refresh |
| `service_update` | Swarm service update | `refreshServiceRow()` |
| `settings_change` | settings API | `checkPauseState()` |
| `rate_limits` | post-scan rate check | update rate limit indicator |
| `digest_ready` | daily digest sent | `loadDigestBanner()` |

### Debugging Patterns

**"Container not found" on remote action:**
1. Check handler has `?host=` routing (see routing pattern above)
2. Check JS function passes hostId to the API call
3. Check template passes `{{.HostID}}` or `{{.Container.HostID}}`

**Row flickers back to old state after action:**
1. `registry.UpdateContainerState()` must be called in the result handler before SSE fires
2. SSE triggers `updateContainerRow()` which fetches `/api/containers/{name}/row?host=`
3. That handler reads from `AllHostContainers()` which returns cached data
4. If cache not patched before SSE, stale state renders

**Policy not sticking on remote containers:**
1. Policy key must be `hostID::name` for remote containers
2. Check `apiChangePolicy` builds `policyKey` with host prefix
3. Check dashboard/detail handlers look up policy with `hostID::name`

**Queue entry not found for remote container:**
1. Queue key is `hostID::name` (from `PendingUpdate.Key()`)
2. `apiApprove` reads key from URL path, not query param
3. `apiUpdate` looks up queue by `hostID + "::" + name`

### File Quick Reference

| Area | Files |
|------|-------|
| Web handlers | `internal/web/handlers.go`, `api_control.go`, `api_queue.go`, `api_policy.go` |
| Web server | `internal/web/server.go` (interfaces, mirror types, route registration) |
| SSE | `internal/web/sse.go`, `internal/events/bus.go` |
| Adapters | `cmd/sentinel/adapters.go` (all bridge code between packages) |
| Cluster server | `internal/cluster/server/server.go` (gRPC, pending, streams) |
| Cluster registry | `internal/cluster/server/registry.go` (host/container cache) |
| Cluster agent | `internal/cluster/agent/agent.go` (enrollment, session, handlers) |
| Cluster types | `internal/cluster/types.go` (HostInfo, ContainerInfo, HostState) |
| Update engine | `internal/engine/updater.go` (scan), `update.go` (execute), `queue.go` |
| Self-update | `internal/engine/selfupdate.go` |
| Store | `internal/store/bolt.go` (BoltDB buckets and all persistence methods) |
| Config | `internal/config/config.go` |
| Frontend | `internal/web/static/app.js`, `index.html`, `container.html`, `queue.html`, `history.html`, `logs.html` |

### Common Recipes

**Adding a new API endpoint:**
1. Add handler method on `*Server` in the appropriate `api_*.go` file
2. Register route in `server.go` `registerRoutes()`
3. If cluster-aware: add `?host=` routing pattern (see above)
4. If cluster-aware: add JS function with `hostId` parameter in `app.js`
5. Update Architecture Reference sections above

**Adding a new SSE event:**
1. Add event constant in `events/bus.go`
2. Publish via `s.deps.EventBus.Publish()` from the relevant handler
3. Add JS handler in `app.js` `setupSSE()` switch block
4. Update SSE Events table above

**Adding a new setting:**
1. Add field to `Config` struct in `internal/config/config.go` with getter/setter
2. Add `envStr`/`envBool`/`envDuration` call in `Load()`
3. Add to `Values()` map for settings display
4. Add API handler in `internal/web/api_settings.go`
5. Add UI control in `settings.html`
6. Update env var table above

**Dev deploy to test server:**
```bash
make dev-deploy
```
