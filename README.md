# Docker-Sentinel — Dev Testing Branch

[![Dev Build](https://github.com/Will-Luck/Docker-Sentinel/actions/workflows/dev-testing.yml/badge.svg?branch=dev-testing)](https://github.com/Will-Luck/Docker-Sentinel/actions/workflows/dev-testing.yml)

This branch contains features and fixes that are being tested before merging into the stable release. If you'd like to help test, pull the dev image:

```bash
docker pull ghcr.io/will-luck/docker-sentinel:dev
```

For the stable release, see the [`main` branch](https://github.com/Will-Luck/Docker-Sentinel/tree/main).

---

## Implemented Features

### Update Engine
- **Pre-pull validation** — pulls the new image before stopping the container, so containers are never left down if a registry pull fails
- **Update delay / cooldown** — only update to images N+ days old via label, avoiding day-zero breakage
- **Per-container grace period label** — `sentinel.grace-period=60s` to override the global grace period per container (max 1 hour)
- **Semver constraint pinning** — `sentinel.semver-constraint=^2.0.0` label to restrict tag discovery to a semver range
- **Semver constraint labels** — `sentinel.semver=minor` to restrict updates to patch-only or minor-only
- **Tag include/exclude regex** — user-configurable regex patterns to filter valid update targets per container
- **Per-container cron schedule** — different containers update on different schedules via labels
- **Pull-only mode** — pull new images without restarting containers, for planned maintenance windows
- **Image backup/retag** — tag the previous image before pulling a new one, with configurable expiry
- **Include stopped containers** — monitor and optionally update stopped/exited containers
- **Remove anonymous volumes on update** — optionally remove anonymous volumes when recreating
- **Parallel registry checks** — configurable concurrency for registry API calls during scans
- **Update maintenance windows** — time-range expressions gating when auto-policy updates are applied
- **Dependency-aware updates** — topological sort ensures dependencies update first
- **Docker Compose file sync** — edit docker-compose.yml tag references in-place and run `compose up -d`
- **Skip updating stopped containers** — do not pull or recreate containers in a stopped state

### Web Dashboard
- **Dedicated images management page** — `/images` page listing all Docker images with repo tags, size, creation date, in-use status, prune and remove
- **Container log viewer** — quick tail of container logs with line count selector (50/100/200/500)
- **Changelog / release notes in queue** — fetch release notes via OCI labels and display in the approval queue and container detail
- **GitHub release notes in UI** — inline display of release notes from GitHub/Gitea Releases API
- **Watch all tags mode** — display all available tags for an image in the UI
- **Mobile-responsive UI** — fully responsive layout for phone and tablet screens
- **Container grouping by stack** — visual grouping by compose project with drag-and-drop reordering
- **Dry-run / simulation mode** — show what updates would happen without applying them
- **History export (CSV/JSON)** — download update history for audit trails
- **Configuration export / import** — full settings backup and restore with merge semantics via GUI
- **Multiple release sources** — track primary + secondary repos for release info
- **Portainer API integration** — trigger updates through Portainer's API for Portainer-managed stacks

### Notifications
- **Email / SMTP provider** — full SMTP config (host, port, TLS, from/to, credentials)
- **MQTT provider** — publish update events to an MQTT broker for home automation
- **Home Assistant MQTT discovery** — MQTT discovery entities so HASS shows native update badges per container
- **Apprise backend** — integration with Apprise for 100+ notification services
- **Customisable notification templates** — Go `text/template` engine with per-event-type overrides, preview, and reset-to-default
- **Notification snooze / dedup timer** — suppress repeat notifications for the same container+version
- **Markdown-formatted notifications** — Markdown bodies for providers that support it (Telegram, ntfy)

### Security & Auth
- **OIDC / SSO authentication** — login via Authelia, Authentik, Keycloak etc. with auto-create users and default roles
- **TOTP / 2FA** — time-based one-time passwords as a second factor, QR enrolment, recovery codes
- **Docker socket proxy support** — `DOCKER_HOST=tcp://socket-proxy:port` with TLS certificate configuration
- **Inbound webhook trigger** — `POST /api/webhook` for CI/CD pipelines to push update signals after image builds (Docker Hub, GHCR, generic JSON payloads)

### Observability
- **Grafana dashboard template** — official Grafana dashboard JSON for Sentinel's Prometheus metrics
- **Prometheus textfile collector** — write metrics as a textfile for node_exporter

---

## Bug Fixes (this branch only)

- **Auth disabled state no longer locks out the GUI** ([#7](https://github.com/Will-Luck/Docker-Sentinel/issues/7)) — Security tab stays visible when auth is off. The auth toggle is always accessible, other sections are greyed out. "Auth: Off" badge is now a clickable link to the Security tab. Improved confirmation dialog and setup wizard hint.

---

## Known Issues

- **Swarm update timeouts** ([#42](https://github.com/Will-Luck/Docker-Sentinel/issues/42)) — Multiple swarm service updates can time out when applied in bulk

## Backburner

These are high-impact but need significant design work:

- **Vulnerability scanning** — Scan images with Trivy/Grype before update, block if new CVEs found
- **AI release note summary** — Ollama integration for breaking change detection in release notes
- **Image signing verification** — Verify cosign/Notary signatures before allowing updates
- **Automatic non-breaking updates** — Use local LLM to classify updates and auto-apply only safe ones

---

## Reporting Issues

If you find any issues with this dev build, please [open an issue](https://github.com/Will-Luck/Docker-Sentinel/issues/new) or start a [discussion](https://github.com/Will-Luck/Docker-Sentinel/discussions).
