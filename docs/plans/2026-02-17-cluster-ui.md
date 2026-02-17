# Cluster Management UI Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a web UI for cluster management — Settings tab to enable/configure, standalone Cluster page with host cards and enrollment, dashboard host groups for remote containers.

**Architecture:** Settings persist cluster config to BoltDB. Toggling cluster mode dynamically starts/stops the gRPC server (no container restart). The Cluster nav link only appears when enabled. Dashboard container table gains host-level grouping above existing stack groups. SSE broadcasts host connect/disconnect events for live updates.

**Tech Stack:** Go 1.24, BoltDB, vanilla HTML/CSS/JS, SSE, gRPC (existing cluster server)

**Design doc:** `docs/plans/2026-02-17-cluster-ui-design.md`

---

## Task 1: Cluster Config Store

Add BoltDB methods to persist cluster configuration (enabled, port, grace period, default remote policy).

**Files:**
- Modify: `internal/store/bolt.go` (bucket already exists: `bucketSettings`)
- Modify: `internal/web/server.go:306-311` (SettingsStore interface — no changes needed, uses existing SaveSetting/LoadSetting)

**Step 1:** Define cluster config keys as constants.

In `internal/store/bolt.go`, add near the existing bucket constants:

```go
// Cluster settings keys (stored in bucketSettings).
const (
	SettingClusterEnabled       = "cluster_enabled"        // "true" / "false"
	SettingClusterPort          = "cluster_port"           // e.g. "9443"
	SettingClusterGracePeriod   = "cluster_grace_period"   // e.g. "30m"
	SettingClusterRemotePolicy  = "cluster_remote_policy"  // "auto" / "manual" / "pinned"
)
```

**Step 2:** Verify existing `SaveSetting`/`LoadSetting` methods work for these keys (they're generic key-value on the `settings` bucket — no new methods needed).

**Step 3:** Commit.

```bash
git add internal/store/bolt.go
git commit -m "feat(store): add cluster config setting keys"
```

---

## Task 2: Page Data — Cluster-Aware Navigation

Add `ClusterEnabled` to `pageData` so templates can conditionally show the Cluster nav link. Add a helper to populate it.

**Files:**
- Modify: `internal/web/handlers.go:16-39` (pageData struct)
- Modify: `internal/web/handlers.go` (withAuth or new helper)

**Step 1:** Add `ClusterEnabled` and `ClusterHosts` fields to `pageData`:

```go
type pageData struct {
	// ... existing fields ...
	ClusterEnabled bool
	ClusterHosts   []ClusterHost // populated only on /cluster page
}
```

**Step 2:** Create a `withCluster` helper that populates `ClusterEnabled`:

```go
func (s *Server) withCluster(data *pageData) {
	if s.deps.Cluster != nil {
		data.ClusterEnabled = true
	} else if s.deps.SettingsStore != nil {
		// Check if cluster is enabled in settings but server not yet started
		// (shouldn't happen in practice, but defensive)
		v, _ := s.deps.SettingsStore.LoadSetting(store.SettingClusterEnabled)
		data.ClusterEnabled = v == "true"
	}
}
```

**Step 3:** Call `s.withCluster(&data)` in every handler that builds pageData — `handleDashboard`, `handleQueue`, `handleHistory`, `handleSettings`, `handleLogs`, `handleAccount`, `handleContainer`, `handleService`.

**Step 4:** Commit.

```bash
git add internal/web/handlers.go
git commit -m "feat(web): add ClusterEnabled to pageData for conditional nav"
```

---

## Task 3: Cluster Settings API

Add GET/POST endpoints for cluster configuration.

**Files:**
- Modify: `internal/web/api_settings.go` (new handler functions)
- Modify: `internal/web/server.go` (register routes)

**Step 1:** Add the GET handler:

```go
func (s *Server) apiClusterSettings(w http.ResponseWriter, _ *http.Request) {
	settings := map[string]string{
		"enabled":       "false",
		"port":          "9443",
		"grace_period":  "30m",
		"remote_policy": "manual",
	}
	if s.deps.SettingsStore != nil {
		for key := range settings {
			fullKey := "cluster_" + key
			if v, err := s.deps.SettingsStore.LoadSetting(fullKey); err == nil && v != "" {
				settings[key] = v
			}
		}
	}
	writeJSON(w, http.StatusOK, settings)
}
```

**Step 2:** Add the POST handler:

```go
func (s *Server) apiClusterSettingsSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled      *bool   `json:"enabled"`
		Port         string  `json:"port"`
		GracePeriod  string  `json:"grace_period"`
		RemotePolicy string  `json:"remote_policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusInternalServerError, "settings store not available")
		return
	}

	// Save each provided field.
	if req.Enabled != nil {
		val := "false"
		if *req.Enabled {
			val = "true"
		}
		s.deps.SettingsStore.SaveSetting(store.SettingClusterEnabled, val)
	}
	if req.Port != "" {
		s.deps.SettingsStore.SaveSetting(store.SettingClusterPort, req.Port)
	}
	if req.GracePeriod != "" {
		s.deps.SettingsStore.SaveSetting(store.SettingClusterGracePeriod, req.GracePeriod)
	}
	if req.RemotePolicy != "" {
		s.deps.SettingsStore.SaveSetting(store.SettingClusterRemotePolicy, req.RemotePolicy)
	}

	// Dynamic start/stop handled by Task 4's ClusterLifecycle callback.
	if req.Enabled != nil && s.clusterLifecycle != nil {
		if *req.Enabled {
			if err := s.clusterLifecycle.Start(); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to start cluster: "+err.Error())
				return
			}
		} else {
			s.clusterLifecycle.Stop()
		}
	}

	s.logEvent(r, "cluster-settings", "", "Cluster settings updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}
```

**Step 3:** Register routes in `registerRoutes()` (inside the existing cluster block, or unconditionally since settings should be accessible even when cluster is off):

```go
// Cluster settings — always available (admin only).
s.mux.Handle("GET /api/settings/cluster", perm(auth.PermSettingsModify, s.apiClusterSettings))
s.mux.Handle("POST /api/settings/cluster", perm(auth.PermSettingsModify, s.apiClusterSettingsSave))
```

**Step 4:** Add `clusterLifecycle` field to `Server` struct:

```go
type ClusterLifecycle interface {
	Start() error
	Stop()
}
```

Add to Server struct:

```go
type Server struct {
	// ... existing fields ...
	clusterLifecycle ClusterLifecycle
}
```

Add a setter:

```go
func (s *Server) SetClusterLifecycle(cl ClusterLifecycle) {
	s.clusterLifecycle = cl
}
```

**Step 5:** Commit.

```bash
git add internal/web/api_settings.go internal/web/server.go
git commit -m "feat(web): cluster settings API endpoints"
```

---

## Task 4: Dynamic Cluster Lifecycle

Allow the cluster server to start/stop at runtime from the settings API, and read saved config on startup.

**Files:**
- Modify: `cmd/sentinel/main.go:280-352`
- Modify: `cmd/sentinel/adapters.go`

**Step 1:** Create a `clusterManager` struct in adapters.go that implements `web.ClusterLifecycle`:

```go
type clusterManager struct {
	mu        sync.Mutex
	srv       *clusterserver.Server
	db        *store.Store
	bus       *events.Bus
	log       *slog.Logger
	updater   *engine.Updater
	cancel    context.CancelFunc
	webDeps   *web.Dependencies // pointer so we can update Cluster field
}

func (m *clusterManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.srv != nil {
		return nil // already running
	}

	// Read config from DB.
	port, _ := m.db.LoadSetting(store.SettingClusterPort)
	if port == "" {
		port = "9443"
	}
	dataDir, _ := m.db.LoadSetting(store.SettingClusterDataDir)
	if dataDir == "" {
		dataDir = "/data/cluster"
	}

	ca, err := cluster.EnsureCA(dataDir)
	if err != nil {
		return fmt.Errorf("initialise CA: %w", err)
	}

	m.srv = clusterserver.New(ca, m.db, m.bus, m.log)

	addr := net.JoinHostPort("", port)
	if err := m.srv.Start(addr); err != nil {
		m.srv = nil
		return fmt.Errorf("start gRPC: %w", err)
	}

	// Wire up cluster scanner for multi-host scanning.
	m.updater.SetClusterScanner(&clusterScannerAdapter{srv: m.srv})

	// Update web deps so handlers see the cluster provider.
	m.webDeps.Cluster = &clusterAdapter{srv: m.srv}

	m.log.Info("cluster gRPC server started", "addr", addr)
	return nil
}

func (m *clusterManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.srv == nil {
		return
	}

	m.srv.Stop()
	m.srv = nil
	m.updater.SetClusterScanner(nil)
	m.webDeps.Cluster = nil

	m.log.Info("cluster gRPC server stopped")
}
```

**Step 2:** In `main.go`, refactor the cluster startup block:

```go
// Check if cluster should start (env var overrides DB setting).
clusterEnabled := cfg.ClusterEnabled
if !clusterEnabled && db != nil {
	if v, _ := db.LoadSetting(store.SettingClusterEnabled); v == "true" {
		clusterEnabled = true
	}
}

cm := &clusterManager{
	db:      db,
	bus:     bus,
	log:     log.Logger,
	updater: updater,
}

if clusterEnabled {
	if err := cm.Start(); err != nil {
		log.Error("failed to start cluster", "error", err)
		os.Exit(1)
	}
	defer cm.Stop()
}

// Later, when creating web deps:
webDeps := web.Dependencies{
	// ... existing fields ...
}
cm.webDeps = &webDeps  // so dynamic start/stop can update Cluster field
if cm.srv != nil {
	webDeps.Cluster = &clusterAdapter{srv: cm.srv}
}

srv := web.NewServer(webDeps)
srv.SetClusterLifecycle(cm)
```

**Step 3:** Commit.

```bash
git add cmd/sentinel/main.go cmd/sentinel/adapters.go
git commit -m "feat: dynamic cluster lifecycle — start/stop gRPC from settings"
```

---

## Task 5: SSE Host Events

Broadcast host connection/disconnection events so the Cluster page and dashboard update in real-time.

**Files:**
- Modify: `internal/events/bus.go` (or wherever event types are defined)
- Modify: `internal/cluster/server/server.go` (publish events on connect/disconnect)

**Step 1:** Add event type constants (check where existing ones are defined):

```go
const (
	EventHostConnected    = "host:connected"
	EventHostDisconnected = "host:disconnected"
)
```

**Step 2:** In the cluster server's `handleStream` (where agents connect), publish an event when a host connects:

```go
s.bus.Publish(events.Event{
	Type: events.EventHostConnected,
	Data: map[string]string{
		"hostID":   hostID,
		"hostName": hostName,
	},
})
```

**Step 3:** Similarly, when a host disconnects (stream ends or heartbeat timeout), publish:

```go
s.bus.Publish(events.Event{
	Type: events.EventHostDisconnected,
	Data: map[string]string{
		"hostID":   hostID,
		"hostName": hostName,
	},
})
```

**Step 4:** Commit.

```bash
git add internal/events/ internal/cluster/server/server.go
git commit -m "feat(sse): broadcast host connected/disconnected events"
```

---

## Task 6: Settings — Cluster Tab

Add the Cluster tab to the Settings page.

**Files:**
- Modify: `internal/web/static/settings.html:66-76` (tab nav)
- Modify: `internal/web/static/settings.html` (new tab panel)
- Modify: `internal/web/static/style.css` (any new styles)

**Step 1:** Add the Cluster tab button (conditionally shown for admins, after Security tab):

```html
<button class="tab-btn" role="tab" aria-selected="false" aria-controls="tab-cluster" data-tab="cluster">Cluster</button>
```

**Step 2:** Add the tab panel:

```html
<div class="tab-panel" id="tab-cluster" role="tabpanel">
    <div class="settings-section">
        <h3>Multi-Host Monitoring</h3>
        <p class="setting-description">Monitor containers across multiple Docker hosts using lightweight agents that report back over encrypted gRPC (mTLS).</p>

        <div class="setting-row">
            <div class="setting-info">
                <label for="cluster-enabled">Enable cluster mode</label>
                <p class="setting-hint">Starts a gRPC server for agent connections. No container restart needed.</p>
            </div>
            <label class="toggle-switch">
                <input type="checkbox" id="cluster-enabled" onchange="onClusterToggle(this.checked)">
                <span class="toggle-slider"></span>
            </label>
        </div>

        <div id="cluster-fields" class="cluster-fields disabled">
            <div class="setting-row">
                <div class="setting-info">
                    <label for="cluster-port">gRPC port</label>
                    <p class="setting-hint">Port for agent connections. Change requires restart.</p>
                </div>
                <input type="number" id="cluster-port" class="setting-input" value="9443" min="1024" max="65535" onchange="onClusterSettingChange()">
            </div>

            <div class="setting-row">
                <div class="setting-info">
                    <label for="cluster-grace">Grace period</label>
                    <p class="setting-hint">How long agents wait before entering autonomous mode when this server is unreachable.</p>
                </div>
                <select id="cluster-grace" class="setting-select" onchange="onClusterSettingChange()">
                    <option value="5m">5 minutes</option>
                    <option value="15m">15 minutes</option>
                    <option value="30m" selected>30 minutes</option>
                    <option value="1h">1 hour</option>
                    <option value="2h">2 hours</option>
                </select>
            </div>

            <div class="setting-row">
                <div class="setting-info">
                    <label for="cluster-policy">Default remote policy</label>
                    <p class="setting-hint">Update policy applied to containers discovered on newly enrolled hosts.</p>
                </div>
                <select id="cluster-policy" class="setting-select" onchange="onClusterSettingChange()">
                    <option value="manual" selected>Manual</option>
                    <option value="auto">Auto</option>
                    <option value="pinned">Pinned</option>
                </select>
            </div>
        </div>
    </div>
</div>
```

**Step 3:** Add JS to load/save cluster settings:

```javascript
function loadClusterSettings() {
    fetch('/api/settings/cluster')
        .then(r => r.json())
        .then(s => {
            document.getElementById('cluster-enabled').checked = s.enabled === 'true';
            document.getElementById('cluster-port').value = s.port || '9443';
            document.getElementById('cluster-grace').value = s.grace_period || '30m';
            document.getElementById('cluster-policy').value = s.remote_policy || 'manual';
            toggleClusterFields(s.enabled === 'true');
        });
}

function onClusterToggle(enabled) {
    if (!enabled) {
        // Warn about connected agents.
        if (!confirm('Disabling cluster mode will disconnect all agents. Continue?')) {
            document.getElementById('cluster-enabled').checked = true;
            return;
        }
    }
    toggleClusterFields(enabled);
    saveClusterSettings();
}

function toggleClusterFields(enabled) {
    const fields = document.getElementById('cluster-fields');
    if (enabled) {
        fields.classList.remove('disabled');
    } else {
        fields.classList.add('disabled');
    }
}

function onClusterSettingChange() {
    saveClusterSettings();
}

function saveClusterSettings() {
    const enabled = document.getElementById('cluster-enabled').checked;
    fetch('/api/settings/cluster', {
        method: 'POST',
        headers: {'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken},
        body: JSON.stringify({
            enabled: enabled,
            port: document.getElementById('cluster-port').value,
            grace_period: document.getElementById('cluster-grace').value,
            remote_policy: document.getElementById('cluster-policy').value,
        })
    })
    .then(r => r.json())
    .then(data => {
        if (data.status === 'saved') {
            showToast('Cluster settings saved');
            // If toggling on/off, reload page to update nav.
            if (data.reload) window.location.reload();
        }
    })
    .catch(err => showToast('Failed to save: ' + err, 'error'));
}
```

**Step 4:** Add CSS for `.cluster-fields.disabled`:

```css
.cluster-fields.disabled {
    opacity: 0.4;
    pointer-events: none;
}
```

**Step 5:** Verify visually with Playwright screenshot.

**Step 6:** Commit.

```bash
git add internal/web/static/settings.html internal/web/static/style.css
git commit -m "feat(ui): cluster settings tab — enable/configure cluster mode"
```

---

## Task 7: Cluster Page — Host Cards & Enrollment

Create the `/cluster` page template and handler.

**Files:**
- Create: `internal/web/static/cluster.html`
- Modify: `internal/web/handlers.go` (new `handleCluster` handler)
- Modify: `internal/web/server.go` (register route)
- Modify: `internal/web/static/style.css` (host card styles)

**Step 1:** Add the `handleCluster` handler:

```go
func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	data := pageData{
		Page:       "cluster",
		QueueCount: len(s.deps.Queue.List()),
	}
	if s.deps.Cluster != nil {
		data.ClusterHosts = s.deps.Cluster.AllHosts()
		connected := s.deps.Cluster.ConnectedHosts()
		connSet := make(map[string]bool, len(connected))
		for _, id := range connected {
			connSet[id] = true
		}
		for i := range data.ClusterHosts {
			data.ClusterHosts[i].Connected = connSet[data.ClusterHosts[i].ID]
		}
	}
	s.withAuth(r, &data)
	s.withCluster(&data)
	s.renderTemplate(w, "cluster.html", data)
}
```

**Step 2:** Register the route (conditionally — but since cluster can be enabled dynamically, register unconditionally and redirect if disabled):

```go
s.mux.Handle("GET /cluster", perm(auth.PermSettingsModify, s.handleCluster))
```

In the handler, if cluster is not enabled, redirect to settings:

```go
if s.deps.Cluster == nil {
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
	return
}
```

**Step 3:** Create `cluster.html` template. This follows the existing page pattern (full HTML document with nav). The key sections are:

- Stat cards (Hosts, Connected, Containers, Server)
- Host cards grid (responsive CSS grid)
- Enrollment section with token generation
- JS for fetch-based interactions (generate token, drain/revoke/remove with confirmation)
- SSE listener for `host:connected` / `host:disconnected` events

The template should be approximately 300-400 lines following the established patterns in `index.html` and `queue.html`. Nav bar includes the standard links plus `{{if .ClusterEnabled}}<a href="/cluster" class="nav-link active">Cluster</a>{{end}}`.

**Step 4:** Add host card CSS to `style.css`:

```css
/* Host cards grid */
.host-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
    gap: var(--sp-4);
    margin-bottom: var(--sp-6);
}

.host-card {
    background: var(--card-bg);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--sp-5);
}

.host-card-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: var(--sp-3);
}

.host-status-dot {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    display: inline-block;
    margin-right: var(--sp-2);
}
.host-status-dot.connected { background: var(--green); }
.host-status-dot.disconnected { background: var(--red); }
.host-status-dot.draining { background: var(--yellow); }

.host-card-stats {
    display: flex;
    gap: var(--sp-4);
    margin: var(--sp-3) 0;
    color: var(--text-muted);
    font-size: 0.9rem;
}

.host-card-actions {
    display: flex;
    gap: var(--sp-2);
    margin-top: var(--sp-4);
    border-top: 1px solid var(--border);
    padding-top: var(--sp-3);
}

/* Enrollment section */
.enroll-section {
    margin-top: var(--sp-6);
}

.token-display {
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: var(--sp-3);
    font-family: monospace;
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    word-break: break-all;
}

.token-display .copy-btn {
    flex-shrink: 0;
}

.agent-command {
    background: var(--code-bg);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: var(--sp-3);
    font-family: monospace;
    font-size: 0.85rem;
    white-space: pre-wrap;
    margin-top: var(--sp-3);
}
```

**Step 5:** Verify visually with Playwright.

**Step 6:** Commit.

```bash
git add internal/web/static/cluster.html internal/web/handlers.go internal/web/server.go internal/web/static/style.css
git commit -m "feat(ui): cluster page — host cards, enrollment, lifecycle actions"
```

---

## Task 8: Dashboard Host Groups

When cluster is active, add host-level grouping to the container table.

**Files:**
- Modify: `internal/web/handlers.go:186-391` (handleDashboard — add remote containers)
- Modify: `internal/web/static/index.html:115-178` (table structure)
- Modify: `internal/web/static/style.css` (host group styles)

**Step 1:** Extend `pageData` with host groups:

```go
type hostGroup struct {
	ID         string
	Name       string
	Connected  bool
	Containers []containerView
	Stacks     []stackGroup
	Count      int
}
```

Add to `pageData`:

```go
type pageData struct {
	// ... existing fields ...
	HostGroups []hostGroup // populated when cluster is active
}
```

**Step 2:** In `handleDashboard`, when cluster is active, fetch remote containers and group them:

```go
if s.deps.Cluster != nil {
	hosts := s.deps.Cluster.AllHosts()
	connected := s.deps.Cluster.ConnectedHosts()
	// ... build hostGroup entries with containers from each host ...
	// Local containers go into a "local" hostGroup
	data.HostGroups = hostGroups
}
```

The host containers come from the existing `ClusterHost.Containers` count. For full container details, we need a new interface method or we use the last-known state from the cluster server's state cache.

**Step 3:** In `index.html`, wrap the existing stack groups in a host-level accordion when `HostGroups` is populated:

```html
{{if .HostGroups}}
    {{range .HostGroups}}
    <tbody class="host-group" data-host="{{.ID}}">
        <tr class="host-header" onclick="toggleHostGroup(this)">
            <td colspan="6">
                <div class="host-header-inner">
                    <span class="host-toggle">▸</span>
                    <span class="host-status-dot {{if .Connected}}connected{{else}}disconnected{{end}}"></span>
                    <span class="host-name">{{.Name}}</span>
                    <span class="host-count">{{.Count}}</span>
                </div>
            </td>
        </tr>
        {{range .Stacks}}
            <!-- existing stack group rendering -->
        {{end}}
    </tbody>
    {{end}}
{{else}}
    <!-- existing non-cluster rendering (unchanged) -->
    {{range .Stacks}}
    <tbody class="stack-group ...">
        ...
    </tbody>
    {{end}}
{{end}}
```

**Step 4:** Add JS for `toggleHostGroup()` — same pattern as `toggleStack()`.

**Step 5:** Add CSS for `.host-header`:

```css
.host-header {
    cursor: pointer;
    background: var(--card-bg-alt);
    border-bottom: 2px solid var(--border);
}

.host-header-inner {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    padding: var(--sp-2) var(--sp-3);
    font-weight: 600;
}
```

**Step 6:** Update stat cards to aggregate across all hosts (fleet-wide totals).

**Step 7:** Verify visually with Playwright.

**Step 8:** Commit.

```bash
git add internal/web/handlers.go internal/web/static/index.html internal/web/static/style.css
git commit -m "feat(ui): dashboard host groups — containers grouped by host when cluster active"
```

---

## Task 9: Queue & History Host Badges

Show which host a pending update or history entry belongs to.

**Files:**
- Modify: `internal/web/static/queue.html` (add host badge column/inline)
- Modify: `internal/web/static/history.html` (add host badge)
- Modify: `internal/web/static/style.css` (badge styles)

**Step 1:** In `queue.html`, add a small host badge next to the container name for remote entries. The queue items already have `HostID` — render the host name as a badge:

```html
<span class="host-badge" title="{{.HostName}}">{{.HostName}}</span>
```

**Step 2:** Same pattern in `history.html`.

**Step 3:** Add CSS:

```css
.host-badge {
    display: inline-block;
    background: var(--purple-dim);
    color: var(--purple);
    font-size: 0.75rem;
    padding: 1px 6px;
    border-radius: var(--radius-sm);
    margin-left: var(--sp-1);
    vertical-align: middle;
}
```

**Step 4:** The queue `PendingUpdate` struct needs `HostName` populated. In the scan flow, when queuing remote containers, set `HostName` from the host info.

**Step 5:** Commit.

```bash
git add internal/web/static/queue.html internal/web/static/history.html internal/web/static/style.css internal/engine/queue.go
git commit -m "feat(ui): host badges on queue and history entries"
```

---

## Task 10: Conditional Nav Link Across All Templates

Add the Cluster nav link to every HTML template, conditionally shown when cluster is enabled.

**Files:**
- Modify: All `.html` templates (11 files): `index.html`, `queue.html`, `history.html`, `settings.html`, `logs.html`, `account.html`, `container.html`, `service.html`, `login.html`, `setup.html`, `error.html`

**Step 1:** In every template's nav-links div, add after the Settings link:

```html
{{if .ClusterEnabled}}<a href="/cluster" class="nav-link{{if eq .Page "cluster"}} active{{end}}">Cluster</a>{{end}}
```

**Step 2:** This is a mechanical change across all templates. The `ClusterEnabled` field is already populated by `withCluster()` from Task 2.

**Step 3:** For `login.html`, `setup.html`, and `error.html` which may not have full pageData, ensure the template handles missing `ClusterEnabled` gracefully (Go templates treat zero-value bool as false, so `{{if .ClusterEnabled}}` is safe).

**Step 4:** Commit.

```bash
git add internal/web/static/*.html
git commit -m "feat(ui): conditional Cluster nav link across all templates"
```

---

## Task 11: Integration Testing & Visual Verification

End-to-end verification of the full feature.

**Step 1:** Build and deploy locally:

```bash
cd /home/lns/Docker-Sentinel
go test ./...
make lint
docker build -t docker-sentinel:cluster-ui .
```

**Step 2:** Deploy on .57 with cluster disabled (default). Verify:
- Settings page has Cluster tab
- No Cluster nav link visible
- Dashboard unchanged

**Step 3:** Enable cluster via Settings → Cluster tab. Verify:
- Toggle starts gRPC server (check logs)
- Cluster nav link appears (after page reload)
- Cluster page shows empty state ("No hosts enrolled yet")
- Enrollment token generation works

**Step 4:** If test cluster (.60/.61/.62) is available, enroll an agent and verify:
- Host card appears on Cluster page
- SSE updates card status in real-time
- Dashboard shows host groups with remote containers
- Queue shows host badges on remote container updates

**Step 5:** Verify Playwright screenshots of each page.

**Step 6:** Final commit and push to Gitea.

```bash
git push origin main
```

---

## Parallelisation Strategy

Tasks can be parallelised as follows:

**Sequential (must be in order):**
- Task 1 (store keys) → Task 3 (settings API) → Task 4 (dynamic lifecycle)

**Parallel after Task 2:**
- Task 5 (SSE events) — independent
- Task 6 (settings tab template) — depends on Task 3 API
- Task 10 (nav links) — depends on Task 2 only

**Parallel after Task 4:**
- Task 7 (cluster page) — depends on Tasks 3, 4
- Task 8 (dashboard host groups) — depends on Task 2

**After all others:**
- Task 9 (queue/history badges) — small, depends on Task 8 pattern
- Task 11 (integration test) — depends on everything
