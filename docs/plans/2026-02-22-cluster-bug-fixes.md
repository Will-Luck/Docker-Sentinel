# Cluster Bug Fixes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix three bugs affecting cluster users: remote policy overrides ignored by scan engine, missing `.Enabled()` checks on stop/start/restart, and bulk policy not scoping remote container keys.

**Architecture:** All three are data-path bugs where the `hostID::name` scoping pattern or `.Enabled()` guard was missed. Fixes are surgical: pass scoped names to `ResolvePolicy`, add `.Enabled()` to three handler conditions, and thread hostID through `apiBulkPolicy`.

**Tech Stack:** Go 1.24, net/http, BoltDB, existing test patterns in `api_policy_test.go` and `policy_test.go`

---

### Task 1: Fix remote policy override ignored by scan engine

The scan engine calls `ResolvePolicy(u.store, c.Labels, c.Name, ...)` for remote containers at `updater.go:691`. But policy overrides for remote containers are stored under the key `hostID::name` (via `store.ScopedKey`). So the DB lookup in `ResolvePolicy` using bare `c.Name` never finds the override.

**Files:**
- Modify: `internal/engine/updater.go:691`
- Test: `internal/engine/policy_test.go`

**Step 1: Write the failing test**

Add to `internal/engine/policy_test.go`:

```go
func TestResolvePolicyScopedKey(t *testing.T) {
	db := newTestStore(t)
	_ = db.SetPolicyOverride("host1::myapp", "pinned")

	r := ResolvePolicy(db, nil, "host1::myapp", "v1.0", "manual", false)
	if r.Policy != "pinned" {
		t.Fatalf("expected pinned, got %s", r.Policy)
	}
	if r.Source != SourceOverride {
		t.Fatalf("expected override source, got %s", r.Source)
	}
}
```

**Step 2: Run test to verify it passes**

Run: `cd /home/lns/Docker-Sentinel && go test -v -run TestResolvePolicyScopedKey ./internal/engine/`
Expected: PASS (ResolvePolicy already takes name as-is, the bug is the caller passing bare name)

**Step 3: Fix the caller in updater.go**

Change `internal/engine/updater.go:691` from:
```go
resolved := ResolvePolicy(u.store, c.Labels, c.Name, tag, u.cfg.DefaultPolicy(), u.cfg.LatestAutoUpdate())
```
to:
```go
resolved := ResolvePolicy(u.store, c.Labels, store.ScopedKey(hostID, c.Name), tag, u.cfg.DefaultPolicy(), u.cfg.LatestAutoUpdate())
```

This uses `store.ScopedKey(hostID, c.Name)` which returns `hostID::name` when hostID is non-empty, or bare `name` when empty. The `store` package is already imported at this location.

**Step 4: Run all engine tests**

Run: `cd /home/lns/Docker-Sentinel && go test -v -race ./internal/engine/`
Expected: all PASS

**Step 5: Commit**

```bash
git add internal/engine/updater.go internal/engine/policy_test.go
git commit -m "fix: use scoped key for remote container policy lookup in scan engine"
```

---

### Task 2: Add `.Enabled()` check to apiRestart, apiStop, apiStart

`apiRestart` (line 38), `apiStop` (line 125), and `apiStart` (line 212) in `api_control.go` check `s.deps.Cluster != nil` but skip `.Enabled()`. Since `Cluster` is always non-nil (wrapped in `ClusterController`), these handlers enter the remote dispatch path for any `?host=` request even when cluster mode is off, causing silent failures.

**Files:**
- Modify: `internal/web/api_control.go:38,125,212`

**Step 1: Fix apiRestart**

Change line 38 from:
```go
if hostID != "" && s.deps.Cluster != nil {
```
to:
```go
if hostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
```

**Step 2: Fix apiStop**

Change line 125 from:
```go
if hostID != "" && s.deps.Cluster != nil {
```
to:
```go
if hostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
```

**Step 3: Fix apiStart**

Change line 212 from:
```go
if hostID != "" && s.deps.Cluster != nil {
```
to:
```go
if hostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
```

**Step 4: Build to verify**

Run: `cd /home/lns/Docker-Sentinel && make build`
Expected: clean build

**Step 5: Commit**

```bash
git add internal/web/api_control.go
git commit -m "fix: add Enabled() guard to cluster routing in stop/start/restart handlers"
```

---

### Task 3: Fix apiBulkPolicy hostID scoping

`apiBulkPolicy` in `api_policy.go` uses bare container names for both `GetPolicyOverride` (line 178) and `SetPolicyOverride` (line 204). Remote containers have policy overrides keyed as `hostID::name`. The bulk endpoint needs to resolve hostID for each container name when looking up or writing overrides.

The bulk policy endpoint receives bare container names from the JS frontend (no hostID). We need to look up the hostID from the cluster container cache when a name matches a remote container.

**Files:**
- Modify: `internal/web/api_policy.go:168-209`
- Test: `internal/web/api_policy_test.go`

**Step 1: Write the failing test**

Add to `internal/web/api_policy_test.go`:

```go
func TestBulkPolicy_RemoteContainersScopedKeys(t *testing.T) {
	docker := &mockContainerLister{}
	policy := newMockPolicyStore()
	remotes := []RemoteContainer{
		{Name: "postgres", Image: "postgres:16", HostID: "h1", Labels: map[string]string{"sentinel.policy": "manual"}},
	}
	srv := newPolicyTestServer(docker, policy, nil, remotes)

	// Confirm: set postgres to pinned.
	body := `{"containers":["postgres"],"policy":"pinned","confirm":true}`
	w := doBulkPolicy(srv, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	result := decodeMap(t, w)
	if result["applied"] != float64(1) {
		t.Errorf("applied = %v, want 1", result["applied"])
	}

	// The override must be stored under "h1::postgres", not "postgres".
	if _, ok := policy.GetPolicyOverride("postgres"); ok {
		t.Error("override stored under bare name 'postgres', want scoped key 'h1::postgres'")
	}
	if p, ok := policy.GetPolicyOverride("h1::postgres"); !ok || p != "pinned" {
		t.Errorf("h1::postgres policy = (%q, %v), want (\"pinned\", true)", p, ok)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /home/lns/Docker-Sentinel && go test -v -run TestBulkPolicy_RemoteContainersScopedKeys ./internal/web/`
Expected: FAIL - override stored under bare name "postgres"

**Step 3: Build a hostID lookup map in apiBulkPolicy**

In `internal/web/api_policy.go`, inside `apiBulkPolicy`, after the `allLabels` line (line 166), add a hostID lookup map built from cluster containers:

```go
// Build hostID lookup for remote containers so we can scope policy keys.
remoteHostIDs := map[string]string{}
if s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
	for _, rc := range s.deps.Cluster.AllHostContainers() {
		if _, exists := remoteHostIDs[rc.Name]; !exists {
			remoteHostIDs[rc.Name] = rc.HostID
		}
	}
}
```

**Step 4: Use scoped keys for GetPolicyOverride and SetPolicyOverride**

In the loop at line 168, after `labels := allLabels[name]`, resolve the policy key:

```go
policyKey := name
if hid, ok := remoteHostIDs[name]; ok {
	policyKey = hid + "::" + name
}
```

Then change line 178 from `s.deps.Policy.GetPolicyOverride(name)` to `s.deps.Policy.GetPolicyOverride(policyKey)`.

Change the `changes` append at line 187 to include the policy key:

The `changeEntry` struct needs a `Key` field (unexported is fine, just for internal use). Actually, simpler: store the policyKey alongside name. Add a field:

```go
type changeEntry struct {
	Name string `json:"name"`
	Key  string `json:"-"`
	From string `json:"from"`
	To   string `json:"to"`
}
```

And at line 187:
```go
changes = append(changes, changeEntry{Name: name, Key: policyKey, From: current, To: body.Policy})
```

Then at line 204, change `s.deps.Policy.SetPolicyOverride(c.Name, body.Policy)` to `s.deps.Policy.SetPolicyOverride(c.Key, body.Policy)`.

**Step 5: Run the test to verify it passes**

Run: `cd /home/lns/Docker-Sentinel && go test -v -run TestBulkPolicy_RemoteContainersScopedKeys ./internal/web/`
Expected: PASS

**Step 6: Run all web tests**

Run: `cd /home/lns/Docker-Sentinel && go test -v -race ./internal/web/`
Expected: all PASS

**Step 7: Commit**

```bash
git add internal/web/api_policy.go internal/web/api_policy_test.go
git commit -m "fix: scope bulk policy overrides with hostID for remote containers"
```

---

### Task 4: Fix apiServiceUpdate missing SSE failure event

When a Swarm service update fails in the background goroutine at `api_services.go:72`, no SSE event is published, so the UI spinner never resolves on failure.

**Files:**
- Modify: `internal/web/api_services.go:69-73`

**Step 1: Add SSE failure event**

Change the goroutine from:
```go
go func() {
	if err := s.deps.Swarm.UpdateService(context.Background(), serviceID, name, targetImage); err != nil {
		s.deps.Log.Error("service update failed", "name", name, "error", err)
	}
}()
```

to:
```go
go func() {
	if err := s.deps.Swarm.UpdateService(context.Background(), serviceID, name, targetImage); err != nil {
		s.deps.Log.Error("service update failed", "name", name, "error", err)
		s.deps.EventBus.Publish(events.SSEEvent{
			Type:          events.EventServiceUpdate,
			ContainerName: name,
			Message:       "service update failed: " + err.Error(),
			Timestamp:     time.Now(),
		})
	}
}()
```

**Step 2: Build to verify**

Run: `cd /home/lns/Docker-Sentinel && make build`
Expected: clean build

**Step 3: Commit**

```bash
git add internal/web/api_services.go
git commit -m "fix: publish SSE event on service update failure"
```

---

### Task 5: Build, test, deploy

**Step 1: Run full test suite**

Run: `cd /home/lns/Docker-Sentinel && make test`
Expected: all PASS

**Step 2: Build Docker image and deploy**

Run: `cd /home/lns/Docker-Sentinel && make dev-deploy`
Expected: deployed to 192.168.1.60:62850

**Step 3: Verify on test server**

Check http://192.168.1.60:62850 loads and shows the dev tag in the footer.
