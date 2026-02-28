# Design: Docker-Sentinel Full Modularisation

**Date:** 2026-02-24
**Status:** Approved
**Scope:** Split `app.js` (5,207 lines), `style.css` (4,215 lines), and 4 oversized Go files into smaller, navigable modules.

## Motivation

The codebase has several oversized files that slow down navigation and debugging. The worst offender is `app.js` — a single 5,200-line monolith loaded by every HTML page. This modularisation introduces esbuild as a bundler for JS/CSS and splits 4 Go files along existing logical boundaries.

No logic changes. No refactoring. Pure structural reorganisation.

## Build System: esbuild

- **Binary:** Downloaded once by Makefile (like `golangci-lint`), no `package.json` or `node_modules`
- **Source:** `internal/web/static/src/js/` and `internal/web/static/src/css/`
- **Output:** `internal/web/static/app.js` and `internal/web/static/style.css` (committed to repo)
- **Targets:** `make frontend` (or `make js` + `make css`), `make build` depends on it
- **Output is committed** so `go install` and Docker builds work without esbuild installed
- HTML templates, `embed.FS`, and server code are unchanged

## JS Modules (12 files)

Source: `internal/web/static/src/js/`

| Module | ~Lines | Contents |
|--------|--------|----------|
| `csrf.js` | 40 | Fetch patch, 401 redirect |
| `utils.js` | 150 | `escapeHTML`, `showConfirm`, toast system, `apiPost` |
| `settings-core.js` | 350 | Poll interval, grace period, toggles, filters, all `set*` functions |
| `settings-cluster.js` | 360 | `loadClusterSettings`, cluster toggle, save |
| `dashboard.js` | 400 | Tabs, stats, filter/sort, manage mode, drag reorder, pause banner, last scan |
| `queue.js` | 500 | Approve/reject/ignore, bulk actions, `triggerUpdate`, `triggerCheck`, policy changes, multi-select |
| `swarm.js` | 320 | Service toggle/update/rollback/scale, `refreshServiceRow` |
| `sse.js` | 400 | SSE connection, live row patching, connection status, digest banner |
| `notifications.js` | 430 | Channel CRUD, provider fields, test, digest settings, per-container prefs |
| `registries.js` | 400 | Credential CRUD, rate limits, GHCR alternatives/badges |
| `about.js` | 320 | About info, release sources CRUD |
| `main.js` | 160 | `DOMContentLoaded` entry point — imports all modules, wires initialisation |

`main.js` is the esbuild entry point. Each module exports its public functions. Shared state passed via init functions, not globals.

## CSS Modules (11 files)

Source: `internal/web/static/src/css/`

| Module | ~Lines | Contents |
|--------|--------|----------|
| `variables.css` | 150 | CSS custom properties, primitives, semantic tokens, shadows, spacing |
| `base.css` | 200 | Reset, typography, nav bar, main layout, breadcrumbs |
| `components.css` | 600 | Cards, tables, badges, buttons, modals, toasts, empty states, focus, transitions |
| `dashboard.css` | 500 | Stack groups, swarm sections, container rows, status badges, manage mode, drag, animations |
| `queue.css` | 200 | Accordion panels, bulk action bar, policy select pills |
| `settings.css` | 800 | Settings page, toggle switches, collapsibles, duration picker, setting rows |
| `notifications.css` | 400 | Digest banner, event filter pills, notification prefs, radio cards |
| `registries.css` | 150 | Registry display, rate limits, GHCR badges, source badges |
| `cluster.css` | 300 | Cluster page, host groups, host cards, service detail page |
| `auth.css` | 550 | Login page, nav user dropdown |
| `responsive.css` | 100 | Media queries |

`variables.css` imported first (entry point), then all others in specificity order.

## Go File Splits (4 moves)

| Current File | Extract To | What Moves |
|-------------|-----------|------------|
| `server.go` (1,150) | `interfaces.go` (~440 lines) | All 30+ interface definitions |
| `handlers.go` (1,407) | `handlers_dashboard.go` (~500 lines) | `handleDashboard`, view types (`pageData`, `tabStats`, `hostGroup`, `containerView`, `stackGroup`, `serviceView`, `taskView`, `buildServiceView`) |
| `api_settings.go` (1,024) | `api_portainer.go` (existing, 52 lines) | 4 Portainer handlers (`apiSetPortainerEnabled`, `apiSetPortainerURL`, `apiSetPortainerToken`, `apiTestPortainerConnection`) |
| `updater.go` (1,486) | `updater_remote.go` (~500 lines) | `scanRemoteHosts`, `scanRemoteHost`, `scanPortainerEndpoints`, `scanPortainerEndpoint`, `checkGHCRAlternatives` |

Pure file moves. No logic changes, no new abstractions.

## What doesn't change

- HTML templates (still load one `app.js` and one `style.css`)
- `embed.FS` embedding
- Server serving logic (`serveJS`, `serveCSS`)
- Docker build (adds `make frontend` before `make build`)
- CI (adds `make frontend` step)

## Risk

- **Go:** Near-zero. Compiler catches all broken references.
- **JS/CSS:** Low. esbuild flags broken imports. Bundled output is functionally identical to original. Missed function references show up immediately in browser console.
- **Build:** Bundled output is committed, so downstream consumers (`go install`, Docker) are unaffected.
