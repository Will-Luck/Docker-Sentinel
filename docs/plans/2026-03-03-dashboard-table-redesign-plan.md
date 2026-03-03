# Dashboard Table Redesign Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Remove the redundant Actions column from the dashboard table, make status badges clickable for update actions, left-align Policy/Status columns, and rebalance column widths to fix the recurring alignment issues (#58).

**Architecture:** The Actions column (220px) is removed entirely since row-click already navigates to container details. Update/self-update triggers move into the Status cell as clickable badges. Column widths rebalance to give breathing room to Image and Ports columns. Policy and Status columns switch from centre-aligned to left-aligned per enterprise data table best practices.

**Tech Stack:** Go templates (index.html), vanilla CSS (dashboard.css, responsive.css), vanilla JS (queue.js, sse.js), esbuild (make frontend)

**Design doc:** `docs/plans/2026-03-03-dashboard-table-redesign.md`

---

### Task 1: Update ColCount and colgroup (Go + HTML template)

**Files:**
- Modify: `internal/web/handlers_dashboard.go:547` (ColCount calculation)
- Modify: `internal/web/static/index.html:133-141` (colgroup)
- Modify: `internal/web/static/index.html:160` (Actions th)

**Step 1: Change ColCount from 7 to 6**

In `internal/web/handlers_dashboard.go`, line 547, change:
```go
colCount := 7 // checkbox + name + image + policy + status + ports + actions
```
to:
```go
colCount := 6 // checkbox + name + image + policy + status + ports
```

**Step 2: Remove the Actions `<col>` and `<th>`, rebalance widths**

In `internal/web/static/index.html`, replace lines 133-141 (the colgroup) with:
```html
                    <colgroup>
                        <col style="width:40px">
                        <col style="width:20%">
                        {{if index .ColumnVisible "image"}}<col class="col-image">{{end}}
                        {{if index .ColumnVisible "policy"}}<col class="col-policy" style="width:110px">{{end}}
                        {{if index .ColumnVisible "status"}}<col class="col-status" style="width:150px">{{end}}
                        {{if index .ColumnVisible "ports"}}<col class="col-ports" style="width:160px">{{end}}
                    </colgroup>
```

Remove line 160 (`<th>Actions</th>`) entirely.

**Step 3: Build and verify template parses**

Run: `cd /home/lns/Docker-Sentinel && go build ./...`
Expected: builds without error (template is embedded at compile time)

**Step 4: Commit**

```bash
git add internal/web/handlers_dashboard.go internal/web/static/index.html
git commit -m "refactor: remove Actions column, rebalance colgroup widths (#58)"
```

---

### Task 2: Remove Actions `<td>` from container row template

**Files:**
- Modify: `internal/web/static/index.html:449-471` (container row Actions td)

**Step 1: Delete the Actions `<td>` block**

Remove lines 449-471 from the `{{define "container-row"}}` template. These lines are the entire Actions cell:
```html
                                <td>
                                    <div class="btn-group">
                                        {{if .IsSelf}}
                                            ...
                                        {{end}}
                                    </div>
                                </td>
```

The template should end with the Ports `<td>` (line 448) followed directly by `</tr>`.

**Step 2: Make status badges clickable for update actions**

Replace the Status `<td>` block (lines 423-447) with this version that makes update badges into clickable buttons:

```html
                                <td class="col-status">
                                    {{if .Maintenance}}
                                        <span class="badge badge-warning badge-updating">Updating</span>
                                    {{else if and .HasUpdate (not .IsSelf) (ne .Policy "pinned")}}
                                        <span class="badge badge-warning badge-action" onclick="event.stopPropagation(); triggerUpdate('{{.Name}}', event, '{{.HostID}}')" role="button" tabindex="0">Update</span>
                                    {{else if .IsSelf}}
                                        {{if and .HasUpdate (eq .HostID "")}}
                                        <span class="badge badge-info badge-action" onclick="event.stopPropagation(); triggerSelfUpdate(event)" role="button" tabindex="0">Update Sentinel</span>
                                        {{else}}
                                        <span class="badge badge-success">Running</span>
                                        {{end}}
                                    {{else if eq .State "running"}}
                                        <span class="status-badge-wrap" data-name="{{.Name}}" data-action="stop" data-host-id="{{.HostID}}">
                                            <span class="badge badge-success badge-default">Running</span>
                                            <span class="badge badge-error badge-hover">Stop</span>
                                        </span>
                                    {{else}}
                                        <span class="status-badge-wrap" data-name="{{.Name}}" data-action="start" data-host-id="{{.HostID}}">
                                            <span class="badge badge-error badge-default">{{.State}}</span>
                                            <span class="badge badge-success badge-hover">Start</span>
                                        </span>
                                    {{end}}
                                </td>
```

Key changes:
- "Update Available" becomes "Update" with `onclick` calling `triggerUpdate()`
- "Update Ready" (self) becomes "Update Sentinel" with `onclick` calling `triggerSelfUpdate()`
- `badge-action` class added for clickable badge styling
- `role="button"` and `tabindex="0"` for accessibility
- `event.stopPropagation()` prevents row click navigation
- Pinned containers with updates show their running/stopped state normally (no update badge)
- The order of conditions matters: maintenance first, then update check, then self check

**Step 3: Build and verify**

Run: `cd /home/lns/Docker-Sentinel && go build ./...`
Expected: builds without error

**Step 4: Commit**

```bash
git add internal/web/static/index.html
git commit -m "feat: move update actions into clickable status badges (#58)"
```

---

### Task 3: Remove Actions `<td>` from swarm service rows

**Files:**
- Modify: `internal/web/static/index.html:295-305` (swarm service Actions td)
- Modify: `internal/web/static/index.html:281-293` (swarm service Status td)

**Step 1: Delete the swarm service Actions `<td>`**

Remove lines 295-305 (the Actions cell for swarm services):
```html
                            <td>
                                <div class="btn-group">
                                {{if and .HasUpdate (ne .Policy "pinned")}}
                                    <button class="btn btn-warning btn-sm" ...>Update</button>
                                {{end}}
                                    <a href="/service/{{.Name}}" class="btn btn-sm" ...>Details</a>
                                </div>
                            </td>
```

**Step 2: Add update action to swarm service Status cell**

The swarm service Status cell (lines 281-293) currently shows replica badges with scale actions. Add an update badge before the replica status when `.HasUpdate` is true.

After the opening `<td class="col-status">`, add:
```html
                                    {{if and .HasUpdate (ne .Policy "pinned")}}
                                        <span class="badge badge-warning badge-action" onclick="event.stopPropagation(); triggerSvcUpdate('{{.Name}}', event)" role="button" tabindex="0" style="margin-bottom:4px">Update</span>
                                    {{end}}
```

This stacks the update badge above the replica count badge within the same cell.

**Step 3: Remove Actions `<td>` from swarm task rows**

Check line 325 (`<td></td>` — empty Actions cell in task rows) and remove it.

**Step 4: Build and verify**

Run: `cd /home/lns/Docker-Sentinel && go build ./...`
Expected: builds without error

**Step 5: Commit**

```bash
git add internal/web/static/index.html
git commit -m "feat: move swarm service update action into status cell (#58)"
```

---

### Task 4: CSS changes — alignment, clickable badges, cleanup

**Files:**
- Modify: `internal/web/static/src/css/dashboard.css:561-565` (centre-align rule)
- Modify: `internal/web/static/src/css/dashboard.css` (add badge-action styles)
- Modify: `internal/web/static/src/css/responsive.css` (remove btn-group responsive rule if needed)

**Step 1: Left-align Policy and Status columns**

In `dashboard.css`, replace lines 561-565:
```css
/* Dashboard policy & status columns: centre-align (matches queue page). */
#container-table .col-policy,
#container-table .col-status {
    text-align: center;
}
```
with:
```css
/* Dashboard policy & status columns: left-align for scanability. */
#container-table .col-policy,
#container-table .col-status {
    text-align: left;
}
```

**Step 2: Add clickable badge styles**

Add after the existing `.badge` styles in `dashboard.css` (after the status-badge-wrap block, around line 745):

```css
/* Clickable update/action badges — cursor, hover glow, keyboard focus. */
.badge-action {
    cursor: pointer;
    transition: filter 150ms ease, box-shadow 150ms ease;
}

.badge-action:hover {
    filter: brightness(1.15);
    box-shadow: 0 0 0 3px color-mix(in srgb, currentColor 15%, transparent);
}

.badge-action:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
}
```

**Step 3: Remove the responsive btn-group rule if it only applied to Actions**

In `responsive.css`, check lines 136-138 (480px breakpoint):
```css
.btn-group { flex-direction: column; }
```
This may still be used by the card-header btn-group (Expand All / Collapse All / Manage buttons). Keep it if so. Only remove if it was exclusively for the Actions column.

**Step 4: Build frontend**

Run: `cd /home/lns/Docker-Sentinel && make frontend`
Expected: esbuild compiles without errors, `static/style.css` and `static/app.js` regenerated

**Step 5: Commit**

```bash
git add internal/web/static/src/css/dashboard.css internal/web/static/src/css/responsive.css internal/web/static/style.css internal/web/static/app.js
git commit -m "fix: left-align policy/status, add clickable badge styles (#58)"
```

---

### Task 5: JS changes — update loading state tracking

**Files:**
- Modify: `internal/web/static/src/js/queue.js:230-254` (triggerUpdate loading state)
- Modify: `internal/web/static/src/js/queue.js:317-330` (triggerSelfUpdate)
- Modify: `internal/web/static/src/js/sse.js:95-110` (SSE loading state tracking)

**Step 1: Update triggerUpdate to work with badge instead of button**

In `queue.js`, the `triggerUpdate` function (line 230) currently finds `event.target.closest(".btn")` and adds `.loading` class. Change it to find the badge instead:

Replace line 231:
```js
var btn = event && event.target ? event.target.closest(".btn") : null;
```
with:
```js
var btn = event && event.target ? event.target.closest(".badge-action") : null;
```

The rest of the loading logic (adding `.loading` class, tracking in `_updateLoadingBtns`, timeout cleanup) works the same since `.loading` is just a CSS class.

**Step 2: Update triggerSelfUpdate similarly**

In `queue.js`, line 318, change:
```js
var btn = event && event.target ? event.target.closest(".btn") : null;
```
to:
```js
var btn = event && event.target ? event.target.closest(".badge-action") : null;
```

**Step 3: Update SSE loading state tracker**

In `sse.js`, line 101, the SSE code looks for `.btn-warning.loading` to re-attach loading state after row replacement. Change:
```js
var updBtn = row ? row.querySelector(".btn-warning.loading") : null;
```
to:
```js
var updBtn = row ? row.querySelector(".badge-action.loading, .badge-updating") : null;
```

This matches either a badge-action in loading state (update triggered but not yet confirmed) or the server-rendered "Updating" badge (maintenance=true).

**Step 4: Add badge loading styles**

In `dashboard.css`, add after the badge-action block:

```css
.badge-action.loading {
    pointer-events: none;
    opacity: 0.7;
}

.badge-action.loading::after {
    content: "";
    display: inline-block;
    width: 10px;
    height: 10px;
    border: 1.5px solid currentColor;
    border-right-color: transparent;
    border-radius: 50%;
    animation: badgeSpin 800ms linear infinite;
    margin-left: 4px;
    vertical-align: middle;
}
```

**Step 5: Build frontend**

Run: `cd /home/lns/Docker-Sentinel && make frontend`

**Step 6: Commit**

```bash
git add internal/web/static/src/js/queue.js internal/web/static/src/js/sse.js internal/web/static/src/css/dashboard.css internal/web/static/style.css internal/web/static/app.js
git commit -m "fix: update JS loading state to use badge-action instead of btn (#58)"
```

---

### Task 6: Swarm JS update

**Files:**
- Modify: `internal/web/static/src/js/swarm.js:28-57` (triggerSvcUpdate)

**Step 1: Update triggerSvcUpdate to use badge**

In `swarm.js`, line 28-57, the `triggerSvcUpdate` function uses `event.target.closest(".btn")`. Change to:
```js
var btn = event && event.target ? event.target.closest(".badge-action") || event.target.closest(".btn") : null;
```

The fallback to `.btn` keeps backward compatibility if swarm update buttons are rendered differently.

**Step 2: Build and commit**

Run: `cd /home/lns/Docker-Sentinel && make frontend`

```bash
git add internal/web/static/src/js/swarm.js internal/web/static/app.js
git commit -m "fix: update swarm triggerSvcUpdate for badge-action (#58)"
```

---

### Task 7: Build, deploy to dev, and visually verify

**Files:**
- None (testing only)

**Step 1: Full build**

Run: `cd /home/lns/Docker-Sentinel && make frontend && go build ./cmd/sentinel`
Expected: clean build, no errors

**Step 2: Run tests**

Run: `cd /home/lns/Docker-Sentinel && go test ./...`
Expected: all tests pass

**Step 3: Run linter**

Run: `cd /home/lns/Docker-Sentinel && make lint`
Expected: no issues (gofmt + golangci-lint clean)

**Step 4: Build dev Docker image and deploy to sentinel-dev-test**

```bash
cd /home/lns/Docker-Sentinel
docker build -t docker-sentinel:dev-testing .
docker stop sentinel-dev-test && docker rm sentinel-dev-test
docker run -d --name sentinel-dev-test \
  -p 62852:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v sentinel-dev-data:/data \
  -e GOTIFY_URL=disabled \
  -l sentinel.self=true \
  docker-sentinel:dev-testing
```

**Step 5: Take screenshots and verify**

Use Playwright to navigate to `http://192.168.1.57:62852` and take screenshots. Verify:
- No Actions column visible
- Status badges are left-aligned
- Policy dropdowns are left-aligned
- "Update" badge appears for containers with pending updates (if any)
- Clicking a row navigates to container details
- Image column has more breathing room
- Ports column displays multi-port containers cleanly
- Stack expand/collapse works without column jitter
- Manage mode checkbox column still works

**Step 6: Commit any fixes found during visual testing**

---

### Task 8: Clean up — remove dead CSS and update column config

**Files:**
- Modify: `internal/web/static/src/js/dashboard.js:16-37` (applyColumnConfig if it references actions)
- Modify: `internal/web/static/src/css/dashboard.css` (remove any orphaned .btn-group styles specific to Actions column)

**Step 1: Check applyColumnConfig for any "actions" references**

The `applyColumnConfig()` function in `dashboard.js:27` defines `allCols = ["image", "policy", "status", "ports"]`. It does NOT include "actions", so no change needed here.

**Step 2: Search for any orphaned CSS targeting the old Actions column**

Grep for `btn-group` in dashboard.css and check if any rules were specific to the container-row Actions cell. The `.btn-group` class is also used in the card-header (Expand All / Collapse All / Manage), so only remove container-row-specific rules.

**Step 3: Build and commit**

```bash
cd /home/lns/Docker-Sentinel && make frontend
git add -A
git commit -m "chore: clean up orphaned Actions column styles (#58)"
```
