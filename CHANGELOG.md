# Changelog

All notable changes to Docker-Sentinel are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/Will-Luck/Docker-Sentinel/compare/v2.8.0...HEAD
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
