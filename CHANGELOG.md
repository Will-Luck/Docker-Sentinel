# Changelog

All notable changes to Docker-Sentinel are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Multi-instance Portainer support.** Connect to multiple Portainer servers,
  each with per-endpoint enable/disable toggles. Local Docker socket endpoints
  are auto-detected and blocked to prevent duplicate monitoring.
- **Portainer containers on dashboard.** Portainer endpoint containers appear
  as host groups on the dashboard, with full policy, queue, severity, and
  maintenance support (same as cluster hosts).
- **Connectors page redesigned.** Portainer tab now shows instance cards with
  add/remove/test/configure workflow, replacing the single URL+token form.
- **Portainer self-update via portainer-updater.** Portainer containers detected
  by Sentinel can now be safely updated without crashing the Portainer API.
  Uses the official `portainer/portainer-updater` helper container, which mounts
  the Docker socket directly and survives the Portainer stop/recreate cycle.
  Works for both queue approvals and manual "update to version" actions.

### Fixed
- **Portainer runtime scanner creation.** Adding a Portainer instance via the
  UI now creates a live scanner immediately. Previously, instances added after
  boot had no scanner until the next restart.
- **Portainer TLS verification.** Portainer connections now skip TLS certificate
  verification, fixing failures with self-signed certs (standard in homelab and
  private network setups).
- **Connectors page CSRF token.** Fixed `csrfToken` function reference being
  passed as a header value instead of being called, which broke all fetch
  requests on the connectors page.
- **Local socket endpoints not blocked.** `IsLocalSocket()` was defined but
  never called. Now auto-blocks local Docker socket endpoints during Test
  Connection and excludes them from scanning.
- **Smart local socket blocking.** `unix://` endpoints are only auto-blocked
  when the Portainer instance runs on the same host as Sentinel. Previously
  all `unix://` endpoints were blocked, which incorrectly disabled remote
  Portainer instances whose Docker sockets are valid monitoring targets.
- **Runtime Portainer instances not scanned.** Portainer instances added via
  the UI after boot were saved to the store and had scanners for API calls,
  but were invisible to the scan engine. Now `ConnectInstance` registers
  with both the web layer and the engine, so runtime-added instances are
  scanned immediately. Endpoint config changes (enabled/blocked) are also
  synced to the engine without requiring a restart.
- **Queue approval for Portainer containers.** Approving a Portainer-managed
  container from the Pending Updates queue previously fell through to the
  local Docker daemon (which can't reach remote containers). Now routes
  through the Portainer API, with Portainer image detection for self-update.
- **NPM resolver cross-host port shadowing.** When multiple NPM proxy hosts
  forwarded the same port to different IPs (e.g. port 8080 on both .57 and
  .64), the resolver matched the lowest NPM ID regardless of which host the
  container actually ran on. Replaced the single `SENTINEL_HOST` string match
  with auto-detection of local network addresses via `net.InterfaceAddrs()`,
  hostname, and `host.docker.internal` DNS. `SENTINEL_HOST` is still honoured
  as an additive override for containerised deployments where bridge networking
  hides the host's LAN IP.
- **Failed approvals missing from history.** When an approved update failed
  (e.g. Portainer agent disconnected, network error), only a log line was
  written. The update vanished from the queue with no history record. Now
  records a "failed" entry with the error message and elapsed duration.
- **History page scan summary display.** Scan summary rows were rendered as
  regular container rows, causing truncated text in the Container column and
  broken `/container/history-0` URLs when clicked. Now render with colspan
  spanning Container + Version columns, non-clickable, with proper text
  wrapping and column alignment at all viewport widths.
- **Images page column alignment.** Size, Status, and Actions columns were
  left-aligned while their content (numbers, badges, buttons) sat off-centre.
  Size is now right-aligned, Status and Actions are centred. Unused badge
  changed from grey to red for better visibility.
- **Portainer connector: hot-reload without restart.** Saving Portainer URL
  and API token in the UI now takes effect immediately. Previously the test
  button always returned "not configured" because the provider was only
  created at startup. Uses the same factory pattern as the NPM connector.
- **Portainer connector: stale credentials after token change.** The connection
  test now always recreates the provider from current DB settings, so changing
  the URL or token and re-testing uses the new values instead of the ones
  from startup.
- **Portainer duplicate queue entries.** When a Portainer endpoint pointed at
  the same Docker socket Sentinel runs on, every container was scanned twice
  (once locally, once via Portainer), creating duplicate queue entries. The
  Portainer scan now collects local container IDs and skips any Portainer
  container whose Docker ID matches a locally-monitored container.
- **Portainer API token help text.** Corrected the path from "Settings > Users >
  Access tokens" to "My account > Access tokens".
- **Portainer container detail page 404.** Clicking a Portainer container in
  the pending updates queue returned "Container not found" because the detail
  handler only knew about local and cluster containers. Now resolves Portainer
  containers via the Portainer API.
- **Dashboard stat card count mismatch.** The "Updates Pending" stat card only
  counted local containers, while the nav badge counted all queue items
  including Portainer. Both now use the full queue length. Removed the
  redundant checkmark icon from the zero-state.
- **Filter bar missing bottom border.** The filter bar on multiple pages lacked
  a visual divider separating it from the table content below.
- **Remote container history/snapshot key mismatch.** History and snapshot
  lookups for cluster and Portainer containers used the plain container name
  instead of the scoped `hostID::name` key, returning empty results.
- **NPM port URLs for all containers.** NPM URL resolution matched every
  container against Sentinel's own domain when no `SENTINEL_HOST` was set,
  causing all containers to show the same proxy URL.
- **NPM wildcard domain resolution.** Proxy hosts with wildcard domains
  (e.g. `*.s3.garage.example.com`) produced broken URLs. Now skips wildcard
  entries and picks the first non-wildcard domain.

### Changed
- **Queue/history key format.** Portainer HostIDs changed from `portainer:N`
  to `portainer:instanceID:N`. Existing queue and history entries are
  automatically migrated on first boot.
- **Portainer settings storage.** Old flat settings (`portainer_url`,
  `portainer_token`, `portainer_enabled`) are migrated to a structured
  `portainer_instances` BoltDB bucket on first boot. The migration is
  idempotent and safe to re-run.
- **Portainer integration descriptions.** Updated the vague "view endpoints"
  description to accurately explain that Sentinel scans Portainer endpoints
  for updates, applies policies, and can redeploy stacks or update standalone
  containers via Portainer's API.

## [2.11.1] - 2026-03-10

### Fixed
- **Cluster agent: multi-network containers failed to recreate.** Docker API
  1.44+ (Engine 26.0) rejects container creation with multiple network
  endpoints. The agent was passing all networks in a single `CreateContainer`
  call, causing `"conflicting options: cannot attach network to more than 1
  network endpoint"` for any container on 2+ non-bridge networks. Now uses
  the same single-primary + `NetworkConnect` pattern as the standalone engine.
- **Cluster agent: BoltDB lock not released on gRPC stream failure.** After a
  successful self-update, the old container stop (which releases the BoltDB
  file lock) was conditional on the gRPC result send succeeding. If the stream
  broke during send, the new container could never open the database. The old
  container stop now uses `defer` and `context.Background()` to guarantee
  execution regardless of stream health.
- **Cluster agent: non-deterministic network skip in self-update.** The extra
  network connect loop re-iterated Go maps non-deterministically, potentially
  skipping the wrong network. Now uses a deterministic `networkPlan` that
  selects the primary once before `CreateContainer`.

## [2.11.0] - 2026-03-10

### Changed
- **Self-update rewritten: rename-before-replace pattern.** The old self-update
  spawned a helper container that ran a shell script to pull/stop/rm/run. This
  was fragile: the helper had to reconstruct all `docker run` flags, and the
  stop/rm/run sequence created a window where Sentinel was completely down.
  The new approach renames the running container out of the way, creates the
  replacement with the original name, and starts it. If anything fails, the
  rename is rolled back. The old container is stopped only after the new one
  is confirmed running and has reported success. This eliminates the helper
  container entirely, preserves all container configuration from the Docker
  inspect output, and reduces downtime from seconds to milliseconds. (#72)

### Fixed
- **Self-update left old container running (BoltDB lock):** After a successful
  rename-before-replace self-update, the old container kept running and held
  the BoltDB file lock on the shared data volume. The new container could not
  open the database. The old container is now stopped after the update result
  is sent, releasing the lock for the replacement. (#72)
- **Self-update failed for local/dev images:** Pull failures are now tolerated
  when the target image already exists locally, so self-updates work in
  environments without registry access.

## [2.10.3] - 2026-03-10

### Fixed
- **Dashboard action icons clipped off screen:** Removed the redundant row action
  icons (view details, check for updates, view logs) that overflowed the table on
  narrow viewports. Clicking the row already navigates to the container detail page
  where all actions are available. (#60)
- **"View Logs" link did not show logs:** Added URL hash fragment handling so
  navigating to `/container/{name}#logs` auto-opens the Container Logs accordion
  and scrolls it into view. Works for any accordion section (e.g. `#policy`,
  `#history`). (#60)

### Changed
- **Inline release notes in queue:** Pending updates now show the upstream release
  notes body inline below the changelog link, rendered in a scrollable pre-formatted
  block. Previously only a link was shown.

## [2.10.2] - 2026-03-10

### Fixed
- **Repeated "Image Identical" results for multi-arch images:** The digest
  equivalence cache in the image ID guard was storing a useless self-referential
  pair for mutable tags like `:latest`. Images like `eclipse-mosquitto` that
  have differing repo vs manifest list digests would trigger a pull on every
  scan cycle even though the image content never changed. The cache now stores
  the correct digest pair (`ImageDigest` vs `DistributionDigest`) so subsequent
  scans skip the false positive after one confirmation pull. (#63)

## [2.10.1] - 2026-03-09

### Changed
- **Clearer history outcome labels:** Renamed all outcome badges for clarity:
  `Success` → `Updated`, `Rollback` → `Rolled Back`, `No Change` → `Image Identical`,
  `Warning` → `Updated (partial)`, `Dry Run` → `Simulated`, `Pull Only` → `Pulled`.
  Split `Skipped` into `Rate Limited` and `Check Failed`.
- **Outcome tooltips:** Every history badge now has a hover tooltip explaining what
  the outcome means in plain English.
- **Scan summary row:** Each scan writes a summary line to history showing the full
  breakdown (checked, up to date, updated, queued, skipped, failed).
- **"Identical" filter pill:** New filter button on the history page to show/hide
  image-identical entries.

## [2.10.0] - 2026-03-09

### Added
- **Real-time container log streaming:** Live log tailing via SSE with Follow
  mode that survives container restarts and stream disconnects. Includes 30s
  heartbeat to survive nginx proxy timeouts.
- **Container control actions:** Stop, start, and restart buttons in the
  container detail logs toolbar.
- **Auto self-update mode:** New setting that triggers Sentinel self-update
  automatically after each scan when no other updates are in progress.
  Configurable as a global persistent setting in the UI.
- **OIDC group/role mapping:** Map IdP group claims to Sentinel roles so admins
  don't manually assign roles to OIDC users. Configurable group claim name,
  priority-based role resolution (admin > operator > viewer), and settings UI.
- **Notification batching:** Buffer rapid-fire notifications during bulk updates
  and send a single summary instead of N individual alerts. Configurable batch
  window duration (default: disabled for backwards compatibility).
- **Scheduled backups:** Automatic BoltDB backups on a cron schedule with
  configurable retention, optional S3-compatible export (stdlib HTTP + SigV4),
  and a manual trigger button in the UI.
- **Cloud registry auth (ECR/ACR/GCR):** Native authentication for AWS ECR,
  Azure ACR, and Google GCR/Artifact Registry with automatic token refresh and
  cached credentials. Pure HTTP implementations with no cloud SDK dependencies.
- **Compose file awareness:** Parse Docker Compose files to discover service
  relationships instead of requiring `sentinel.depends-on` labels. Auto-discovery
  from container labels, with label-based deps taking priority on conflict.
- **Image signature verification:** Verify image signatures via Cosign/Sigstore
  before deployment. Enforce, warn, or disable modes with per-container overrides
  via the `sentinel.verify` label.
- **Security scanning (Trivy):** Scan images for vulnerabilities before or after
  updates with configurable severity thresholds. Pre-update mode blocks deploys
  that exceed the threshold.
- **Styled confirmation dialogs:** All 8 bare `confirm()` calls replaced with
  `showConfirm()` modal, including a red danger variant for destructive actions.
- **Enhanced empty states:** Contextual SVG icons and call-to-action buttons on
  all empty-data pages (queue, history, cluster, images, portainer, logs).
- **`apiFetch()` utility:** Generalised fetch wrapper with automatic button
  loading spinners, JSON handling, and toast notifications. Replaces raw `fetch()`
  in images and queue modules.
- **Responsive hamburger navigation:** Collapsible mobile nav at 768px with
  hamburger toggle, Escape/outside-click dismiss, and aria-expanded support.
  Added to all 13 page templates.
- **Scan progress bar:** Real-time 3px accent bar below stat cards shows scan
  progress via new `scan_start` and `scan_progress` SSE events from the engine.
- **Container log viewer redesign:** Line-by-line rendering with level-based
  colouring (error/warn/info/debug), timestamp extraction, filter input, and
  auto-scroll toggle. Replaces raw `<pre>` block.
- **Cluster journal replay (Phase 6):** Offline journal entries from cluster
  agents are now persisted to the history store with full field mapping and
  enriched SSE events. Previously entries were logged but not recorded.
- **Scanner/verifier wiring:** Trivy vulnerability scanning and Cosign signature
  verification are now called during the update flow. Pre-update scan blocks
  deploys exceeding the severity threshold; enforce mode rejects unsigned images.
  Settings API endpoints and UI accordion sections added for configuration.
- **Queue CSV/JSON export:** Export pending updates from the queue page as CSV
  or JSON, matching the existing history export pattern.
- **Health check endpoints:** `/healthz` (liveness) and `/readyz` (readiness)
  probes for Kubernetes and load balancer integration. Readiness checks DB and
  Docker socket connectivity. Both endpoints skip authentication.
- **Queue keyboard shortcuts:** `j`/`k` navigation, `a`/`r`/`i` for
  approve/reject/ignore, Enter to toggle details, `?` for help overlay.
- **Atom feed for update history:** Subscribe to updates via Atom 1.0 feed at
  `/api/history/feed?token=xxx`. Authenticated via API token in query parameter.
  Auto-discovery link on the history page.
- **Bulk container actions:** Restart, stop, and start buttons in manage mode
  bulk bar. Sequential execution with 200ms stagger, progress counter, and
  summary toast on completion.
- **Notification retry with backoff:** Configurable retry (0-3 attempts) with
  exponential backoff for all notification providers. Initial backoff and max
  retries configurable in the Notifications settings tab.

### Changed
- **BoltDB nil-bucket safety:** All `tx.Bucket()` calls across the persistence
  layer (bolt.go, bolt_auth.go, bolt_hooks.go, bolt_registry.go, bolt_webauthn.go,
  custom_urls.go, notify.go) now check for nil before use, returning descriptive
  errors instead of panicking on missing or corrupted buckets.
- **Docker daemon startup validation:** `NewClient()` now pings the Docker daemon
  on init and returns a clear error if unreachable, instead of deferring failures
  to the first API call.
- **Notification dispatch timeouts:** `Stop()`, `flush()`, and `Reconfigure()`
  on `notify.Multi` now use a 30-second context timeout instead of
  `context.Background()`, preventing goroutines from hanging indefinitely during
  shutdown.

### Security
- **HTTP security headers:** Added middleware setting `X-Content-Type-Options`,
  `X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy`, and
  `Content-Security-Policy` on all responses.
- **Auth endpoint rate limiting:** Per-IP sliding window rate limiter (10 req/min)
  on login, setup, passkey, and TOTP verification endpoints.
- **Container name input validation:** All 28 API handlers that accept a container
  name from URL path parameters now validate the input against a strict allowlist
  (alphanumeric, underscore, dot, hyphen) before processing.

### Fixed
- **IsIdle() race condition:** Replaced racy TryLock-based idle check with an
  atomic counter that tracks in-progress updates
- **SSE log stream injection:** Escape newlines and carriage returns in log data
  before writing to SSE frames to prevent event injection
- **Self-update concurrent guard:** Prevent multiple simultaneous self-update
  goroutines with an atomic bool gate; re-queue on failure
- **Notification dispatch ignores context:** `dispatch()` now passes the caller's
  context to notifier `Send()` calls instead of always using `context.Background()`
- **EventSource leak on navigation:** Clean up SSE log stream connection when
  user navigates away from the page
- **Scroll listener accumulation:** Deduplicate scroll handler on log stream
  reconnect to prevent listener pile-up
- **Reconfigure() timer race:** Flush pending batch events and stop timer before
  swapping notifier chain to prevent dispatching to stale notifiers
- **Inconsistent ContainerNames in single events:** Single-event batch
  pass-through now populates `ContainerNames` for consumer consistency
- Queue page self-update button not working (#58)
- Throttle `UpdateLastSeen` BoltDB writes to every 5 minutes per agent instead
  of every 30-second heartbeat, reducing disk I/O ~10x at scale (#50)
- **Version severity badges:** Coloured MAJOR/MINOR/PATCH/BUILD chips next to
  version arrows on dashboard, queue, and history pages. Uses `registry.ParseSemVer`
  with OCI label fallback for non-semver tags like "latest".
- **Row quick actions:** Detail, check, and logs icon buttons per container row,
  visible on hover (always visible on mobile touch devices).
- **History page version column:** Shows old-to-new version transition with
  severity badge. JS-side semver parser mirrors the Go implementation.
- **Settings "Show Advanced" toggle:** Hides expert settings (cluster, Portainer,
  scan concurrency, maintenance windows, OIDC, lifecycle hooks) by default.
  Persisted in localStorage.
- **System health indicators:** Docker and database health dots in the About tab.
  Nav bar health dot fetches `/readyz` on page load.
- **Dashboard keyboard shortcuts:** `m` (toggle manage mode), `s` (trigger scan),
  `?` (show help overlay).
- **"All up to date" celebration state:** Checkmark icon replaces "0" on the
  Updates Pending stat card when no updates are pending.
- **Row update flash animation:** Green highlight fades out over 500ms when SSE
  patches a container row.

### Fixed
- Severity badge empty for containers with non-semver tags (e.g. "latest") that
  have pending semver updates. Now falls back to the OCI
  `org.opencontainers.image.version` label.
- Port chips vanishing on SSE row refresh. `handleContainerRow` local path was
  missing `Ports` and `HostAddress` fields.
- Container detail page missing `Severity`, `HasUpdate`, and `DigestOnly` fields
  for both local and remote containers.
- Remote container detail `HasUpdate` not detecting digest-only updates. Now
  checks queue membership instead of `newestVersion != ""`.

## [2.8.0] - 2026-03-03

### Added
- NPM (Nginx Proxy Manager) integration with automatic proxy host resolution
- Per-port custom URL overrides for containers
- Clickable port links on dashboard with vertical stacking and +N collapse
- Configurable dashboard columns (show/hide ports, status, policy, etc.)
- Port mappings for remote cluster containers

### Changed
- Dashboard table redesign: removed Actions column, made status badges clickable
- Centre-aligned policy, status, and ports columns

### Fixed
- Queue CSS dead selectors and stale settings.html text
- Port chip alignment with column header
- NPM hot-init, flexBool deserialisation, host matching
- Private IP validation for NPM/Portainer URLs
- SSE reload prevention on form pages
- 40 code review issues resolved (#10-#57)

## [2.6.0] - 2026-03-02

### Added
- Version scope setting (relaxed/strict) for update filtering

### Fixed
- Default version scope to strict (issue #48)
- Per-row removal in bulk queue actions without page reload (#47)

## [2.5.1] - 2026-03-01

### Fixed
- Multi-arch digest mismatch causing no-op updates (#45)

## [2.5.0] - 2026-02-28

### Added
- Auto-update setting for remote Sentinel agents
- Remote containers in dependency graph
- GHCR switch for remote containers
- Host-scoped hook storage for remote containers
- Host-scoped notification preferences for remote containers
- Rollback for remote containers via update history lookup
- Remote container log streaming via cluster gRPC

### Fixed
- Local container updates now pull the target version, not the current tag
- Auto-filter to Unused when entering Images manage mode (#9)
- Images page column widths, manage mode checkboxes, vertical alignment (#9)
- UI bugs across Images, Logs, History, and Settings pages (#8)
- SSE auto-refresh for remote container updates
- Agent pushes fresh container list before update result
- ImageDigest population in agent container listings
- Rate limit check skips registry instead of stopping entire scan
- Stale gRPC goroutine after stream replacement
- 14 bugs from code review audit

## [2.4.0] - 2026-02-23

### Added
- Tag precision scope for constrained version comparison
- Parts field on SemVer for tag precision tracking

## [2.3.5] - 2026-02-21

### Fixed
- Fall back to registry digest for Swarm services without pinned digest

## [2.3.4] - 2026-02-21

### Fixed
- Minor stability fixes

## [2.3.3] - 2026-02-21

### Fixed
- Minor stability fixes

## [2.3.2] - 2026-02-21

### Fixed
- Minor stability fixes

## [2.3.1] - 2026-02-21

### Fixed
- Minor stability fixes

## [2.3.0] - 2026-02-20

### Added
- System Settings accordion in General tab
- Portainer-style enrollment snippets and auto-enrollment
- First-run setup wizard with time-limited setup window

### Fixed
- BoltDB deadlock, double-escape, lint issues
- Deduplicate local host tab when cluster mode is enabled
- Skip wizard when upgrading from older version with existing users
- Template parsing restricted to required files only
- Security and correctness fixes from code review

## [2.2.0] - 2026-02-19

### Added
- Multi-host agent architecture (cluster mode)
- Cluster management UI with settings tab, host cards, and dashboard host groups
- Embedded git commit hash in version string

### Fixed
- Cluster-aware policy resolution, registry dropdown, bulk policy
- 6 multi-host agent bugs found during acceptance testing
- Registry UX, Swarm sort, stat sync (issues #30, #31)

## [2.1.0] - 2026-02-16

### Added
- Docker Swarm service support with monitoring and updates
- Rollback policies (automatic, manual, disabled)
- Resolved version display for non-semver tags on dashboard

### Fixed
- SSE service row updates now match full page render
- Paginate GHCR tag listing for large repos
- Filter calver tags from semver version comparison
- Caching, validation, and safety issues from code review

## [2.0.2] - 2026-02-16

### Changed
- Replace setup token with 5-minute time window

### Fixed
- Show setup token field when URL param is missing

## [2.0.1] - 2026-02-15

### Added
- User column in activity log

### Fixed
- Settings toggles not persisting across page reloads

### Changed
- Split large files for navigability

## [2.0.0] - 2026-02-15

### Added
- Web dashboard with htmx and embedded HTML
- Authentication system (username/password, WebAuthn passkeys, OIDC SSO, TOTP 2FA)
- Role-based access control (admin, operator, viewer)
- Notification system with 10 providers (Gotify, Slack, Discord, Telegram, ntfy,
  Pushover, webhook, SMTP, Apprise, MQTT)
- Home Assistant MQTT discovery
- Notification templates with per-event customisation
- Per-container notification preferences and digest scheduling
- Registry credential management with rate limit tracking
- GHCR alternative detection for Docker Hub images
- Image cleanup after updates
- Cron scheduling (in addition to interval-based polling)
- Prometheus metrics endpoint
- Lifecycle hooks (pre/post-update, rollback)
- Dependency-aware update ordering
- BoltDB persistence for all state
- Container snapshot and rollback
- Manual approval queue with bulk actions
- Activity logging
- Config export/import
- Self-update mechanism
- Image management page (list, prune, delete)
- Grafana dashboard template

### Changed
- Complete rewrite from Docker-Guardian (shell script) to Go
- Modular architecture with clean package boundaries

[Unreleased]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.10.1...HEAD
[2.10.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.10.0...v2.10.1
[2.10.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.9.1...v2.10.0
[2.9.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.9.0...v2.9.1
[2.9.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.8.0...v2.9.0
[2.8.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.6.0...v2.8.0
[2.6.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.5.1...v2.6.0
[2.5.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.5.0...v2.5.1
[2.5.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.4.0...v2.5.0
[2.4.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.3.5...v2.4.0
[2.3.5]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.3.4...v2.3.5
[2.3.4]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.3.3...v2.3.4
[2.3.3]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.3.2...v2.3.3
[2.3.2]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.3.1...v2.3.2
[2.3.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.3.0...v2.3.1
[2.3.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.2.0...v2.3.0
[2.2.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.0.2...v2.1.0
[2.0.2]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.0.1...v2.0.2
[2.0.1]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/Will-Luck/Docker-Sentinel/releases/tag/v2.0.0
