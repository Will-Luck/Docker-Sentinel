# Setup Wizard & GUI-First Configuration Design

## Problem

Docker-Sentinel requires env vars to configure mode (server vs agent), cluster settings, and other infrastructure options. There's no first-run wizard — just a single "create admin" card. Agents are fully headless with no web UI. This conflicts with the goal of a GUI-first experience where any configurable value is accessible from the interface.

## Solution

A multi-step setup wizard shown on every fresh container launch. The wizard determines the instance role (server or agent), collects credentials, and configures core settings. After setup, agents keep a minimal status UI. All settings from env vars become editable in the GUI.

## First-Run Detection & Startup

New DB setting: `instance_role` (values: `"server"`, `"agent"`, or empty).

```
Container starts
  → Load config (env vars still work as overrides)
  → Check DB: instance_role set?
     NO  → Start web server on SENTINEL_WEB_PORT (default 8080)
           → Redirect everything to /setup (wizard)
           → 5-minute security window starts
     YES, "server" → Normal server startup
     YES, "agent"  → Start agent process + minimal web UI
```

The 5-minute security window carries forward. On expiry the wizard locks and shows: "Security window timed out — restart the container to re-initialize setup."

**Env var override:** If `SENTINEL_MODE=agent` + `SENTINEL_ENROLL_TOKEN` are set, the wizard auto-enrolls on first boot and skips the interactive wizard. Existing deployments with env vars keep working.

**Backwards compat:** Existing servers with `auth_setup_complete=true` in DB skip the wizard.

## Wizard Steps

All steps rendered client-side with progress dots. No page reloads between steps.

### Step 1: Role Selection

Two clickable cards:
- **Server** — "Central dashboard that monitors and updates containers"
- **Agent** — "Connects to a Sentinel server and runs updates on this host"

### Step 2: Admin Account (both paths)

- Username + password + confirm password
- Server: dashboard admin
- Agent: login for the agent's mini-UI
- Validation: min 8 chars, must contain letter + digit

### Step 3: Role-Specific Configuration

**Server path:**
- Default update policy: auto / manual / pinned (radio cards)
- Poll interval: dropdown (15m / 30m / 1h / 6h / 12h / 24h)
- Enable cluster mode: toggle

**Agent path:**
- Server address (host:port)
- Enrollment token (paste from server)
- Host name (human-readable label)
- "Connect" button → live enrollment attempt → success/error inline

### Step 4: Optional Extras (server only)

Collapsible sections, all skippable:
- **Notifications** — quick-add one channel (type + URL/token)
- **TLS** — auto-generate self-signed / provide cert paths / skip
- **Registry credentials** — add one private registry login

Each section saves independently. "Skip all, go to dashboard" button available.

### Final Step: Done

- Server: "Setup complete!" → "Go to Dashboard"
- Agent: "Connected to server:9443 as my-host" → "Go to Status Page"

## Agent Mini-UI

Single-page status dashboard, login-protected:

- Status card: connected/disconnected, server address, host name, container count, version
- Settings: server address, host name, grace period (editable)
- Actions: Switch to Server Mode, Re-enroll, Change Password, Logout

No nav bar, no tabs — just the status card and settings.

## Portainer-Style Agent Enrollment

Server's cluster page "Enroll New Host" section upgraded with tabbed snippets:

**Docker Run tab** and **Docker Compose tab** with ready-to-paste commands including:
- Image tag auto-filled from server's current version
- Server address auto-filled from request host + cluster port
- Host name editable (defaults to `my-agent`)
- Port editable (defaults to 8080)
- Copy button on each snippet
- One-time enrollment token

**Auto-enrollment:** When `SENTINEL_ENROLL_TOKEN` env var is set on a fresh container, the wizard auto-enrolls — skips interactive steps. Saves `instance_role=agent`, creates default admin account (random password printed to container logs), starts agent mode.

## Post-Setup Settings

All wizard-configured values editable in the GUI afterward.

**Server Settings page additions:**

| Setting | Location | Notes |
|---|---|---|
| TLS mode (auto/manual/off) | General tab | Cert path fields for manual mode |
| Web port | General tab | Requires restart badge |
| Log format (JSON/text) | General tab | Requires restart badge |
| Docker socket path | General tab | Requires restart badge |
| Instance role switch | General tab | "Switch to Agent" with confirmation |

Settings requiring restart show a yellow badge. A persistent banner appears: "Pending changes require a restart to take effect."

**Role switching:**
- Server → Agent: Settings "Switch to Agent" → confirmation → server address + token → saves config → process re-exec
- Agent → Server: Agent UI "Switch to Server" → confirmation → saves `instance_role=server` → process re-exec → full dashboard

## Files to Create/Modify

**New files:**
- `internal/web/static/agent.html` — agent mini-UI page
- `internal/web/handlers_agent.go` — agent status + settings handlers

**Modified files:**
- `internal/web/static/setup.html` — rewrite: single card → multi-step wizard
- `internal/web/static/cluster.html` — enrollment snippet generator
- `internal/web/static/settings.html` — TLS, port, log format, socket, role switch sections
- `internal/web/handlers_auth.go` — expand apiSetup for role + agent enrollment + extras
- `internal/web/server.go` — startup routing based on role, always start web
- `internal/config/config.go` — DB-persisted config overlay for all settings
- `cmd/sentinel/main.go` — auto-enrollment detection, process re-exec

**Unchanged:**
- Agent gRPC protocol
- Cluster server internals
- Engine/updater logic
- Auth model (multi-user with roles)
