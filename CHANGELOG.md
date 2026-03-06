# Changelog

All notable changes to Docker-Sentinel are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
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

### Fixed
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
