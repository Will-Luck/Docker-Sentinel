# Dashboard Table Redesign

**Date:** 2026-03-03
**Issue:** https://git.lucknet.uk/GiteaLN/Docker-Sentinel/issues/58
**Status:** Design approved, pending implementation

## Problem

The dashboard container table has recurring alignment issues caused by cramming 7 columns into a fixed-width table (`table-layout: fixed` with pixel widths). Every fix to one column's width cascades into adjacent columns. The root cause is an oversized Actions column (220px) that shows a redundant "Details" button 95% of the time, since clicking the row already navigates to container details.

## Design Decisions

### 1. Remove the Actions column entirely

The "Details" button duplicates the row-click behaviour. Removing the entire column frees 220px and eliminates the primary source of horizontal pressure.

**Action migration:**

| Current Actions content | New location |
|---|---|
| "Details" link | Removed (row click handles this) |
| "Update" button | Clickable badge in Status cell |
| "Update Sentinel" button | Clickable badge in Status cell |
| "Updating..." spinner | Already in Status cell as badge |

### 2. Status cell becomes actionable

The Status cell already shows state badges. With this change, update-related badges become clickable action triggers:

| Container state | Badge shown | Interaction |
|---|---|---|
| Running, no update | `[Running]` green | Hover swaps to `[Stop]` red |
| Stopped/Exited | `[exited]` red | Hover swaps to `[Start]` green |
| Update available | `[Update]` orange | Click triggers update |
| Self with update | `[Update Sentinel]` blue | Click triggers self-update |
| Updating | `[Updating...]` orange spinner | Non-interactive |
| Self, no update | `[Running]` green | No hover action (read-only) |

Key: "Update Available" shortens to "Update" since the badge is now a call-to-action, not just informational.

When an update is available, the running/stop hover swap is replaced by the clickable Update badge. Users can still stop a container with a pending update via the container detail page.

### 3. Column width rebalancing

```
Before (7 cols):  [40px] [22%] [auto] [120px] [140px] [140px] [220px]
After  (6 cols):  [40px] [20%] [auto] [110px] [150px] [160px]
                   chk    name  image  policy  status  ports
```

- Image (auto-fill) gains the most space (~150px on a 1900px viewport)
- Status bumps from 140px to 150px for clickable badges
- Ports bumps from 140px to 160px for better multi-port stacking
- Name trims slightly from 22% to 20%
- Policy trims from 120px to 110px

### 4. Left-align Policy and Status columns

Per enterprise data table best practices: centre-alignment "prevents quick scanning and noticing irregularities." Both columns switch to `text-align: left` for a consistent left edge that the eye can follow down the table.

### 5. ColCount adjustment

`{{$.ColCount}}` decreases by 1 everywhere it's used (stack headers, host headers, empty state, swarm section dividers). This is a Go template variable set server-side.

## What stays the same

- `table-layout: fixed` with `<colgroup>` (prevents column jitter during expand/collapse)
- All CSS animations (badge pulse, spinner, chevron rotation, slide transitions)
- Manage mode (checkbox column, drag handles, bulk actions)
- Port display (chips, +N more toggle, expand on click)
- Column visibility settings (hide/show image, policy, status, ports)
- Responsive breakpoints (hide image on tablet, hide policy on phone)
- Row hover and click behaviour (row click = navigate to detail)
- Status hover swap for Running/Stop and Stopped/Start (when no update pending)
- Swarm service replica scaling badges (healthy/degraded/down with scale controls)

## Scope

### In scope
- Remove Actions column from HTML template, CSS, and JS
- Move update triggers into Status cell
- Rebalance column widths in `<colgroup>`
- Left-align Policy and Status columns
- Update `ColCount` in Go server code
- Update responsive breakpoints if needed
- Update any SSE row-update JS that references the Actions cell

### Out of scope (future improvements)
- Row density control (condensed/regular/relaxed)
- Column resize handles
- Sticky Name column for horizontal scroll
- Column reorder via drag

## Files affected

### Go (server-side)
- `internal/web/server.go` or wherever `ColCount` is computed
- `internal/web/static/index.html` (main template)

### CSS
- `internal/web/static/src/css/dashboard.css` (alignment, badge clickability, remove actions styles)
- `internal/web/static/src/css/components.css` (if btn-group styles need cleanup)
- `internal/web/static/src/css/responsive.css` (breakpoint adjustments)

### JS
- `internal/web/static/src/js/dashboard.js` (column config, any actions-column references)
- `internal/web/static/src/js/sse.js` (row update rendering must omit actions cell)
- `internal/web/static/src/js/main.js` (triggerUpdate wiring, if moved to badge click)

### Build
- `make frontend` to rebuild app.js and style.css

## Research references

- [Enterprise Data Table UX Patterns](https://www.pencilandpaper.io/articles/ux-pattern-analysis-enterprise-data-tables) -- alignment rules, opportunistic disclosure, density patterns
