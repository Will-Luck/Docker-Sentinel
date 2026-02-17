# Cluster Management UI Implementation Plan (Revised)

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a web UI for cluster management — Settings tab to enable/configure, standalone Cluster page with host cards and enrollment, dashboard host groups for remote containers.

**Architecture:** A thread-safe `ClusterController` proxy sits in `web.Dependencies` (always non-nil), delegating to the real `clusterAdapter` when active. This avoids the value-copy problem (Dependencies is stored by value in `Server`). Routes are registered unconditionally; handlers check `controller.Enabled()`. Settings persist to BoltDB with validation. Dynamic start/stop toggles the gRPC server without container restart.

**Tech Stack:** Go 1.24, BoltDB, vanilla HTML/CSS/JS, SSE, gRPC (existing cluster server)

**Design doc:** `docs/plans/2026-02-17-cluster-ui-design.md`

**Key insight (from Codex review):** `web.NewServer(deps)` copies `Dependencies` by value (`server.go:449`). Mutating `deps.Cluster` after construction is invisible to the running server. The `ClusterController` wrapper solves this by being a stable pointer that swaps its internal provider atomically.

---

## Task 1: ClusterController — Thread-Safe Provider Proxy

Create a `ClusterController` type that wraps `ClusterProvider` with mutex protection. This is always non-nil in `Dependencies`, so cluster routes can be registered unconditionally.

**Files:**
- Create: `internal/web/cluster_controller.go`
- Modify: `internal/web/server.go:28-64` (Dependencies struct — change `Cluster` type)
- Modify: `internal/web/server.go:655-662` (remove nil-check on route registration)
- Modify: `internal/web/handlers.go` (existing cluster handlers — use `Enabled()` check)

**Step 1:** Create `cluster_controller.go`:

```go
package web

import (
	"context"
	"sync"
)

// ClusterController is a thread-safe proxy around ClusterProvider.
// Always non-nil in Dependencies — the internal provider is swapped
// atomically when cluster mode is toggled at runtime.
type ClusterController struct {
	mu       sync.RWMutex
	provider ClusterProvider // nil when cluster is disabled
}

// NewClusterController creates a controller with no active provider.
func NewClusterController() *ClusterController {
	return &ClusterController{}
}

// SetProvider swaps the underlying provider. Pass nil to disable.
func (c *ClusterController) SetProvider(p ClusterProvider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.provider = p
}

// Enabled returns true if a provider is active.
func (c *ClusterController) Enabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.provider != nil
}

// AllHosts delegates to the active provider, or returns nil.
func (c *ClusterController) AllHosts() []ClusterHost {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return nil
	}
	return c.provider.AllHosts()
}

// GetHost delegates to the active provider.
func (c *ClusterController) GetHost(id string) (ClusterHost, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return ClusterHost{}, false
	}
	return c.provider.GetHost(id)
}

// ConnectedHosts delegates to the active provider.
func (c *ClusterController) ConnectedHosts() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return nil
	}
	return c.provider.ConnectedHosts()
}

// GenerateEnrollToken delegates to the active provider.
func (c *ClusterController) GenerateEnrollToken() (string, string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return "", "", fmt.Errorf("cluster not enabled")
	}
	return c.provider.GenerateEnrollToken()
}

// RemoveHost delegates to the active provider.
func (c *ClusterController) RemoveHost(id string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return fmt.Errorf("cluster not enabled")
	}
	return c.provider.RemoveHost(id)
}

// RevokeHost delegates to the active provider.
func (c *ClusterController) RevokeHost(id string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return fmt.Errorf("cluster not enabled")
	}
	return c.provider.RevokeHost(id)
}

// DrainHost delegates to the active provider.
func (c *ClusterController) DrainHost(id string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return fmt.Errorf("cluster not enabled")
	}
	return c.provider.DrainHost(id)
}

// UpdateRemoteContainer delegates to the active provider.
func (c *ClusterController) UpdateRemoteContainer(ctx context.Context, hostID, containerName, targetImage, targetDigest string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return fmt.Errorf("cluster not enabled")
	}
	return c.provider.UpdateRemoteContainer(ctx, hostID, containerName, targetImage, targetDigest)
}
```

**Step 2:** In `server.go`, change `Dependencies.Cluster` from `ClusterProvider` (interface) to `*ClusterController`:

```go
type Dependencies struct {
	// ... existing fields ...
	Cluster *ClusterController // always non-nil — wraps real provider
}
```

**Step 3:** In `registerRoutes()`, remove the `if s.deps.Cluster != nil` guard. Register cluster routes unconditionally:

```go
// Cluster management — always registered; handlers check Enabled().
s.mux.Handle("GET /api/cluster/hosts", perm(auth.PermSettingsModify, s.handleClusterHosts))
s.mux.Handle("POST /api/cluster/enroll-token", perm(auth.PermSettingsModify, s.handleGenerateEnrollToken))
s.mux.Handle("DELETE /api/cluster/hosts/{id}", perm(auth.PermSettingsModify, s.handleRemoveHost))
s.mux.Handle("POST /api/cluster/hosts/{id}/revoke", perm(auth.PermSettingsModify, s.handleRevokeHost))
s.mux.Handle("POST /api/cluster/hosts/{id}/drain", perm(auth.PermSettingsModify, s.handleDrainHost))
```

**Step 4:** Update existing cluster handlers (`handleClusterHosts`, `handleGenerateEnrollToken`, etc.) to check `s.deps.Cluster.Enabled()` instead of `s.deps.Cluster != nil`:

```go
func (s *Server) handleClusterHosts(w http.ResponseWriter, r *http.Request) {
	if !s.deps.Cluster.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "cluster not enabled")
		return
	}
	// ... rest unchanged ...
}
```

**Step 5:** Update `cmd/sentinel/main.go` to always create the controller:

```go
clusterCtrl := web.NewClusterController()

// If cluster was running at startup, wire it up:
if clusterSrv != nil {
	clusterCtrl.SetProvider(&clusterAdapter{srv: clusterSrv})
}

webDeps := web.Dependencies{
	// ... existing fields ...
	Cluster: clusterCtrl,
}
```

**Step 6:** Write tests for `ClusterController` — verify thread safety, nil provider returns, provider swap.

**Step 7:** Commit.

```bash
git add internal/web/cluster_controller.go internal/web/cluster_controller_test.go internal/web/server.go internal/web/handlers.go cmd/sentinel/main.go
git commit -m "feat(web): ClusterController — thread-safe provider proxy for dynamic lifecycle"
```

---

## Task 2: Cluster Config Store + Validated Settings API

Add BoltDB keys, GET/POST endpoints with input validation, and lifecycle hooks.

**Files:**
- Modify: `internal/store/bolt.go` (setting key constants)
- Modify: `internal/web/api_settings.go` (new handlers)
- Modify: `internal/web/server.go` (register routes, ClusterLifecycle interface)

**Step 1:** Define cluster config keys in `internal/store/bolt.go` near existing setting constants:

```go
// Cluster settings keys (stored in bucketSettings).
const (
	SettingClusterEnabled      = "cluster_enabled"       // "true" / "false"
	SettingClusterPort         = "cluster_port"          // e.g. "9443"
	SettingClusterGracePeriod  = "cluster_grace_period"  // e.g. "30m"
	SettingClusterRemotePolicy = "cluster_remote_policy" // "auto" / "manual" / "pinned"
)
```

**Step 2:** Add `ClusterLifecycle` interface and field to `Server` in `server.go`:

```go
// ClusterLifecycle allows the settings API to start/stop the cluster
// server at runtime without restarting the container.
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

// SetClusterLifecycle wires the dynamic start/stop callback.
func (s *Server) SetClusterLifecycle(cl ClusterLifecycle) {
	s.clusterLifecycle = cl
}
```

**Step 3:** Add GET handler in `api_settings.go`:

```go
func (s *Server) apiClusterSettings(w http.ResponseWriter, _ *http.Request) {
	defaults := map[string]string{
		"enabled":       "false",
		"port":          "9443",
		"grace_period":  "30m",
		"remote_policy": "manual",
	}
	if s.deps.SettingsStore != nil {
		for key := range defaults {
			fullKey := "cluster_" + key
			if v, err := s.deps.SettingsStore.LoadSetting(fullKey); err == nil && v != "" {
				defaults[key] = v
			}
		}
	}
	writeJSON(w, http.StatusOK, defaults)
}
```

**Step 4:** Add POST handler with **validation** (port range, duration parse, policy whitelist):

```go
func (s *Server) apiClusterSettingsSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled      *bool  `json:"enabled"`
		Port         string `json:"port"`
		GracePeriod  string `json:"grace_period"`
		RemotePolicy string `json:"remote_policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusInternalServerError, "settings store not available")
		return
	}

	// Validate port.
	if req.Port != "" {
		p, err := strconv.Atoi(req.Port)
		if err != nil || p < 1024 || p > 65535 {
			writeError(w, http.StatusBadRequest, "port must be 1024-65535")
			return
		}
	}

	// Validate grace period.
	if req.GracePeriod != "" {
		allowed := map[string]bool{"5m": true, "15m": true, "30m": true, "1h": true, "2h": true}
		if !allowed[req.GracePeriod] {
			writeError(w, http.StatusBadRequest, "invalid grace period")
			return
		}
	}

	// Validate remote policy.
	if req.RemotePolicy != "" {
		allowed := map[string]bool{"auto": true, "manual": true, "pinned": true}
		if !allowed[req.RemotePolicy] {
			writeError(w, http.StatusBadRequest, "invalid remote policy — must be auto, manual, or pinned")
			return
		}
	}

	// Save each provided field, checking for errors.
	save := func(key, val string) error {
		if err := s.deps.SettingsStore.SaveSetting(key, val); err != nil {
			return err
		}
		return nil
	}

	if req.Enabled != nil {
		val := "false"
		if *req.Enabled {
			val = "true"
		}
		if err := save(store.SettingClusterEnabled, val); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	if req.Port != "" {
		if err := save(store.SettingClusterPort, req.Port); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	if req.GracePeriod != "" {
		if err := save(store.SettingClusterGracePeriod, req.GracePeriod); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	if req.RemotePolicy != "" {
		if err := save(store.SettingClusterRemotePolicy, req.RemotePolicy); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}

	// Dynamic start/stop via ClusterLifecycle callback.
	if req.Enabled != nil && s.clusterLifecycle != nil {
		if *req.Enabled {
			if err := s.clusterLifecycle.Start(); err != nil {
				// Rollback: save disabled state since start failed.
				_ = s.deps.SettingsStore.SaveSetting(store.SettingClusterEnabled, "false")
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

**Step 5:** Register routes unconditionally (cluster settings should be accessible even when cluster is off — that's how you enable it):

```go
// Cluster settings — always available (admin only).
s.mux.Handle("GET /api/settings/cluster", perm(auth.PermSettingsModify, s.apiClusterSettings))
s.mux.Handle("POST /api/settings/cluster", perm(auth.PermSettingsModify, s.apiClusterSettingsSave))
```

**Step 6:** Write tests for validation (bad port, invalid policy, invalid grace period).

**Step 7:** Commit.

```bash
git add internal/store/bolt.go internal/web/api_settings.go internal/web/server.go
git commit -m "feat(web): cluster settings API with validation and lifecycle hooks"
```

---

## Task 3: Dynamic Cluster Lifecycle (main.go + adapters.go)

Wire the `clusterManager` in `main.go` that implements `ClusterLifecycle` and uses `ClusterController.SetProvider()` for runtime swaps.

**Files:**
- Modify: `cmd/sentinel/adapters.go` (new `clusterManager` struct)
- Modify: `cmd/sentinel/main.go:280-352` (refactor cluster startup)

**Step 1:** Create `clusterManager` in `adapters.go`:

```go
// clusterManager implements web.ClusterLifecycle for dynamic cluster
// start/stop from the settings API. Uses ClusterController.SetProvider()
// to swap the active provider atomically — no value-copy issues.
type clusterManager struct {
	mu       sync.Mutex
	srv      *clusterserver.Server
	db       *store.Store
	bus      *events.Bus
	log      *slog.Logger
	updater  *engine.Updater
	ctrl     *web.ClusterController // stable pointer in Dependencies
	dataDir  string                 // from config or env
}

func (m *clusterManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.srv != nil {
		return nil // already running
	}

	// Read port from DB, fall back to default.
	port, _ := m.db.LoadSetting(store.SettingClusterPort)
	if port == "" {
		port = "9443"
	}

	ca, err := cluster.EnsureCA(m.dataDir)
	if err != nil {
		return fmt.Errorf("initialise CA: %w", err)
	}

	m.srv = clusterserver.New(ca, m.db, m.bus, m.log)

	addr := net.JoinHostPort("", port)
	if err := m.srv.Start(addr); err != nil {
		m.srv = nil
		return fmt.Errorf("start gRPC: %w", err)
	}

	// Wire cluster scanner for multi-host scanning.
	m.updater.SetClusterScanner(&clusterScannerAdapter{srv: m.srv})

	// Swap provider in controller — handlers see it immediately.
	m.ctrl.SetProvider(&clusterAdapter{srv: m.srv})

	m.log.Info("cluster gRPC server started", "addr", addr)
	return nil
}

func (m *clusterManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.srv == nil {
		return
	}

	// Clear provider first so handlers stop dispatching.
	m.ctrl.SetProvider(nil)
	m.updater.SetClusterScanner(nil)

	m.srv.Stop()
	m.srv = nil

	m.log.Info("cluster gRPC server stopped")
}
```

**Step 2:** Refactor `main.go` cluster startup block:

```go
clusterCtrl := web.NewClusterController()

// Determine cluster data directory.
clusterDataDir := cfg.ClusterDir
if clusterDataDir == "" {
	clusterDataDir = "/data/cluster"
}

cm := &clusterManager{
	db:      db,
	bus:     bus,
	log:     log.Logger,
	updater: updater,
	ctrl:    clusterCtrl,
	dataDir: clusterDataDir,
}

// Check if cluster should start (env var overrides DB setting).
clusterEnabled := cfg.ClusterEnabled
if !clusterEnabled && db != nil {
	if v, _ := db.LoadSetting(store.SettingClusterEnabled); v == "true" {
		clusterEnabled = true
	}
}

if clusterEnabled {
	if err := cm.Start(); err != nil {
		log.Error("failed to start cluster", "error", err)
		os.Exit(1)
	}
	defer cm.Stop()
}

// Web deps — Cluster is always the stable controller pointer.
webDeps := web.Dependencies{
	// ... existing fields ...
	Cluster: clusterCtrl,
}

srv := web.NewServer(webDeps)
srv.SetClusterLifecycle(cm)
```

**Step 3:** Commit.

```bash
git add cmd/sentinel/main.go cmd/sentinel/adapters.go
git commit -m "feat: clusterManager — dynamic lifecycle with atomic provider swap"
```

---

## Task 4: Nav/Data-Struct Plumbing — ClusterEnabled Across All Pages

Add `ClusterEnabled` field to every data struct used by pages with nav bars. Create `withCluster()` helper.

**Files:**
- Modify: `internal/web/handlers.go` (pageData, containerDetailData, serviceDetailData, withCluster helper)
- Modify: All `.html` templates (conditional nav link)

**Step 1:** Add `ClusterEnabled` to `pageData` (line ~39):

```go
type pageData struct {
	// ... existing fields ...
	ClusterEnabled bool
	ClusterHosts   []ClusterHost // populated only on /cluster page
	HostGroups     []hostGroup   // populated only on dashboard when cluster active
}
```

**Step 2:** Add `ClusterEnabled` to `containerDetailData` (line ~575):

```go
type containerDetailData struct {
	// ... existing fields ...
	ClusterEnabled bool
}
```

**Step 3:** Add `ClusterEnabled` to `serviceDetailData` (line ~725):

```go
type serviceDetailData struct {
	// ... existing fields ...
	ClusterEnabled bool
}
```

**Step 4:** Create `withCluster` helper:

```go
// withCluster populates ClusterEnabled by checking the controller
// and falling back to the DB setting.
func (s *Server) withCluster(data *pageData) {
	if s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		data.ClusterEnabled = true
		return
	}
	if s.deps.SettingsStore != nil {
		v, _ := s.deps.SettingsStore.LoadSetting(store.SettingClusterEnabled)
		data.ClusterEnabled = v == "true"
	}
}
```

**Step 5:** Call `s.withCluster(&data)` in every handler that builds `pageData`: `handleDashboard`, `handleQueue`, `handleHistory`, `handleSettings`, `handleLogs`, `handleAccount`.

**Step 6:** In `handleContainerDetail` and `handleServiceDetail`, set `ClusterEnabled` the same way:

```go
// In handleContainerDetail:
data.ClusterEnabled = s.deps.Cluster != nil && s.deps.Cluster.Enabled()

// In handleServiceDetail:
data.ClusterEnabled = s.deps.Cluster != nil && s.deps.Cluster.Enabled()
```

**Step 7:** In every `.html` template's nav-links div, add after the Settings link:

```html
{{if .ClusterEnabled}}<a href="/cluster" class="nav-link{{if eq .Page "cluster"}} active{{end}}">Cluster</a>{{end}}
```

Templates to modify (11 files): `index.html`, `queue.html`, `history.html`, `settings.html`, `logs.html`, `account.html`, `container.html`, `service.html`, `login.html`, `setup.html`, `error.html`.

For `login.html`, `setup.html`, and `error.html` — these use `errorPageData` or minimal structs without `ClusterEnabled`. Since Go templates treat missing/zero-value bool as false, `{{if .ClusterEnabled}}` is safe and will simply not render the link. No struct changes needed for these.

**Step 8:** Commit.

```bash
git add internal/web/handlers.go internal/web/static/*.html
git commit -m "feat(web): ClusterEnabled nav link across all page templates"
```

---

## Task 5: Remote Container Data Model

Extend `ClusterProvider` to expose remote container details (not just counts), and add `HostID`/`HostName` to `UpdateRecord` for history.

**Files:**
- Modify: `internal/web/server.go:215-248` (ClusterProvider, ClusterHost)
- Modify: `cmd/sentinel/adapters.go` (clusterAdapter — expose container lists)
- Modify: `internal/store/bolt.go:37-49` (UpdateRecord — add host fields)
- Modify: `internal/web/handlers.go` (containerView — add host fields)

**Step 1:** Add `RemoteContainer` struct and `AllHostContainers` method to `ClusterProvider`:

```go
// RemoteContainer represents a container on a remote host.
type RemoteContainer struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"` // "running", "exited", etc.
	HostID  string `json:"host_id"`
	HostName string `json:"host_name"`
}

type ClusterProvider interface {
	// ... existing methods ...

	// AllHostContainers returns containers from all connected hosts.
	AllHostContainers() []RemoteContainer
}
```

**Step 2:** Implement `AllHostContainers` in `clusterAdapter` (`adapters.go`):

```go
func (a *clusterAdapter) AllHostContainers() []web.RemoteContainer {
	var result []web.RemoteContainer
	for _, info := range a.srv.AllHosts() {
		hs, ok := a.srv.GetHost(info.ID)
		if !ok {
			continue
		}
		for _, c := range hs.Containers {
			result = append(result, web.RemoteContainer{
				Name:     c.Name,
				Image:    c.Image,
				State:    c.State,
				HostID:   info.ID,
				HostName: info.Name,
			})
		}
	}
	return result
}
```

Also add to `ClusterController.AllHostContainers()`:

```go
func (c *ClusterController) AllHostContainers() []RemoteContainer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.provider == nil {
		return nil
	}
	return c.provider.AllHostContainers()
}
```

**Step 3:** Add host fields to `store.UpdateRecord`:

```go
type UpdateRecord struct {
	// ... existing fields ...
	HostID   string `json:"host_id,omitempty"`
	HostName string `json:"host_name,omitempty"`
}
```

This is backwards-compatible — existing records in BoltDB have no host fields, which deserialize as empty strings.

**Step 4:** Add host fields to `containerView` in `handlers.go`:

```go
type containerView struct {
	// ... existing fields ...
	HostID   string
	HostName string
}
```

**Step 5:** Commit.

```bash
git add internal/web/server.go internal/web/cluster_controller.go cmd/sentinel/adapters.go internal/store/bolt.go internal/web/handlers.go
git commit -m "feat: remote container data model — host fields on containers, history, and provider"
```

---

## Task 6: Queue Key Refactor — Support Remote Containers

The queue uses `hostID::name` as keys for remote containers (`engine/queue.go:73`), but approve/reject APIs use plain `name` from URL path (`api_queue.go:22`). Fix APIs to use the full key.

**Files:**
- Modify: `internal/web/api_queue.go` (approve/reject/ignore — use key parameter)
- Modify: `internal/web/server.go` (route patterns)
- Modify: `internal/web/static/queue.html` (JS sends full key)

**Step 1:** Change route patterns from `{name}` to `{key}` and use URL path that can contain `::`:

```go
// In registerRoutes:
s.mux.Handle("POST /api/queue/{key}/approve", perm(auth.PermQueueManage, s.apiApprove))
s.mux.Handle("POST /api/queue/{key}/reject", perm(auth.PermQueueManage, s.apiReject))
s.mux.Handle("POST /api/queue/{key}/ignore-version", perm(auth.PermQueueManage, s.apiIgnoreVersion))
```

**Step 2:** Update handlers to use `r.PathValue("key")`:

```go
func (s *Server) apiApprove(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "container key required")
		return
	}

	// Extract container name for protection check (strip hostID:: prefix).
	name := key
	if idx := strings.Index(key, "::"); idx >= 0 {
		name = key[idx+2:]
	}

	if s.isProtectedContainer(r.Context(), name) {
		writeError(w, http.StatusForbidden, "cannot approve updates for sentinel itself")
		return
	}

	update, ok := s.deps.Queue.Approve(key)
	// ... rest unchanged, using key instead of name ...
}
```

**Step 3:** Update `queue.html` JS to send the full key (which includes `hostID::` prefix for remote containers):

```javascript
// When rendering queue items, use item.Key (not item.ContainerName) for API calls
function approveUpdate(key) {
    fetch(`/api/queue/${encodeURIComponent(key)}/approve`, {
        method: 'POST',
        headers: {'X-CSRF-Token': csrfToken}
    })
    // ...
}
```

**Step 4:** Ensure `PendingUpdate` exposes `Key()` in the template or JSON response. Add `Key string` to the JSON marshalling or compute it in the template data.

**Step 5:** Commit.

```bash
git add internal/web/api_queue.go internal/web/server.go internal/web/static/queue.html
git commit -m "fix(web): queue API uses full key (hostID::name) for remote containers"
```

---

## Task 7: Settings — Cluster Tab (HTML/CSS/JS)

Add the Cluster tab to the Settings page.

**Files:**
- Modify: `internal/web/static/settings.html:66-76` (tab nav + panel)
- Modify: `internal/web/static/style.css` (cluster-fields disabled state)

**Step 1:** Add the Cluster tab button after the existing tabs:

```html
<button class="tab-btn" role="tab" aria-selected="false" aria-controls="tab-cluster" data-tab="cluster">Cluster</button>
```

**Step 2:** Add the tab panel (see design doc for full layout — toggle, port, grace period, default policy).

**Step 3:** Add JS for `loadClusterSettings()`, `saveClusterSettings()`, `onClusterToggle()` with confirmation dialog when disabling.

**Step 4:** Add CSS for `.cluster-fields.disabled` (opacity + pointer-events).

**Step 5:** Verify visually with Playwright screenshot.

**Step 6:** Commit.

```bash
git add internal/web/static/settings.html internal/web/static/style.css
git commit -m "feat(ui): cluster settings tab — enable/configure cluster mode"
```

---

## Task 8: Cluster Page — Host Cards & Enrollment

Create the `/cluster` page with host cards, stat row, and enrollment UI.

**Files:**
- Create: `internal/web/static/cluster.html`
- Modify: `internal/web/handlers.go` (new `handleCluster` handler)
- Modify: `internal/web/server.go` (register page route)
- Modify: `internal/web/static/style.css` (host card styles)

**Step 1:** Add `handleCluster` handler:

```go
func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	if !s.deps.Cluster.Enabled() {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	data := pageData{
		Page:       "cluster",
		QueueCount: len(s.deps.Queue.List()),
	}
	data.ClusterHosts = s.deps.Cluster.AllHosts()
	s.withAuth(r, &data)
	s.withCluster(&data)
	s.renderTemplate(w, "cluster.html", data)
}
```

**Step 2:** Register route unconditionally (handler redirects if disabled):

```go
s.mux.Handle("GET /cluster", perm(auth.PermSettingsModify, s.handleCluster))
```

**Step 3:** Create `cluster.html` — stat cards, host cards grid, enrollment section. SSE listener for `cluster_host` events (existing `EventClusterHost` type — **do NOT create new event types**). Approximately 300-400 lines following patterns in `index.html`.

**Step 4:** Add host card CSS (grid, status dots, card layout, enrollment section, token display).

**Step 5:** Verify visually with Playwright.

**Step 6:** Commit.

```bash
git add internal/web/static/cluster.html internal/web/handlers.go internal/web/server.go internal/web/static/style.css
git commit -m "feat(ui): cluster page — host cards, enrollment, lifecycle actions"
```

---

## Task 9: Dashboard Host Groups

When cluster is active, add host-level grouping above existing stack groups.

**Files:**
- Modify: `internal/web/handlers.go` (handleDashboard — build hostGroups)
- Modify: `internal/web/static/index.html` (host-level accordion)
- Modify: `internal/web/static/style.css` (host header styles)

**Step 1:** Define `hostGroup` struct in `handlers.go`:

```go
type hostGroup struct {
	ID         string
	Name       string
	Connected  bool
	Stacks     []stackGroup
	Count      int
}
```

**Step 2:** In `handleDashboard`, when cluster is active, build host groups:

```go
if s.deps.Cluster.Enabled() {
	// "local" group — this server's containers.
	localGroup := hostGroup{
		ID:        "local",
		Name:      "local",
		Connected: true,
		Stacks:    data.Stacks, // existing stack groups
		Count:     len(data.Containers),
	}
	data.HostGroups = []hostGroup{localGroup}

	// Remote host groups from cluster provider.
	remoteContainers := s.deps.Cluster.AllHostContainers()
	// Group remote containers by host, build stack groups per host...
	// ... (group by HostID, then by stack label within each host)
	data.HostGroups = append(data.HostGroups, remoteGroups...)

	// Update stat cards with fleet-wide totals.
	data.TotalContainers += remoteTotal
	data.RunningContainers += remoteRunning
}
```

**Step 3:** In `index.html`, wrap existing stack rendering in host-level accordion when `HostGroups` is populated:

```html
{{if .HostGroups}}
    {{range .HostGroups}}
    <tbody class="host-group" data-host="{{.ID}}">
        <tr class="host-header" onclick="toggleHostGroup(this)">
            <td colspan="6">
                <span class="host-toggle">&#9656;</span>
                <span class="host-status-dot {{if .Connected}}connected{{else}}disconnected{{end}}"></span>
                <span class="host-name">{{.Name}}</span>
                <span class="host-count">{{.Count}}</span>
            </td>
        </tr>
        {{range .Stacks}}
            <!-- existing stack group rendering -->
        {{end}}
    </tbody>
    {{end}}
{{else}}
    <!-- existing non-cluster rendering (unchanged) -->
{{end}}
```

**Step 4:** Add JS for `toggleHostGroup()` and CSS for `.host-header`.

**Step 5:** Verify visually with Playwright.

**Step 6:** Commit.

```bash
git add internal/web/handlers.go internal/web/static/index.html internal/web/static/style.css
git commit -m "feat(ui): dashboard host groups — containers grouped by host"
```

---

## Task 10: Queue & History Host Badges

Show which host a pending update or history entry belongs to.

**Files:**
- Modify: `internal/web/static/queue.html` (host badge)
- Modify: `internal/web/static/history.html` (host badge)
- Modify: `internal/web/static/style.css` (badge styles)

**Step 1:** In `queue.html`, add host badge next to container name for remote entries:

```html
{{if .HostName}}<span class="host-badge" title="Host: {{.HostName}}">{{.HostName}}</span>{{end}}
```

**Step 2:** In `history.html`, same pattern using the new `HostName` field on `UpdateRecord`:

```html
{{if .HostName}}<span class="host-badge">{{.HostName}}</span>{{end}}
```

**Step 3:** Add CSS for `.host-badge` (small pill, purple theme matching existing style).

**Step 4:** Commit.

```bash
git add internal/web/static/queue.html internal/web/static/history.html internal/web/static/style.css
git commit -m "feat(ui): host badges on queue and history entries"
```

---

## Task 11: Integration Testing & Visual Verification

End-to-end verification of the full feature.

**Step 1:** Build and test:

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

**Step 3:** Enable cluster via Settings -> Cluster tab. Verify:
- Toggle starts gRPC server (check logs)
- Cluster nav link appears (after page reload)
- Cluster page shows empty state ("No hosts enrolled yet")
- Enrollment token generation works

**Step 4:** If test cluster (.60/.61/.62) is available, enroll an agent and verify:
- Host card appears on Cluster page
- SSE updates card status in real-time (using existing `cluster_host` event)
- Dashboard shows host groups with remote containers
- Queue shows host badges on remote container updates

**Step 5:** Verify Playwright screenshots of each page.

**Step 6:** Final commit and push to Gitea.

```bash
git push origin main
```

---

## Parallelisation Strategy

**Phase 1 (sequential — foundation):**
- Task 1 (ClusterController) -> Task 2 (Settings API) -> Task 3 (Dynamic Lifecycle)

**Phase 2 (parallel — can run after Phase 1):**
- Task 4 (Nav plumbing across all templates)
- Task 5 (Remote container data model)
- Task 6 (Queue key refactor)

**Phase 3 (parallel — after Phase 2):**
- Task 7 (Settings tab HTML)
- Task 8 (Cluster page)

**Phase 4 (after Phase 2 + 3):**
- Task 9 (Dashboard host groups)
- Task 10 (Queue/history badges)

**Phase 5 (after all):**
- Task 11 (Integration testing)

---

## Codex Review Findings Addressed

| Codex Finding | Resolution |
|---------------|------------|
| Value-copy blocker: `NewServer(deps)` copies by value | Task 1: `ClusterController` proxy — always non-nil, swaps provider atomically |
| Routes conditional on nil check | Task 1: Register unconditionally, handlers check `Enabled()` |
| Missing validation in Settings POST | Task 2: Port range, grace period whitelist, policy whitelist validation |
| `SaveSetting` errors ignored | Task 2: Check and return 5xx on save failures |
| Lifecycle rollback on start failure | Task 2: Rollback enabled state if `Start()` fails |
| Handler names wrong in plan | Task 4: Uses correct `handleContainerDetail`/`handleServiceDetail` |
| SSE type mismatch (`events.Event` vs `events.SSEEvent`) | Task 8: Uses existing `EventClusterHost` and `SSEEvent` — no new types |
| `SettingClusterDataDir` doesn't exist | Task 3: Uses `cfg.ClusterDir` from env, not a DB setting |
| `ClusterProvider` lacks container details | Task 5: Added `AllHostContainers()` method and `RemoteContainer` struct |
| `clusterAdapter` only exposes counts | Task 5: Extended to expose full container lists |
| Queue key mismatch (approve by name vs hostID::name) | Task 6: APIs use full key with `::` support |
| History record has no host field | Task 5: Added `HostID`/`HostName` to `UpdateRecord` |
| `SetClusterScanner` unsynchronized | Task 3: Called under `clusterManager.mu` lock, before any scanning starts |
| `containerDetailData`/`serviceDetailData` missing ClusterEnabled | Task 4: Added to both structs |
| Data races on provider swap | Task 1: `ClusterController` uses `sync.RWMutex` throughout |
| Connect/disconnect events already exist | Task 8: Reuses `EventClusterHost` — no new event types |
