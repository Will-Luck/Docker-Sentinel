# Issue 59 Completion: Remaining Nice-to-Have Features

**Date:** 2026-03-09
**Issue:** GiteaLN/Docker-Sentinel#59
**Goal:** Close out the feature wishlist by implementing the 6 remaining items.

## Features

### 1. Queue CSV/JSON Export (Trivial)

Clone the `apiHistoryExport` pattern.

- **Route:** `GET /api/queue/export?format=csv|json`
- **Handler:** `apiQueueExport` in `api_queue.go`
- **CSV columns:** Container, Current Image, New Image, Detected, Versions Available
- **JSON:** Same `[]PendingUpdate` with `Content-Disposition: attachment` header
- **UI:** Export dropdown button on queue page matching history page pattern

### 2. Health Check Endpoints (Small)

Two unauthenticated endpoints following Kubernetes conventions.

- **`GET /healthz`** — Liveness. Returns `200 {"status": "ok"}`. No dependency checks.
- **`GET /readyz`** — Readiness. Checks DB accessible + Docker socket connected.
  Returns `200` if all pass, `503` with component statuses if any fail.
- **Auth:** Both skip auth middleware (load balancers can't authenticate).
- **Location:** `api_health.go`, routes in pre-auth section of `registerRoutes()`.

Response format:
```json
{"status": "ready", "checks": {"db": "ok", "docker": "ok"}}
{"status": "not_ready", "checks": {"db": "ok", "docker": "error: connection refused"}}
```

### 3. Keyboard Shortcuts for Queue (Small)

Page-scoped `keydown` listener in `queue.js`.

- **Navigation:** `j`/`k` = next/previous row. `.kb-focused` CSS class on `tr.container-row`.
- **Actions:** `a`/`r`/`i` = approve/reject/ignore focused row. Calls existing functions.
- **Enter/Space:** Toggle accordion on focused row.
- **`?`:** Toggle shortcuts help overlay.
- **Guard:** Inactive when modal/input is focused.
- **Scope:** Queue page only, listener in `initQueuePage()`.

No backend changes.

### 4. RSS/Atom Feed for History (Small)

Atom 1.0 feed with API token auth.

- **Route:** `GET /api/history/feed?token=abc123`
- **Auth:** Validate token against existing API tokens in auth store (query param
  since feed readers can't send Bearer headers).
- **Content:** Last 50 updates. Entry title = "Updated {container}: {old} to {new}",
  content = outcome + duration + error.
- **Feed metadata:** Title = "Docker Sentinel Updates", self-link, updated timestamp.
- **Auto-discovery:** `<link rel="alternate" type="application/atom+xml">` on history page.
- **Implementation:** `encoding/xml` templates, no external dependencies.

### 5. Bulk Container Actions (Medium)

Extend manage mode with restart/stop/start via staggered individual API calls.

- **UI:** Three buttons added to `#bulk-bar`: Restart, Stop, Start.
- **Confirmation:** `showConfirm()` danger modal with container name list.
- **Execution:** Sequential calls to individual `/api/containers/{name}/{action}`
  with 200ms delay between each (avoids Docker daemon hammering).
- **Progress:** Counter in bulk bar: "Restarting 2/4..."
- **Error handling:** Collect failures, summary toast: "3 succeeded, 1 failed: name (error)"
- **No new backend endpoints.** Frontend stagger gives real-time progress feedback.

### 6. Webhook Retry with Backoff (Medium)

Configurable retry at `Multi.dispatch()` level wrapping `Notifier.Send()`.

- **Settings:**
  - `notification_retry_count`: 0 (disabled, default), 1, 2, 3
  - `notification_retry_backoff`: initial backoff duration (default: 2s)
  - Max backoff capped at 30s
- **Implementation:** `retrySend()` in `notifier.go`:
  - Loop `0..retryCount`, call `Send()`, on error sleep `backoff * 2^attempt + jitter`
  - Respect context cancellation
- **Scope:** All providers equally (any HTTP provider can have transient failures).
- **Concurrency:** Each provider runs in its own goroutine in `dispatch()`, so retries
  for one slow provider don't block others.
- **Settings UI:** "Retry" section in Notifications settings tab.
- **No per-provider config** (YAGNI).

## Execution Strategy

Three parallel groups based on dependencies:

```
Group 1 (trivial + small, all independent):
  [A] Queue export + Health endpoints
  [B] Keyboard shortcuts
  [C] Atom feed

Group 2 (medium, independent of each other but after Group 1 for clean context):
  [D] Bulk container actions
  [E] Webhook retry

Final: make lint && make test && deploy to .57
```
