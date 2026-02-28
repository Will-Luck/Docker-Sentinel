# Modularisation Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Split `app.js` (5,207 lines), `style.css` (4,215 lines), and 4 oversized Go files into smaller modules with esbuild as the JS/CSS bundler.

**Architecture:** Source modules live in `internal/web/static/src/js/` and `src/css/`. esbuild bundles them back to `static/app.js` and `static/style.css` which Go's `embed.FS` picks up. Bundled output is committed so `go install` and Docker builds work without esbuild. Go splits are pure file moves with no logic changes.

**Tech Stack:** esbuild (installed via `go install`), ES module imports/exports, CSS `@import`, Go file reorganisation.

**Design doc:** `docs/plans/2026-02-24-modularisation-design.md`

---

## Task 1: Add esbuild to Makefile

**Files:**
- Modify: `Makefile`

**Step 1: Add esbuild targets to Makefile**

Add these variables and targets. esbuild installs via `go install` — no new dependencies beyond Go itself.

```makefile
ESBUILD := $(shell go env GOPATH)/bin/esbuild
JS_SRC  := internal/web/static/src/js/main.js
JS_OUT  := internal/web/static/app.js
CSS_SRC := internal/web/static/src/css/index.css
CSS_OUT := internal/web/static/style.css

$(ESBUILD):
	go install github.com/evanw/esbuild/cmd/esbuild@latest

js: $(ESBUILD)
	$(ESBUILD) $(JS_SRC) --bundle --format=iife --outfile=$(JS_OUT)

css: $(ESBUILD)
	$(ESBUILD) $(CSS_SRC) --bundle --outfile=$(CSS_OUT)

frontend: js css

.PHONY: js css frontend
```

Make `build` depend on `frontend`:

```makefile
build: frontend
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/sentinel
```

**Step 2: Verify esbuild installs**

Run: `cd /home/lns/Docker-Sentinel && make $(shell go env GOPATH)/bin/esbuild`
Expected: esbuild binary installed to `~/go/bin/esbuild`

**Step 3: Commit**

```bash
git add Makefile
git commit -m "build: add esbuild frontend bundling targets"
```

---

## Task 2: Create JS source directory and scaffold modules

**Files:**
- Create: `internal/web/static/src/js/` directory
- Create: 12 empty module files

**Step 1: Create directory and empty module files**

```bash
mkdir -p internal/web/static/src/js
touch internal/web/static/src/js/{csrf,utils,settings-core,settings-cluster,dashboard,queue,swarm,sse,notifications,registries,about,main}.js
```

**Step 2: Commit scaffold**

```bash
git add internal/web/static/src/js/
git commit -m "build: scaffold JS module structure"
```

---

## Task 3: Split JS — csrf.js and utils.js

These are the foundation modules that everything else imports from.

**Files:**
- Write: `internal/web/static/src/js/csrf.js`
- Write: `internal/web/static/src/js/utils.js`
- Reference: `internal/web/static/app.js` sections 0, 3, 4, 4a, 5 (apiPost only)

**Step 1: Write csrf.js**

Copy lines 1–43 from `app.js` (Section 0: CSRF Protection). This is the IIFE that patches `window.fetch`. It runs on import — no exports needed. However, `csrfToken` variable is referenced by inline scripts in HTML templates, so export it and assign to `window` in main.js.

Add at end:
```js
export { csrfToken };
```

Note: `csrfToken` is defined inside the IIFE. It needs to be extracted to module scope so it can be exported. Restructure the IIFE into a module-level `let csrfToken` + the fetch patch logic.

**Step 2: Write utils.js**

Copy from `app.js`:
- Section 3 (lines 1161–1228): Toast system (`showToast`, `_showToastImmediate`, `queueBatchToast`, `_flushBatchToasts`)
- Section 4 (lines 1229–1238): `escapeHTML`
- Section 4a (lines 1239–1322): `showConfirm`
- From Section 5 (line ~1323–1340): `apiPost` helper only (the shared fetch wrapper)

Export all public functions:
```js
export { showToast, escapeHTML, showConfirm, apiPost };
```

**Step 3: Verify modules parse**

```bash
$(go env GOPATH)/bin/esbuild internal/web/static/src/js/utils.js --bundle --format=iife --outfile=/dev/null
```

Expected: no errors

**Step 4: Commit**

```bash
git add internal/web/static/src/js/csrf.js internal/web/static/src/js/utils.js
git commit -m "refactor: extract csrf.js and utils.js modules"
```

---

## Task 4: Split JS — dashboard.js

**Files:**
- Write: `internal/web/static/src/js/dashboard.js`
- Reference: `app.js` sections 1, 1b, 2c (pause banner + last scan), 6, 7, 9, 9a, 9b, 10, stack drag reorder

**Step 1: Write dashboard.js**

Copy from `app.js`:
- Section 1 (lines 44–65): Theme system (`initTheme`, `applyTheme`)
- Section 1b (lines 67–129): Accordion persistence
- Section 2c pause banner (lines 1051–1105): `initPauseBanner`, `resumeScanning`, `checkPauseState`
- Section 2c last scan (lines 1106–1160): `refreshLastScan`, `renderLastScanTicker`
- Section 6 (lines 1829–1849): `onRowClick`
- Section 7 (lines 1850–1968): `toggleStack`, `toggleSwarmSection`, `toggleHostGroup`, `expandAllStacks`, `collapseAllStacks`
- Section 9 (lines 2287–2359): Multi-select (`updateSelectionUI`, `clearSelection`, `recomputeSelectionState`)
- Section 9a (lines 2360–2593): Filter & sort (`initFilters`, `activateFilter`, `applyFiltersAndSort`, `sortRows`, `sortSwarmServices`)
- Section 9b (lines 2594–2766): Dashboard tabs (`initDashboardTabs`, `switchDashboardTab`, `recalcTabStats`)
- Section 10 (lines 2767–2800): Manage mode (`toggleManageMode`)
- Stack drag reorder (lines 2801–2909)

Add imports from utils:
```js
import { showToast, showConfirm } from './utils.js';
```

Export all public functions:
```js
export {
  initTheme, applyTheme, initPauseBanner, resumeScanning,
  refreshLastScan, onRowClick, toggleStack, toggleSwarmSection,
  toggleHostGroup, expandAllStacks, collapseAllStacks,
  updateSelectionUI, clearSelection, recomputeSelectionState,
  initFilters, activateFilter, applyFiltersAndSort,
  initDashboardTabs, toggleManageMode, applyBulkPolicy
};
```

**Step 2: Commit**

```bash
git add internal/web/static/src/js/dashboard.js
git commit -m "refactor: extract dashboard.js module"
```

---

## Task 5: Split JS — queue.js

**Files:**
- Write: `internal/web/static/src/js/queue.js`
- Reference: `app.js` section 5 (lines 1323–1828, excluding `apiPost`)

**Step 1: Write queue.js**

Copy from `app.js` Section 5 — all API action functions except `apiPost` (which is in utils.js):
- `removeQueueRow`, `toggleQueueAccordion`
- `approveUpdate`, `ignoreUpdate`, `rejectUpdate`
- `bulkQueueAction`, `approveAll`, `ignoreAll`, `rejectAll`
- `triggerUpdate`, `triggerCheck`, `triggerRollback`
- `changePolicy`, `triggerScan`, `triggerSelfUpdate`
- `switchToGHCR`, `loadAllTags`, `updateToVersion`
- `applyBulkPolicy`

Add imports:
```js
import { apiPost, showToast, showConfirm } from './utils.js';
```

Export all public functions.

**Step 2: Commit**

```bash
git add internal/web/static/src/js/queue.js
git commit -m "refactor: extract queue.js module"
```

---

## Task 6: Split JS — swarm.js

**Files:**
- Write: `internal/web/static/src/js/swarm.js`
- Reference: `app.js` section 7b (lines 1969–2286)

**Step 1: Write swarm.js**

Copy Section 7b: `toggleSvc`, `triggerSvcUpdate`, `changeSvcPolicy`, `rollbackSvc`, `scaleSvc`, `refreshServiceRow`

Add imports:
```js
import { apiPost, showToast, showConfirm, escapeHTML } from './utils.js';
```

Export all public functions.

**Step 2: Commit**

```bash
git add internal/web/static/src/js/swarm.js
git commit -m "refactor: extract swarm.js module"
```

---

## Task 7: Split JS — sse.js

**Files:**
- Write: `internal/web/static/src/js/sse.js`
- Reference: `app.js` sections 11, 11a, 11a GHCR, 11b (lines 2910–3520)

**Step 1: Write sse.js**

Copy:
- Section 11 (lines 2910–2936): `scheduleReload`, `scheduleQueueReload`
- Section 11a row (lines 2937–3288): `updateContainerRow`, `updateStats`, `refreshDashboardStats`, `updatePendingColor`, `showBadgeSpinner`, `reapplyBadgeSpinners`, `clearPendingBadge`, `setConnectionStatus`, `initSSE`
- Section 11a GHCR (lines 3289–3448): `loadGHCRAlternatives`, `applyRegistryBadges`, `applyGHCRBadges`, `parseDockerRepo`, `renderGHCRAlternatives`
- Section 11b (lines 3449–3520): `loadDigestBanner`, `scheduleDigestBannerRefresh`, `updateQueueBadge`, `dismissDigestBanner`

Add imports:
```js
import { showToast, escapeHTML } from './utils.js';
```

Export: `initSSE`, `loadGHCRAlternatives`, `loadDigestBanner`, `updateQueueBadge`, and any functions needed by other modules.

**Step 2: Commit**

```bash
git add internal/web/static/src/js/sse.js
git commit -m "refactor: extract sse.js module"
```

---

## Task 8: Split JS — settings-core.js and settings-cluster.js

**Files:**
- Write: `internal/web/static/src/js/settings-core.js`
- Write: `internal/web/static/src/js/settings-cluster.js`
- Reference: `app.js` sections 2, 2a, 2b (lines 131–1050)

**Step 1: Write settings-core.js**

Copy:
- Section 2 (lines 131–405): `initSettingsPage` (the orchestrator function that delegates to all setting loaders)
- Section 2a (lines 447–693): All settings helpers — `normaliseDuration`, `onCustomUnitChange`, `parseDuration`, `populateCustomDuration`, `selectOptionByValue`, `updatePauseToggleText`, `setDefaultPolicy`, `setRollbackPolicy`, `onGracePeriodChange`, `applyCustomGracePeriod`, `setGracePeriod`, `setPauseState`, `updateLatestAutoText`, `setLatestAutoUpdate`, `saveFilters`, `updateToggleText`, and all `set*` toggle functions

Add imports:
```js
import { apiPost, showToast } from './utils.js';
```

Export all public functions. Note: `updateToggleText` is called from inline scripts in `settings.html` — must be exported and assigned to `window`.

**Step 2: Write settings-cluster.js**

Copy Section 2b (lines 695–1050): `loadClusterSettings`, `onClusterToggle`, `toggleClusterFields`, `saveClusterSettings`

Add imports:
```js
import { apiPost, showToast } from './utils.js';
```

Export all public functions.

**Step 3: Commit**

```bash
git add internal/web/static/src/js/settings-core.js internal/web/static/src/js/settings-cluster.js
git commit -m "refactor: extract settings-core.js and settings-cluster.js modules"
```

---

## Task 9: Split JS — notifications.js

**Files:**
- Write: `internal/web/static/src/js/notifications.js`
- Reference: `app.js` sections 13, 14, 14b (lines 3682–4480)

**Step 1: Write notifications.js**

Copy:
- Section 13 (lines 3682–4107): `PROVIDER_FIELDS`, `canonicaliseEventKey`, `loadNotificationChannels`, `renderChannels`, `buildChannelCard`, `addChannel`, `deleteChannel`, `collectChannelsFromDOM`, `saveNotificationChannels`, `testChannel`, `testNotification`
- Section 14 digest (lines 4108–4458): `modeUsesDigest`, `updateDigestScheduleVisibility`, `updateNotifyModePreview`, `onNotifyModeChange`, `getSelectedNotifyMode`, `loadDigestSettings`, `saveDigestSettings`, `triggerDigest`, `loadContainerNotifyPrefs`, `renderContainerNotifyPrefs`, `toggleAllPrefs`, `updatePrefsActionBar`, `applyPrefsToSelected`, `setContainerNotifyPref`
- Section 14b (lines 4459–4480): `escapeHtml` (notification-local version), `isSafeURL`

Add imports:
```js
import { apiPost, showToast, escapeHTML } from './utils.js';
```

Export all public functions. Note: the local `escapeHtml` (lowercase h) should be replaced with the import from utils.js to deduplicate.

**Step 2: Commit**

```bash
git add internal/web/static/src/js/notifications.js
git commit -m "refactor: extract notifications.js module"
```

---

## Task 10: Split JS — registries.js

**Files:**
- Write: `internal/web/static/src/js/registries.js`
- Reference: `app.js` sections 15, 16 (lines 4481–4883)

**Step 1: Write registries.js**

Copy:
- Section 15 (lines 4481–4863): `loadRegistries`, `renderRegistryStatus`, `renderRegistryCredentials`, `addRegistryCredential`, `deleteRegistryCredential`, `collectRegistryCredentialsFromDOM`, `saveRegistryCredentials`, `testRegistryCredential`
- Section 16 (lines 4864–4883): `updateRateLimitStatus`

Add imports:
```js
import { apiPost, showToast, escapeHTML } from './utils.js';
```

Export all public functions.

**Step 2: Commit**

```bash
git add internal/web/static/src/js/registries.js
git commit -m "refactor: extract registries.js module"
```

---

## Task 11: Split JS — about.js

**Files:**
- Write: `internal/web/static/src/js/about.js`
- Reference: `app.js` lines 4893–5207

**Step 1: Write about.js**

Copy the about/release sources block:
- `loadFooterVersion`, `loadAboutInfo`, `appendAboutSection`, `appendAboutRow`, `appendAboutRowEl`, `formatAboutTime`, `formatAboutTimeAgo`
- `loadReleaseSources`, `renderReleaseSources`, `addReleaseSource`, `deleteReleaseSource`, `collectReleaseSourcesFromDOM`, `saveReleaseSources`

Add imports:
```js
import { apiPost, showToast, escapeHTML } from './utils.js';
```

Export all public functions.

**Step 2: Commit**

```bash
git add internal/web/static/src/js/about.js
git commit -m "refactor: extract about.js module"
```

---

## Task 12: Write main.js and wire window exports

This is the critical integration step. `main.js` is the esbuild entry point that imports all modules and exposes HTML-referenced functions on `window`.

**Files:**
- Write: `internal/web/static/src/js/main.js`

**Step 1: Write main.js**

Structure:

```js
// Entry point — bundled by esbuild into app.js

// Side-effect import: patches fetch with CSRF token
import { csrfToken } from './csrf.js';

import {
  showToast, escapeHTML, showConfirm, apiPost
} from './utils.js';

import {
  initTheme, initPauseBanner, resumeScanning, refreshLastScan,
  onRowClick, toggleStack, toggleSwarmSection, toggleHostGroup,
  expandAllStacks, collapseAllStacks, clearSelection,
  initFilters, activateFilter, initDashboardTabs, toggleManageMode,
  applyBulkPolicy
} from './dashboard.js';

import {
  approveUpdate, ignoreUpdate, rejectUpdate,
  approveAll, ignoreAll, rejectAll,
  toggleQueueAccordion, triggerUpdate, triggerCheck, triggerRollback,
  changePolicy, triggerScan, triggerSelfUpdate,
  loadAllTags, updateToVersion, switchToGHCR
} from './queue.js';

import {
  toggleSvc, triggerSvcUpdate, changeSvcPolicy, rollbackSvc, scaleSvc
} from './swarm.js';

import { initSSE, loadGHCRAlternatives, loadDigestBanner } from './sse.js';

import {
  initSettingsPage, onPollIntervalChange, onCustomUnitChange,
  applyCustomPollInterval, setDefaultPolicy, setRollbackPolicy,
  onGracePeriodChange, applyCustomGracePeriod, setLatestAutoUpdate,
  setPauseState, saveFilters, setImageCleanup, saveCronSchedule,
  setDependencyAware, setHooksEnabled, setHooksWriteLabels,
  setDryRun, setPullOnly, setUpdateDelay, setComposeSync,
  setImageBackup, setShowStopped, setRemoveVolumes, setScanConcurrency,
  setHADiscovery, saveHADiscoveryPrefix, updateToggleText,
  confirmAuthToggle, createUser, saveGeneralSetting, switchRole
} from './settings-core.js';

import {
  onClusterToggle, saveClusterSettings, loadClusterSettings
} from './settings-cluster.js';

import {
  addChannel, saveNotificationChannels, testNotification,
  onNotifyModeChange, saveDigestSettings, triggerDigest,
  saveNotifyPref, loadNotificationChannels, loadContainerNotifyPrefs
} from './notifications.js';

import {
  addRegistryCredential, saveRegistryCredentials, loadRegistries
} from './registries.js';

import {
  loadFooterVersion, addReleaseSource, saveReleaseSources, loadAboutInfo
} from './about.js';

// ── Window exports ──────────────────────────────────────────
// Functions called from HTML on* attributes and inline <script> blocks.
// Grouped by module for maintainability.

// utils
window.showToast = showToast;
window.escapeHTML = escapeHTML;
window.showConfirm = showConfirm;
window.csrfToken = csrfToken;

// dashboard
window.activateFilter = activateFilter;
window.resumeScanning = resumeScanning;
window.expandAllStacks = expandAllStacks;
window.collapseAllStacks = collapseAllStacks;
window.toggleManageMode = toggleManageMode;
window.triggerScan = triggerScan;
window.toggleHostGroup = toggleHostGroup;
window.toggleStack = toggleStack;
window.toggleSwarmSection = toggleSwarmSection;
window.onRowClick = onRowClick;
window.applyBulkPolicy = applyBulkPolicy;
window.clearSelection = clearSelection;

// queue
window.toggleQueueAccordion = toggleQueueAccordion;
window.approveUpdate = approveUpdate;
window.ignoreUpdate = ignoreUpdate;
window.rejectUpdate = rejectUpdate;
window.approveAll = approveAll;
window.ignoreAll = ignoreAll;
window.rejectAll = rejectAll;
window.triggerUpdate = triggerUpdate;
window.triggerCheck = triggerCheck;
window.triggerRollback = triggerRollback;
window.changePolicy = changePolicy;
window.triggerSelfUpdate = triggerSelfUpdate;
window.loadAllTags = loadAllTags;
window.updateToVersion = updateToVersion;
window.switchToGHCR = switchToGHCR;

// swarm
window.toggleSvc = toggleSvc;
window.triggerSvcUpdate = triggerSvcUpdate;
window.changeSvcPolicy = changeSvcPolicy;
window.rollbackSvc = rollbackSvc;
window.scaleSvc = scaleSvc;

// settings-core
window.onPollIntervalChange = onPollIntervalChange;
window.onCustomUnitChange = onCustomUnitChange;
window.applyCustomPollInterval = applyCustomPollInterval;
window.setDefaultPolicy = setDefaultPolicy;
window.setRollbackPolicy = setRollbackPolicy;
window.onGracePeriodChange = onGracePeriodChange;
window.applyCustomGracePeriod = applyCustomGracePeriod;
window.setLatestAutoUpdate = setLatestAutoUpdate;
window.setPauseState = setPauseState;
window.saveFilters = saveFilters;
window.setImageCleanup = setImageCleanup;
window.saveCronSchedule = saveCronSchedule;
window.setDependencyAware = setDependencyAware;
window.setHooksEnabled = setHooksEnabled;
window.setHooksWriteLabels = setHooksWriteLabels;
window.setDryRun = setDryRun;
window.setPullOnly = setPullOnly;
window.setUpdateDelay = setUpdateDelay;
window.setComposeSync = setComposeSync;
window.setImageBackup = setImageBackup;
window.setShowStopped = setShowStopped;
window.setRemoveVolumes = setRemoveVolumes;
window.setScanConcurrency = setScanConcurrency;
window.setHADiscovery = setHADiscovery;
window.saveHADiscoveryPrefix = saveHADiscoveryPrefix;
window.updateToggleText = updateToggleText;
window.confirmAuthToggle = confirmAuthToggle;
window.createUser = createUser;
window.saveGeneralSetting = saveGeneralSetting;
window.switchRole = switchRole;

// settings-cluster
window.onClusterToggle = onClusterToggle;
window.saveClusterSettings = saveClusterSettings;

// notifications
window.addChannel = addChannel;
window.saveNotificationChannels = saveNotificationChannels;
window.testNotification = testNotification;
window.onNotifyModeChange = onNotifyModeChange;
window.saveDigestSettings = saveDigestSettings;
window.triggerDigest = triggerDigest;
window.saveNotifyPref = saveNotifyPref;

// registries
window.addRegistryCredential = addRegistryCredential;
window.saveRegistryCredentials = saveRegistryCredentials;

// about
window.addReleaseSource = addReleaseSource;
window.saveReleaseSources = saveReleaseSources;

// ── DOMContentLoaded ────────────────────────────────────────
// Copy the init block from app.js Section 12 (lines 3521–3681).
// This wires theme, SSE, filters, tabs, settings page, etc.
```

**IMPORTANT:** The `DOMContentLoaded` handler from Section 12 (lines 3521–3681) goes here. It calls `initTheme()`, `initSSE()`, `initFilters()`, `initDashboardTabs()`, `initSettingsPage()`, `loadFooterVersion()`, etc. Copy it verbatim, removing any function definitions that have been moved to modules.

**Step 2: Build the bundle**

```bash
cd /home/lns/Docker-Sentinel && make js
```

Expected: `internal/web/static/app.js` is regenerated. No esbuild errors.

**Step 3: Verify bundle size is similar**

```bash
wc -c internal/web/static/app.js
```

Expected: Within ~5% of the original (~200KB). The IIFE wrapper adds minimal overhead.

**Step 4: Verify the app works**

```bash
cd /home/lns/Docker-Sentinel && make build && bin/sentinel --help
```

Then: Open Sentinel in a browser, check the browser console for any `ReferenceError` or `TypeError` — these indicate a missing window export.

**Step 5: Commit**

```bash
git add internal/web/static/src/js/main.js internal/web/static/app.js
git commit -m "refactor: wire main.js entry point with all window exports"
```

---

## Task 13: Create CSS source directory and split modules

**Files:**
- Create: `internal/web/static/src/css/` directory with 11 module files + `index.css` entry point
- Reference: `internal/web/static/style.css` section map from design doc

**Step 1: Create directory and files**

```bash
mkdir -p internal/web/static/src/css
```

**Step 2: Split CSS into modules**

| Module | Lines from `style.css` | Contents |
|--------|----------------------|----------|
| `variables.css` | 6–144 | CSS custom properties (primitives, semantic, component tokens) |
| `base.css` | 145–326 | Reset, typography, nav bar, main layout, page header |
| `components.css` | 327–858, 1312–1406, 1526–1555, 1841–1872, 2670–2735, 2966–3024 | Cards, tables, badges, buttons, bulk bar, toasts, status dots, empty state, utilities, focus styles, transitions |
| `dashboard.css` | 969–1209, 1694–1780, 2736–2802, 2803–2872 | Stack groups, swarm sections, container rows, status hover, self-protected, animations |
| `queue.css` | 859–968, 1210–1311, 1407–1525 | Policy select, accordion panels, confirmation modal |
| `settings.css` | 1873–2669, 3521–3673 | Settings page, notification prefs, pause banner, event pills, collapsibles, toggle switches, duration picker, setting rows, general settings |
| `notifications.css` | 3025–3242, 2094–2158 | Digest banner, event filter pills |
| `registries.css` | 3243–3376 | Registry display, rate limits, GHCR badges, source badges |
| `cluster.css` | 3377–3520 | Cluster page, host cards, host groups, service detail |
| `auth.css` | 2931–2965, 3674–4215 | Nav user dropdown, login page |
| `responsive.css` | 2873–2930 | Media queries |

Note: Some sections are non-contiguous in the original file. The implementing agent should grep by CSS selectors/comments to find exact boundaries rather than relying solely on line numbers (which shift if earlier sections are slightly different than mapped).

**Step 3: Write index.css entry point**

```css
/* Entry point — bundled by esbuild into style.css */
@import "./variables.css";
@import "./base.css";
@import "./components.css";
@import "./dashboard.css";
@import "./queue.css";
@import "./settings.css";
@import "./notifications.css";
@import "./registries.css";
@import "./cluster.css";
@import "./auth.css";
@import "./responsive.css";
```

Order matters: variables first (defines custom properties), base second (reset + layout), components third (reusable), then page-specific, responsive last.

**Step 4: Build and verify**

```bash
cd /home/lns/Docker-Sentinel && make css
wc -c internal/web/static/style.css
```

Expected: similar size to original. Open app in browser — visual diff should show zero changes.

**Step 5: Commit**

```bash
git add internal/web/static/src/css/ internal/web/static/style.css
git commit -m "refactor: split style.css into 11 CSS modules with esbuild bundling"
```

---

## Task 14: Go split — server.go → interfaces.go

**Files:**
- Modify: `internal/web/server.go`
- Create: `internal/web/interfaces.go`

**Step 1: Create interfaces.go**

Move lines 72–534 from `server.go` into a new file `interfaces.go`. This is everything from the first interface (`HistoryStore`) through `ConfigWriter`, including all struct types that are part of interface contracts (`RegistryCredential`, `RateLimitStatus`, `GHCRAlternative`, `RemoteContainer`, `ClusterHost`, `ServiceSummary`, `TaskInfo`, `ServiceDetail`, `ReleaseSource`, `HookEntry`, `PortainerEndpoint`, `PortainerContainerInfo`, `LogEntry`, `NotifyPref`, `NotifyState`, `UpdateRecord`, `SnapshotEntry`, `PendingUpdate`, `ContainerSummary`, `ContainerInspect`).

The new file needs:
```go
package web
```

Plus any imports used by these types (likely `time` and `context` at minimum).

`server.go` retains: package declaration, imports, `//go:embed`, `Dependencies` struct (lines 28–70), `Server` struct, and everything from line 536 onward.

**Step 2: Verify compilation**

```bash
cd /home/lns/Docker-Sentinel && go build ./...
```

Expected: clean build, no errors.

**Step 3: Verify tests pass**

```bash
cd /home/lns/Docker-Sentinel && go test ./internal/web/...
```

**Step 4: Commit**

```bash
git add internal/web/server.go internal/web/interfaces.go
git commit -m "refactor: extract interfaces.go from server.go (30+ interface definitions)"
```

---

## Task 15: Go split — handlers.go → handlers_dashboard.go

**Files:**
- Modify: `internal/web/handlers.go`
- Create: `internal/web/handlers_dashboard.go`

**Step 1: Create handlers_dashboard.go**

Move from `handlers.go`:
- Lines 20–151: View types (`pageData`, `tabStats`, `hostGroup`, `containerView`, `stackGroup`, `serviceView`, `taskView`)
- Lines 155–234: `buildServiceView`
- Lines 237–648: `handleDashboard`

New file needs:
```go
package web
```

Plus imports for `net/http`, `sort`, `strings`, `html/template`, and any internal packages used by `handleDashboard`.

`handlers.go` retains everything from line 650 onward (handleQueue, handleHistory, handleSettings, handleLogs, handleContainerRow, handleDashboardStats, handleCluster, handleContainerDetail, handleServiceDetail, renderTemplate, renderError).

**Step 2: Verify compilation and tests**

```bash
cd /home/lns/Docker-Sentinel && go build ./... && go test ./internal/web/...
```

**Step 3: Commit**

```bash
git add internal/web/handlers.go internal/web/handlers_dashboard.go
git commit -m "refactor: extract handlers_dashboard.go (view types + handleDashboard)"
```

---

## Task 16: Go split — api_settings.go → api_portainer.go

**Files:**
- Modify: `internal/web/api_settings.go`
- Modify: `internal/web/api_portainer.go` (existing, append)

**Step 1: Move Portainer settings handlers**

Move lines 942–1025 from `api_settings.go` (the 4 Portainer handlers) and append them to the existing `api_portainer.go`:
- `apiSetPortainerEnabled` (942–968)
- `apiSetPortainerURL` (970–987)
- `apiSetPortainerToken` (989–1005)
- `apiTestPortainerConnection` (1007–1025)

Add any missing imports to `api_portainer.go` (likely `encoding/json`, `net/http`, `strings`).

**Step 2: Verify compilation and tests**

```bash
cd /home/lns/Docker-Sentinel && go build ./... && go test ./internal/web/...
```

**Step 3: Commit**

```bash
git add internal/web/api_settings.go internal/web/api_portainer.go
git commit -m "refactor: move Portainer settings handlers to api_portainer.go"
```

---

## Task 17: Go split — updater.go → updater_remote.go

**Files:**
- Modify: `internal/engine/updater.go`
- Create: `internal/engine/updater_remote.go`

**Step 1: Create updater_remote.go**

Move from `updater.go`:
- Lines 895–915: `scanRemoteHosts`
- Lines 920–1128: `scanRemoteHost`
- Lines 1132–1152: `scanPortainerEndpoints`
- Lines 1155–1389: `scanPortainerEndpoint`
- Lines 1394–1486: `checkGHCRAlternatives`

New file needs:
```go
package engine
```

Plus imports for `context`, `fmt`, `log/slog`, `strings`, `time`, and any internal packages used.

`updater.go` retains: types/interfaces (lines 1–165), constructor/setters (168–407), `Scan()` function (408–886), and the separator comment.

**Step 2: Verify compilation and tests**

```bash
cd /home/lns/Docker-Sentinel && go build ./... && go test ./internal/engine/...
```

**Step 3: Commit**

```bash
git add internal/engine/updater.go internal/engine/updater_remote.go
git commit -m "refactor: extract updater_remote.go (multi-host + Portainer scanning)"
```

---

## Task 18: Update Dockerfile and CI

**Files:**
- Modify: `Dockerfile`
- Modify: `.gitea/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`

**Step 1: Update Dockerfile**

The Dockerfile's builder stage needs esbuild. Since `make build` now depends on `make frontend`, and `make frontend` needs esbuild, add it to the builder:

```dockerfile
FROM golang:1.24-alpine AS builder
RUN apk add --no-cache git make
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
RUN go install github.com/evanw/esbuild/cmd/esbuild@latest
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN make frontend
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /sentinel ./cmd/sentinel
```

Note: Since bundled output is committed to the repo, the `make frontend` step in Docker is a safety net — it ensures the binary always matches the source modules. If someone modifies a source JS file but forgets `make frontend`, Docker build catches it.

**Step 2: Update CI workflows**

Add `make frontend` step before `make build` in `.gitea/workflows/ci.yml` build job.
Add `go install esbuild` + `make frontend` before Go build in `.github/workflows/release.yml` if needed.

**Step 3: Verify Docker build**

```bash
cd /home/lns/Docker-Sentinel && docker build -t sentinel-modtest .
```

**Step 4: Commit**

```bash
git add Dockerfile .gitea/workflows/ci.yml .github/workflows/release.yml
git commit -m "build: add esbuild to Dockerfile and CI pipelines"
```

---

## Task 19: Final verification

**Step 1: Full build**

```bash
cd /home/lns/Docker-Sentinel && make clean && make frontend && make build
```

**Step 2: Full test suite**

```bash
cd /home/lns/Docker-Sentinel && make test
```

**Step 3: Lint**

```bash
cd /home/lns/Docker-Sentinel && make lint
```

**Step 4: Manual browser test**

Run the binary and verify in the browser:
- Dashboard loads, containers display, SSE connection works
- Settings page loads, all toggles work
- Queue page loads, approve/reject/ignore work
- Container detail page loads
- Cluster page loads (if cluster mode)
- Check browser console for any JS errors

**Step 5: Final commit (if any fixups needed)**

```bash
git add -A
git commit -m "refactor: final fixups for modularisation"
```

---

## Summary

| Phase | Tasks | Files Created | Files Modified |
|-------|-------|--------------|----------------|
| Build system | 1 | 0 | 1 (Makefile) |
| JS modules | 2–12 | 12 | 1 (app.js regenerated) |
| CSS modules | 13 | 12 | 1 (style.css regenerated) |
| Go splits | 14–17 | 3 | 5 |
| Build integration | 18 | 0 | 3 (Dockerfile, CI x2) |
| Verification | 19 | 0 | 0 |

Total: 19 tasks, ~27 files created, ~11 files modified.
