# Setup Wizard Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the single-screen admin setup with a multi-step first-run wizard that configures instance role (server/agent), credentials, and core settings — plus an agent mini-UI and Portainer-style enrollment snippets.

**Architecture:** Every container starts a web server. Fresh installs redirect to a stepped wizard (Audible-Sync pattern). The wizard persists `instance_role` to BoltDB, then the process re-execs into the chosen mode. Agents keep a minimal status page. Env vars remain supported as overrides for automation.

**Tech Stack:** Go 1.24, BoltDB, embedded HTML templates, vanilla JS, existing CSS design system

---

## Task 1: Config & DB foundation for `instance_role`

**Files:**
- Modify: `internal/config/config.go:136-180` (Validate)
- Modify: `internal/config/config.go:54` (Mode field)
- Modify: `cmd/sentinel/main.go:55-101` (startup logic)

**Step 1: Relax agent mode validation**

Currently `config.go:168-178` rejects agent mode if `WebEnabled` is true or `ServerAddr` is empty. The wizard needs to start a web server before the role is known, so validation must be deferred until after the wizard completes.

In `config.go`, change the agent validation block:

```go
// Cluster mode validation.
if c.Mode != "" && c.Mode != "server" && c.Mode != "agent" {
    errs = append(errs, fmt.Errorf("SENTINEL_MODE must be 'server' or 'agent', got %q", c.Mode))
}
if c.Mode == "agent" {
    if c.ServerAddr == "" && c.EnrollToken == "" {
        // Only require ServerAddr when NOT enrolling fresh — during wizard, these are set later.
        errs = append(errs, fmt.Errorf("SENTINEL_SERVER_ADDR is required in agent mode"))
    }
    // Remove the WebEnabled check — agents now run a minimal web UI.
}
```

**Step 2: Add `NeedsWizard` helper to detect first-run**

In `cmd/sentinel/main.go`, after DB open but before the server/agent branch, add a check:

```go
// Check if instance has been configured via the wizard.
instanceRole, _ := db.LoadSetting("instance_role")
needsWizard := instanceRole == ""

// Env var overrides: if SENTINEL_MODE is explicitly set AND auth_setup_complete is true,
// skip wizard (backwards compat for existing deployments).
if cfg.Mode != "" {
    if v, _ := db.LoadSetting("auth_setup_complete"); v == "true" {
        needsWizard = false
        if instanceRole == "" {
            // Backfill instance_role for existing deployments.
            _ = db.SaveSetting("instance_role", cfg.Mode)
        }
    }
}
```

**Step 3: Add wizard-mode web server startup**

When `needsWizard` is true, start a minimal web server that only serves the wizard:

```go
if needsWizard {
    fmt.Println("First-run setup required — starting setup wizard...")
    runWizard(ctx, cfg, db, log)
    // After wizard completes, re-exec to apply the chosen role.
    return
}
```

The `runWizard` function starts a minimal HTTP server with just `/setup`, `/static/*`, and `/api/setup/*` routes. After wizard completion, it writes `instance_role` to DB and re-execs the process.

**Step 4: Build and test**

Run: `go build ./...`

**Step 5: Commit**

```bash
git add internal/config/config.go cmd/sentinel/main.go
git commit -m "feat: add instance_role DB setting and wizard detection"
```

---

## Task 2: Wizard web server (`runWizard` function)

**Files:**
- Modify: `cmd/sentinel/main.go` (add `runWizard`)
- Create: `internal/web/wizard.go` (minimal wizard server)

**Step 1: Create wizard server**

`internal/web/wizard.go` — a stripped-down HTTP server that only serves the setup wizard. No auth middleware, no dashboard routes, no SSE.

```go
package web

import (
    "context"
    "encoding/json"
    "log/slog"
    "net"
    "net/http"
    "time"
)

// WizardDeps holds the minimal dependencies for the setup wizard server.
type WizardDeps struct {
    SettingsStore SettingsStore
    Auth          *auth.Service
    Log           *slog.Logger
    Version       string
    ClusterPort   string
}

// WizardServer serves only the first-run setup wizard.
type WizardServer struct {
    deps          WizardDeps
    mux           *http.ServeMux
    tmpl          *template.Template
    server        *http.Server
    setupDeadline time.Time
    done          chan struct{} // closed when wizard completes
}

func NewWizardServer(deps WizardDeps) *WizardServer {
    s := &WizardServer{
        deps:          deps,
        mux:           http.NewServeMux(),
        setupDeadline: time.Now().Add(5 * time.Minute),
        done:          make(chan struct{}),
    }
    s.parseTemplates()
    s.registerRoutes()
    return s
}
```

Routes: `GET /setup` (render wizard), `POST /api/setup` (submit wizard), `GET /static/*` (CSS/JS/images), redirect everything else to `/setup`.

**Step 2: Add `runWizard` to main.go**

```go
func runWizard(ctx context.Context, cfg *config.Config, db *store.DB, log *logging.Logger) {
    // Ensure auth buckets exist for user creation.
    if err := db.EnsureAuthBuckets(); err != nil {
        log.Error("failed to create auth buckets", "error", err)
        os.Exit(1)
    }

    authSvc := auth.NewService(auth.ServiceConfig{
        Users: db, Sessions: db, Roles: db, Tokens: db, Settings: db,
        Log: log.Logger, CookieSecure: cfg.CookieSecure, SessionExpiry: cfg.SessionExpiry,
    })

    ws := web.NewWizardServer(web.WizardDeps{
        SettingsStore: &settingsStoreAdapter{db},
        Auth:          authSvc,
        Log:           log.Logger,
        Version:       versionString(),
        ClusterPort:   cfg.ClusterPort,
    })

    addr := net.JoinHostPort("", cfg.WebPort)
    go func() {
        if err := ws.ListenAndServe(addr); err != nil && err != http.ErrServerClosed {
            log.Error("wizard server error", "error", err)
        }
    }()

    // Wait for wizard completion or shutdown.
    select {
    case <-ws.Done():
        log.Info("setup wizard completed")
    case <-ctx.Done():
        log.Info("shutdown during wizard")
    }
    ws.Shutdown(context.Background())
}
```

After `runWizard` returns, `main()` re-reads `instance_role` from DB and continues with the appropriate startup path.

**Step 3: Build and test**

Run: `go build ./...`

**Step 4: Commit**

```bash
git add internal/web/wizard.go cmd/sentinel/main.go
git commit -m "feat: add wizard web server for first-run setup"
```

---

## Task 3: Wizard HTML — multi-step UI

**Files:**
- Modify: `internal/web/static/setup.html` (full rewrite)

**Step 1: Rewrite setup.html as a stepped wizard**

Replace the single-card layout with a multi-step wizard. All steps in the DOM, JS shows/hides them. Progress dots at top (like Audible-Sync).

Steps:
- Step 0: Role selection (server vs agent) — two clickable cards
- Step 1: Admin account (username + password + confirm) — shared by both paths
- Step 2a (server): Core settings — policy dropdown, poll interval, cluster toggle
- Step 2b (agent): Server connection — address, enrollment token, host name, connect button
- Step 3 (server only): Optional extras — collapsible notification/TLS/registry sections
- Step 4: Done — success message + redirect button

Key CSS classes to reuse from existing design system: `.card`, `.btn`, `.btn-info`, `.btn-sm`, `.setting-input`, `.setting-select`, `.channel-toggle`, `.toggle-switch-label`, `.toggle-switch-text`.

New CSS for wizard-specific styles (progress dots, role cards, step panels) goes in a `<style>` block in setup.html, same pattern as the current setup.html.

**Step 2: Role selection cards**

Two cards side-by-side, clickable. On click, highlight the selected card and enable the Continue button. Store selection in a JS variable.

```html
<div class="wizard-roles">
    <div class="wizard-role-card" data-role="server" onclick="selectRole('server')">
        <div class="wizard-role-icon">
            <svg><!-- server icon --></svg>
        </div>
        <div class="wizard-role-title">Server</div>
        <div class="wizard-role-desc">Central dashboard that monitors and updates containers</div>
    </div>
    <div class="wizard-role-card" data-role="agent" onclick="selectRole('agent')">
        <div class="wizard-role-icon">
            <svg><!-- agent icon --></svg>
        </div>
        <div class="wizard-role-title">Agent</div>
        <div class="wizard-role-desc">Connects to a Sentinel server and runs updates on this host</div>
    </div>
</div>
```

**Step 3: Server settings step**

```html
<div class="wizard-pane" id="wizard-step-2a">
    <h3>Core Settings</h3>
    <div class="form-group">
        <label class="form-label">Default Update Policy</label>
        <div class="wizard-policy-cards">
            <div class="wizard-policy-card selected" data-policy="manual" onclick="selectPolicy('manual')">
                <strong>Manual</strong>
                <span>Review updates before applying</span>
            </div>
            <div class="wizard-policy-card" data-policy="auto" onclick="selectPolicy('auto')">
                <strong>Auto</strong>
                <span>Apply updates automatically</span>
            </div>
            <div class="wizard-policy-card" data-policy="pinned" onclick="selectPolicy('pinned')">
                <strong>Pinned</strong>
                <span>Never update unless manually triggered</span>
            </div>
        </div>
    </div>
    <div class="form-group">
        <label class="form-label">Scan Interval</label>
        <select class="form-input" id="wizard-poll-interval">
            <option value="15m">Every 15 minutes</option>
            <option value="30m">Every 30 minutes</option>
            <option value="1h">Every hour</option>
            <option value="6h" selected>Every 6 hours</option>
            <option value="12h">Every 12 hours</option>
            <option value="24h">Daily</option>
        </select>
    </div>
    <div class="form-group">
        <label class="form-label">
            <input type="checkbox" id="wizard-cluster-enabled"> Enable cluster mode
        </label>
        <span class="form-hint">Manage containers on remote hosts via agents</span>
    </div>
</div>
```

**Step 4: Agent connection step**

```html
<div class="wizard-pane" id="wizard-step-2b">
    <h3>Connect to Server</h3>
    <div class="form-group">
        <label class="form-label">Server Address</label>
        <input class="form-input" type="text" id="wizard-server-addr" placeholder="192.168.1.60:9443">
        <span class="form-hint">The Sentinel server's hostname or IP and gRPC port</span>
    </div>
    <div class="form-group">
        <label class="form-label">Enrollment Token</label>
        <input class="form-input" type="text" id="wizard-enroll-token" placeholder="Paste token from server">
    </div>
    <div class="form-group">
        <label class="form-label">Host Name</label>
        <input class="form-input" type="text" id="wizard-host-name" placeholder="my-agent">
        <span class="form-hint">A human-readable name for this host</span>
    </div>
    <div id="wizard-enroll-status" style="display:none"></div>
    <button class="setup-btn" onclick="testEnrollment()">Test Connection</button>
</div>
```

**Step 5: Optional extras step (server only)**

Collapsible accordion sections for notifications, TLS, registry credentials. Each section has its own save logic. "Skip all" button at the bottom.

**Step 6: Build and test**

Run: `go build ./...` (verifies template parses)

**Step 7: Commit**

```bash
git add internal/web/static/setup.html
git commit -m "feat: rewrite setup.html as multi-step wizard"
```

---

## Task 4: Wizard API handlers

**Files:**
- Modify: `internal/web/wizard.go` (add handlers)
- Modify: `internal/web/handlers_auth.go:131-237` (update `apiSetup`)

**Step 1: Expand setup API to accept role + settings**

The `POST /api/setup` endpoint currently accepts `{username, password}`. Expand it to accept the full wizard payload:

```go
type wizardRequest struct {
    // Step 1: Role
    Role string `json:"role"` // "server" or "agent"

    // Step 2: Credentials
    Username string `json:"username"`
    Password string `json:"password"`

    // Step 3a: Server settings
    DefaultPolicy  string `json:"default_policy,omitempty"`
    PollInterval   string `json:"poll_interval,omitempty"`
    ClusterEnabled bool   `json:"cluster_enabled,omitempty"`

    // Step 3b: Agent settings
    ServerAddr  string `json:"server_addr,omitempty"`
    EnrollToken string `json:"enroll_token,omitempty"`
    HostName    string `json:"host_name,omitempty"`
}
```

**Step 2: Server path handler**

On submit with `role=server`:
1. Validate username + password (same as current)
2. Create admin user (same as current)
3. Save settings: `instance_role=server`, `default_policy`, `poll_interval`
4. If `cluster_enabled`, save `cluster_enabled=true`
5. Mark `auth_setup_complete=true`
6. Create session + set cookie
7. Signal wizard completion (close `done` channel)

**Step 3: Agent path handler**

On submit with `role=agent`:
1. Validate username + password
2. Create admin user (for agent mini-UI login)
3. Save settings: `instance_role=agent`, `server_addr`, `host_name`
4. Mark `auth_setup_complete=true`
5. Create session + set cookie
6. Signal wizard completion

Note: actual enrollment happens after process re-exec into agent mode. The wizard just saves the config — the agent startup code reads `server_addr` and `enroll_token` from DB.

**Step 4: Add enrollment test endpoint**

`POST /api/setup/test-enrollment` — makes a quick gRPC dial to the server address to verify connectivity. Returns `{status: "ok"}` or `{error: "..."}`. This is for the "Test Connection" button in the agent wizard step.

```go
func (s *WizardServer) apiTestEnrollment(w http.ResponseWriter, r *http.Request) {
    var body struct {
        ServerAddr string `json:"server_addr"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        writeError(w, http.StatusBadRequest, "invalid request")
        return
    }
    // Attempt a TCP dial with 5-second timeout.
    conn, err := net.DialTimeout("tcp", body.ServerAddr, 5*time.Second)
    if err != nil {
        writeError(w, http.StatusBadGateway, "cannot reach server: "+err.Error())
        return
    }
    conn.Close()
    writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

**Step 5: Add optional extras endpoints**

`POST /api/setup/notifications` — saves a single notification channel during wizard.
`POST /api/setup/tls` — saves TLS preference (`auto`, `manual` with paths, or `none`).

These are optional — the wizard works without them.

**Step 6: Build and test**

Run: `go build ./...`
Run: `go test ./internal/web/ -run TestWizard -v`

**Step 7: Commit**

```bash
git add internal/web/wizard.go internal/web/handlers_auth.go
git commit -m "feat: wizard API handlers for server and agent paths"
```

---

## Task 5: Wizard JavaScript

**Files:**
- Modify: `internal/web/static/setup.html` (add `<script>` section)

**Step 1: Step navigation engine**

Client-side step management — show/hide wizard panes, update progress dots, handle back/next buttons.

```javascript
var wizardState = {
    step: 0,
    role: '',
    policy: 'manual',
    totalSteps: 4
};

function goToStep(n) {
    // Hide all panes
    var panes = document.querySelectorAll('.wizard-pane');
    for (var i = 0; i < panes.length; i++) panes[i].style.display = 'none';

    // Show target pane
    var targetId = 'wizard-step-' + n;
    // For step 2, show 2a (server) or 2b (agent) based on role
    if (n === 2) targetId = wizardState.role === 'agent' ? 'wizard-step-2b' : 'wizard-step-2a';
    // For step 3, skip for agents (go straight to done)
    if (n === 3 && wizardState.role === 'agent') { n = 4; targetId = 'wizard-step-4'; }

    document.getElementById(targetId).style.display = 'block';
    wizardState.step = n;
    updateProgressDots();
}
```

**Step 2: Form validation**

Client-side validation before advancing:
- Step 1: role must be selected
- Step 2: username non-empty, password >= 8 chars with letter + digit, confirm matches
- Step 3a: no required validation (defaults are fine)
- Step 3b: server address non-empty, token non-empty, host name non-empty

**Step 3: Form submission**

On final step, collect all form data and POST to `/api/setup`:

```javascript
function submitWizard() {
    var payload = {
        role: wizardState.role,
        username: document.getElementById('wizard-username').value,
        password: document.getElementById('wizard-password').value
    };
    if (wizardState.role === 'server') {
        payload.default_policy = wizardState.policy;
        payload.poll_interval = document.getElementById('wizard-poll-interval').value;
        payload.cluster_enabled = document.getElementById('wizard-cluster-enabled').checked;
    } else {
        payload.server_addr = document.getElementById('wizard-server-addr').value;
        payload.enroll_token = document.getElementById('wizard-enroll-token').value;
        payload.host_name = document.getElementById('wizard-host-name').value;
    }

    fetch('/api/setup', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(payload)
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
        if (data.error) { showError(data.error); return; }
        goToStep(4); // Done step
    });
}
```

**Step 4: Enrollment test button**

```javascript
function testEnrollment() {
    var addr = document.getElementById('wizard-server-addr').value;
    var btn = event.target;
    btn.disabled = true;
    btn.textContent = 'Testing...';

    fetch('/api/setup/test-enrollment', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({server_addr: addr})
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
        var el = document.getElementById('wizard-enroll-status');
        el.style.display = 'block';
        if (data.error) {
            el.className = 'setup-error';
            el.textContent = data.error;
        } else {
            el.className = 'wizard-success';
            el.textContent = 'Connection successful!';
        }
        btn.disabled = false;
        btn.textContent = 'Test Connection';
    });
}
```

**Step 5: Countdown timer**

Same as current setup.html — ticks down from `RemainingSeconds`, reloads page to show expired state when window closes.

**Step 6: Build and test**

Run: `go build ./...`

**Step 7: Commit**

```bash
git add internal/web/static/setup.html
git commit -m "feat: wizard client-side step navigation and form submission"
```

---

## Task 6: Process re-exec after wizard

**Files:**
- Modify: `cmd/sentinel/main.go`

**Step 1: Implement re-exec**

After `runWizard` returns, re-read the configured role from DB and continue the appropriate startup path. Rather than an actual `syscall.Exec` re-exec (which is complex with Docker), just fall through to the existing startup branches:

```go
if needsWizard {
    fmt.Println("First-run setup required — starting setup wizard...")
    runWizard(ctx, cfg, db, log)

    // Re-read config from DB after wizard.
    instanceRole, _ = db.LoadSetting("instance_role")
    if instanceRole == "" {
        // Wizard was cancelled or shutdown happened — exit cleanly.
        return
    }
    // Override cfg.Mode with wizard choice.
    cfg.Mode = instanceRole

    // Load wizard-saved settings into cfg.
    if instanceRole == "agent" {
        if v, _ := db.LoadSetting("server_addr"); v != "" {
            cfg.ServerAddr = v
        }
        if v, _ := db.LoadSetting("host_name"); v != "" {
            cfg.HostName = v
        }
        if v, _ := db.LoadSetting("enroll_token"); v != "" {
            cfg.EnrollToken = v
        }
        cfg.WebEnabled = true // Agent now keeps a minimal web UI
    }
}
```

**Step 2: Load wizard settings for server path**

```go
if instanceRole == "server" || instanceRole == "" {
    // Load wizard-saved settings.
    if v, _ := db.LoadSetting("default_policy"); v != "" {
        switch v {
        case "auto", "manual", "pinned":
            cfg.SetDefaultPolicy(v)
        }
    }
    if v, _ := db.LoadSetting("poll_interval"); v != "" {
        if d, err := time.ParseDuration(v); err == nil {
            cfg.SetPollInterval(d)
        }
    }
    if v, _ := db.LoadSetting(store.SettingClusterEnabled); v == "true" {
        cfg.ClusterEnabled = true
    }
}
```

Note: The existing settings-loading code in main.go already does most of this. The wizard just writes the same DB keys that the settings page writes, so the existing load-from-DB code picks them up naturally. We may need minimal additional loading for new fields.

**Step 3: Build and test**

Run: `go build ./...`

**Step 4: Commit**

```bash
git add cmd/sentinel/main.go
git commit -m "feat: continue startup with wizard-chosen role after wizard completes"
```

---

## Task 7: Agent mini-UI

**Files:**
- Create: `internal/web/static/agent.html`
- Create: `internal/web/agent_server.go`

**Step 1: Create agent.html template**

A single-page status dashboard. Styled with the existing CSS design system (imports style.css). No nav bar — just a centered card.

Content:
- Logo + "Sentinel Agent" heading
- Status card: connection status (green/red dot), server address, host name, container count, version
- Settings section: editable server address, host name, grace period fields
- Actions: "Switch to Server Mode" button, "Re-enroll" button
- Account section: "Change Password" button, "Logout" button

The page auto-refreshes agent status via a polling `GET /api/agent/status` call every 5 seconds.

**Step 2: Create agent_server.go**

A minimal web server for agent mode. Similar to wizard.go but persistent. Routes:

```go
// Public routes
s.mux.HandleFunc("GET /login", s.handleLogin)
s.mux.HandleFunc("POST /login", s.apiLogin)
s.mux.HandleFunc("GET /static/", s.serveStaticFile)
s.mux.HandleFunc("GET /favicon.svg", s.serveFavicon)

// Auth-required routes
s.mux.Handle("GET /{$}", authed(s.handleAgentDashboard))
s.mux.Handle("GET /api/agent/status", authed(s.apiAgentStatus))
s.mux.Handle("POST /api/agent/settings", authed(s.apiAgentSettings))
s.mux.Handle("POST /api/auth/change-password", authed(s.apiChangePassword))
s.mux.Handle("POST /logout", authed(s.handleLogout))
```

**Step 3: Agent status API**

`GET /api/agent/status` returns:
```json
{
    "connected": true,
    "server_addr": "192.168.1.60:9443",
    "host_name": "test-agent-2",
    "containers": 12,
    "version": "v2.3.0",
    "uptime": "2h15m"
}
```

The agent process exposes connection status via a shared state struct that the mini-UI reads.

**Step 4: Agent settings API**

`POST /api/agent/settings` accepts:
```json
{
    "server_addr": "192.168.1.60:9443",
    "host_name": "test-agent-2"
}
```

Saves to DB. Changes to server_addr show a "requires restart" banner.

**Step 5: Build and test**

Run: `go build ./...`

**Step 6: Commit**

```bash
git add internal/web/static/agent.html internal/web/agent_server.go
git commit -m "feat: add agent mini-UI with status dashboard"
```

---

## Task 8: Wire agent mini-UI into main.go

**Files:**
- Modify: `cmd/sentinel/main.go:466-498` (`runAgent` function)

**Step 1: Start agent web server alongside agent process**

Currently `runAgent` starts the agent gRPC client and blocks. Add a minimal web server alongside it:

```go
func runAgent(ctx context.Context, cfg *config.Config, log *logging.Logger) {
    // ... existing Docker client setup ...

    // Start agent mini-UI web server.
    db, err := store.Open(cfg.DBPath)
    if err != nil {
        log.Error("failed to open database", "error", err)
        os.Exit(1)
    }
    defer db.Close()

    if err := db.EnsureAuthBuckets(); err != nil {
        log.Error("failed to create auth buckets", "error", err)
        os.Exit(1)
    }

    authSvc := auth.NewService(auth.ServiceConfig{
        Users: db, Sessions: db, Roles: db, Tokens: db, Settings: db,
        Log: log.Logger, CookieSecure: cfg.CookieSecure, SessionExpiry: cfg.SessionExpiry,
    })

    agentWeb := web.NewAgentServer(web.AgentDeps{
        Auth:          authSvc,
        SettingsStore: db,
        Log:           log.Logger,
        Version:       versionString(),
    })

    go func() {
        addr := net.JoinHostPort("", cfg.WebPort)
        if err := agentWeb.ListenAndServe(addr); err != nil && err != http.ErrServerClosed {
            log.Error("agent web server error", "error", err)
        }
    }()

    // ... existing agent gRPC startup ...

    // Wire agent status into web server for the status API.
    agentWeb.SetStatusProvider(a)

    log.Info("starting agent mode", "server", cfg.ServerAddr, "host", cfg.HostName)
    if err := a.Run(ctx); err != nil {
        log.Error("agent exited with error", "error", err)
        os.Exit(1)
    }
}
```

**Step 2: Define AgentStatusProvider interface**

```go
// In agent_server.go:
type AgentStatusProvider interface {
    Connected() bool
    ContainerCount() int
}
```

The agent struct already tracks connection state — just expose it via this interface.

**Step 3: Build and test**

Run: `go build ./...`

**Step 4: Commit**

```bash
git add cmd/sentinel/main.go internal/web/agent_server.go
git commit -m "feat: start agent mini-UI web server in agent mode"
```

---

## Task 9: Enrollment snippet upgrade on cluster page

**Files:**
- Modify: `internal/web/static/cluster.html:124-166` (enrollment section)
- Modify: `internal/web/server.go:877-891` (`handleGenerateEnrollToken`)

**Step 1: Upgrade enrollment section HTML**

Replace the single `<pre>` with a tabbed snippet view (Docker Run / Docker Compose):

```html
<div id="token-display" style="display:none">
    <div class="token-display">
        <code id="token-value"></code>
        <button class="btn btn-sm" onclick="copyToken()">Copy Token</button>
    </div>

    <div class="form-group" style="margin: var(--sp-3) 0">
        <label class="form-label">Host Name</label>
        <input class="form-input" type="text" id="enroll-hostname" value="my-agent"
               oninput="updateSnippets()" style="max-width: 300px">
    </div>
    <div class="form-group" style="margin-bottom: var(--sp-3)">
        <label class="form-label">Agent Web Port</label>
        <input class="form-input" type="number" id="enroll-port" value="8080"
               oninput="updateSnippets()" style="max-width: 120px">
    </div>

    <div class="snippet-tabs">
        <button class="snippet-tab active" onclick="showSnippet('docker-run')">Docker Run</button>
        <button class="snippet-tab" onclick="showSnippet('docker-compose')">Docker Compose</button>
    </div>

    <div class="snippet-pane active" id="snippet-docker-run">
        <pre class="agent-command" id="snippet-run"></pre>
        <button class="btn btn-sm" onclick="copySnippet('snippet-run')">Copy</button>
    </div>
    <div class="snippet-pane" id="snippet-docker-compose">
        <pre class="agent-command" id="snippet-compose"></pre>
        <button class="btn btn-sm" onclick="copySnippet('snippet-compose')">Copy</button>
    </div>
</div>
```

**Step 2: Update generateToken JS**

```javascript
function generateToken() {
    fetch('/api/cluster/enroll-token', {
        method: 'POST',
        headers: {'X-CSRF-Token': csrfToken}
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
        if (data.error) { showToast(data.error, 'error'); return; }
        document.getElementById('token-value').textContent = data.token;
        window._enrollToken = data.token;
        updateSnippets();
        document.getElementById('token-display').style.display = 'block';
    });
}

function updateSnippets() {
    var token = window._enrollToken || '';
    var host = window.location.hostname;
    var port = document.getElementById('enroll-port').value || '8080';
    var name = document.getElementById('enroll-hostname').value || 'my-agent';
    var image = '{{.Version}}'; // server passes image ref
    var clusterPort = '{{.ClusterPort}}';

    document.getElementById('snippet-run').textContent =
        'docker run -d \\\n' +
        '  --name sentinel-agent \\\n' +
        '  -v /var/run/docker.sock:/var/run/docker.sock \\\n' +
        '  -v sentinel-agent-data:/data \\\n' +
        '  -p ' + port + ':8080 \\\n' +
        '  -e SENTINEL_ENROLL_TOKEN=' + token + ' \\\n' +
        '  -e SENTINEL_SERVER_ADDR=' + host + ':' + clusterPort + ' \\\n' +
        '  -e SENTINEL_HOST_NAME=' + name + ' \\\n' +
        '  ghcr.io/will-luck/docker-sentinel:' + image;

    document.getElementById('snippet-compose').textContent =
        'services:\n' +
        '  sentinel-agent:\n' +
        '    image: ghcr.io/will-luck/docker-sentinel:' + image + '\n' +
        '    restart: unless-stopped\n' +
        '    volumes:\n' +
        '      - /var/run/docker.sock:/var/run/docker.sock\n' +
        '      - sentinel-agent-data:/data\n' +
        '    ports:\n' +
        '      - "' + port + ':8080"\n' +
        '    environment:\n' +
        '      SENTINEL_ENROLL_TOKEN: ' + token + '\n' +
        '      SENTINEL_SERVER_ADDR: ' + host + ':' + clusterPort + '\n' +
        '      SENTINEL_HOST_NAME: ' + name;
}
```

**Step 3: Pass version and cluster port to template**

In `handleCluster` handler, include the server's image tag (stripped from version string) and cluster port in the template data.

**Step 4: Build and test**

Run: `go build ./...`

**Step 5: Commit**

```bash
git add internal/web/static/cluster.html internal/web/server.go
git commit -m "feat: Portainer-style enrollment snippets with Docker Run and Compose tabs"
```

---

## Task 10: Auto-enrollment for snippet-deployed agents

**Files:**
- Modify: `cmd/sentinel/main.go`

**Step 1: Detect auto-enrollment env vars**

When `SENTINEL_ENROLL_TOKEN` is set on a fresh container (no `instance_role`), skip the wizard and auto-configure as an agent:

```go
if needsWizard && cfg.EnrollToken != "" && cfg.ServerAddr != "" {
    // Auto-enrollment: snippet-deployed agent, skip wizard.
    log.Info("auto-enrolling as agent", "server", cfg.ServerAddr)

    if err := db.EnsureAuthBuckets(); err != nil {
        log.Error("failed to create auth buckets", "error", err)
        os.Exit(1)
    }

    // Save role and agent config.
    _ = db.SaveSetting("instance_role", "agent")
    _ = db.SaveSetting("server_addr", cfg.ServerAddr)
    _ = db.SaveSetting("auth_setup_complete", "true")
    if cfg.HostName != "" {
        _ = db.SaveSetting("host_name", cfg.HostName)
    }

    // Generate random admin password and print to logs.
    randomPass := generateRandomPassword()
    hash, _ := auth.HashPassword(randomPass)
    userID, _ := auth.GenerateUserID()
    db.SeedBuiltinRoles()
    db.CreateFirstUser(auth.User{
        ID: userID, Username: "admin", PasswordHash: hash,
        RoleID: auth.RoleAdminID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
    })

    fmt.Println("=============================================")
    fmt.Println("Auto-enrolled as agent.")
    fmt.Printf("Agent UI login: admin / %s\n", randomPass)
    fmt.Println("Change this password after first login.")
    fmt.Println("=============================================")

    cfg.Mode = "agent"
    needsWizard = false
}
```

**Step 2: Add `generateRandomPassword` helper**

```go
func generateRandomPassword() string {
    b := make([]byte, 16)
    rand.Read(b)
    return base64.RawURLEncoding.EncodeToString(b)
}
```

**Step 3: Build and test**

Run: `go build ./...`

**Step 4: Commit**

```bash
git add cmd/sentinel/main.go
git commit -m "feat: auto-enrollment for snippet-deployed agents"
```

---

## Task 11: Settings page — new editable fields

**Files:**
- Modify: `internal/web/static/settings.html` (General tab)
- Modify: `internal/web/api_settings.go` (new endpoints)

**Step 1: Add new settings to General tab**

In `settings.html`, add to the General tab (currently shows read-only env vars table):

```html
<div class="settings-rows">
    <div class="setting-row">
        <div class="setting-info">
            <div class="setting-label">Web Port</div>
            <div class="setting-desc">Dashboard port. <span class="restart-badge">Requires restart</span></div>
        </div>
        <input type="number" id="setting-web-port" class="setting-input"
               style="max-width:120px" min="1" max="65535"
               onchange="saveGeneralSetting('web_port', this.value)">
    </div>
    <div class="setting-row">
        <div class="setting-info">
            <div class="setting-label">TLS</div>
            <div class="setting-desc">HTTPS encryption. <span class="restart-badge">Requires restart</span></div>
        </div>
        <select id="setting-tls-mode" class="setting-select"
                onchange="saveGeneralSetting('tls_mode', this.value)">
            <option value="off">Off</option>
            <option value="auto">Auto (self-signed)</option>
            <option value="manual">Manual (provide certs)</option>
        </select>
    </div>
    <div class="setting-row">
        <div class="setting-info">
            <div class="setting-label">Log Format</div>
            <div class="setting-desc">Structured JSON or plain text. <span class="restart-badge">Requires restart</span></div>
        </div>
        <select id="setting-log-format" class="setting-select"
                onchange="saveGeneralSetting('log_format', this.value)">
            <option value="json">JSON</option>
            <option value="text">Plain text</option>
        </select>
    </div>
    <div class="setting-row">
        <div class="setting-info">
            <div class="setting-label">Instance Role</div>
            <div class="setting-desc">This instance is running as a <strong id="current-role">server</strong>.</div>
        </div>
        <button class="btn btn-sm btn-warning" onclick="switchRole()">Switch to Agent</button>
    </div>
</div>
```

**Step 2: Restart-required banner**

Add a JS-driven banner that appears when any restart-required setting changes:

```html
<div id="restart-banner" class="restart-banner" style="display:none">
    <span>Settings changed that require a container restart to take effect.</span>
</div>
```

**Step 3: Add API endpoint for general settings**

`POST /api/settings/general` in `api_settings.go`:

```go
func (s *Server) apiSaveGeneralSetting(w http.ResponseWriter, r *http.Request) {
    var body struct {
        Key   string `json:"key"`
        Value string `json:"value"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        writeError(w, http.StatusBadRequest, "invalid request")
        return
    }

    allowed := map[string]bool{
        "web_port": true, "tls_mode": true, "log_format": true,
    }
    if !allowed[body.Key] {
        writeError(w, http.StatusBadRequest, "unknown setting: "+body.Key)
        return
    }

    if err := s.deps.SettingsStore.SaveSetting(body.Key, body.Value); err != nil {
        writeError(w, http.StatusInternalServerError, "failed to save setting")
        return
    }

    writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "restart_required": "true"})
}
```

**Step 4: Role switch endpoint**

`POST /api/settings/switch-role` — validates, saves new config, returns success. The actual role change requires a container restart.

**Step 5: Build and test**

Run: `go build ./...`
Run: `go test ./internal/web/ -v`

**Step 6: Commit**

```bash
git add internal/web/static/settings.html internal/web/api_settings.go
git commit -m "feat: add TLS, port, log format, and role switch to settings page"
```

---

## Task 12: Final build, test, and verification

**Step 1: Full build**

Run: `go build ./...`

**Step 2: Run all tests**

Run: `go test -race ./... -count=1`

**Step 3: Vet**

Run: `go vet ./...`

**Step 4: Manual verification checklist**

- [ ] Fresh container starts wizard (no env vars)
- [ ] Wizard step navigation works (forward/back)
- [ ] Server path: creates admin, saves settings, redirects to dashboard
- [ ] Agent path: creates admin, tests connection, saves settings
- [ ] Existing deployment (auth_setup_complete=true) skips wizard
- [ ] Auto-enrollment with SENTINEL_ENROLL_TOKEN works
- [ ] Agent mini-UI shows status after agent starts
- [ ] Cluster page shows Docker Run + Docker Compose snippets
- [ ] Settings page shows new fields with restart badges
- [ ] 5-minute timeout shows expired message

---

## Summary

| Task | Files | What |
|------|-------|------|
| 1 | config.go, main.go | `instance_role` DB setting, wizard detection, relax validation |
| 2 | wizard.go, main.go | Minimal wizard web server |
| 3 | setup.html | Multi-step wizard HTML/CSS |
| 4 | wizard.go, handlers_auth.go | Wizard API (server + agent paths) |
| 5 | setup.html | Wizard JavaScript (navigation, validation, submission) |
| 6 | main.go | Process re-exec after wizard completes |
| 7 | agent.html, agent_server.go | Agent mini-UI template + server |
| 8 | main.go, agent_server.go | Wire agent web server into agent startup |
| 9 | cluster.html, server.go | Portainer-style enrollment snippets |
| 10 | main.go | Auto-enrollment for snippet-deployed agents |
| 11 | settings.html, api_settings.go | New editable settings (TLS, port, log, role switch) |
| 12 | — | Final build, test, vet, manual verification |
