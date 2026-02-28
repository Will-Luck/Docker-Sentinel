# Light Theme — Design

## Goal

Re-enable the light theme, add Auto (OS preference) mode, audit CSS for contrast compliance, and replace the login page background image with a pure CSS pattern.

## Decisions

- **Theme options**: Dark / Light / Auto (system preference)
- **Default**: Auto — respects OS `prefers-color-scheme`
- **Login page**: Drop inline `<style>` block and `login-bg.png`. Move all login CSS into `style.css`. Pure CSS circuit node mesh background.
- **Approach**: Full CSS audit before enabling — verify all `light-dark()` light-side values meet WCAG AA contrast ratios.

## Current State

The CSS already uses `light-dark()` on all 87+ variables with both light and dark values defined (Material Design 3 purple baseline palette). Light mode is disabled in two places:

1. `app.js:initTheme()` — forces `"dark"` if stored theme is `"light"` or `"auto"`
2. `settings.html` — only shows `<option value="dark">Dark</option>`

The login page (`login.html`) has ~130 lines of inline CSS with hardcoded dark hex values and references `login-bg.png`.

## Changes

### 1. CSS Audit

Check every `light-dark()` pair's light-side value for WCAG AA contrast:
- 4.5:1 for normal text (< 18px / < 14px bold)
- 3:1 for large text and UI components

Key pairs to verify:
- `--fg-primary` (#1D1B20) on `--bg-body` (#FEF7FF) — text on background
- `--fg-secondary` (#49454F) on `--bg-surface` (#F7F2FA) — secondary text
- `--fg-muted` (#79747E) on surfaces — muted/disabled text
- `--success` (#2E7D32) on `--success-bg` — status badges
- `--warning` (#E65100) on `--warning-bg` — warning indicators
- `--error` (#B3261E) on `--error-bg` — error states
- `--accent` (#6750A4) on surfaces — links, active elements

Fix any failing pairs by adjusting the light-side value only.

### 2. Login Page — CSS to style.css

Move all `.login-*` rules from the inline `<style>` in `login.html` into `style.css`.

Replace hardcoded dark hex values with `light-dark()` equivalents:

| Inline value | Variable equivalent |
|-------------|-------------------|
| `#141218` | `var(--md-surface)` |
| `#E6E0E9` | `var(--md-on-surface)` |
| `rgba(33,31,38,0.65)` | `color-mix(in srgb, var(--md-surface-container) 65%, transparent)` |
| `#49454F` | `var(--md-outline-variant)` |
| `#D0BCFF` | `var(--md-primary)` |
| `#CAC4D0` | `var(--md-on-surface-variant)` |
| `#938F99` | `var(--md-outline)` |
| `#381E72` | `var(--md-on-primary)` |

Remove `color-scheme: dark` from `.login-wrap` — let it inherit from root.

### 3. Login Background — CSS Circuit Node Mesh

Replace `login-bg.png` with pure CSS:

```css
.login-wrap {
  background-color: var(--md-surface);
  background-image:
    radial-gradient(circle, var(--md-primary) 1px, transparent 1px),
    linear-gradient(var(--md-outline-variant) 1px, transparent 1px),
    linear-gradient(90deg, var(--md-outline-variant) 1px, transparent 1px);
  background-size: 40px 40px, 40px 40px, 40px 40px;
  /* Dots + grid at ~8% opacity via colour choice */
}
```

Tune opacity and spacing during visual testing. Keep `backdrop-filter: blur(24px)` on the card.

### 4. JavaScript — initTheme()

```javascript
function initTheme() {
    var saved = localStorage.getItem("sentinel-theme") || "auto";
    applyTheme(saved);
}
```

Remove the two lines that force dark. `applyTheme()` already handles all three modes correctly.

### 5. Settings HTML

```html
<select id="theme-select" class="setting-select">
    <option value="auto">Auto (system)</option>
    <option value="dark">Dark</option>
    <option value="light">Light</option>
</select>
```

### 6. Visual Testing

Deploy to `.60:62850`. Use Playwright to screenshot every page in both light and dark modes:

- Dashboard, Queue, Container Detail, History, Logs
- Each Settings tab (Scanning, Notifications, Registries, Hooks, Appearance, Cluster, Security)
- Account, Login
- Modals (update confirmation, bulk policy)
- Toast notifications

Compare light screenshots for:
- Low contrast text
- Missing/invisible borders
- Hardcoded colours that didn't switch
- Status badge readability
- Code/monospace blocks

Fix issues found, re-screenshot, iterate.

## Files Modified

| File | Change |
|------|--------|
| `internal/web/static/style.css` | Add login CSS rules, adjust any failing contrast values |
| `internal/web/static/login.html` | Remove `<style>` block, remove `login-bg.png` reference |
| `internal/web/static/settings.html` | Add Light + Auto options to theme select |
| `internal/web/static/app.js` | Remove force-dark logic in `initTheme()` |
| `internal/web/static/login-bg.png` | Delete (no longer needed) |

## Out of Scope

- `style-claude-theme.css` (warm/serif alternative) — not wired up, leave as-is
- Mobile-responsive layout changes — not a theme concern
- Setup page styling — uses same CSS variables, will theme-switch automatically
