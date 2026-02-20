# Code Review Fixes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix all critical and high-severity issues found in the code review sweep — concurrency bugs, silent failures, false-success SSE events, and security fail-open paths.

**Architecture:** Fixes are grouped by file/module to minimize churn. Each task is self-contained — one logical fix per task with its own test and commit. Critical concurrency and security fixes come first, then the systematic SSE/error-handling fixes.

**Tech Stack:** Go 1.24, gRPC, BoltDB, SSE events, Docker SDK

---

## Task 1: Fix `Registry.Get` returning raw pointer — data race in `handleCertRenewal`

**Critical:** `handleCertRenewal` mutates shared `*HostState` fields after lock is released. Also affects `json.Marshal` read race.

**Files:**
- Modify: `internal/cluster/server/registry.go:95-146`
- Modify: `internal/cluster/server/server.go:687-716`
- Test: `internal/cluster/server/server_test.go`

**Step 1: Write the failing test**

Add a test that exercises concurrent cert renewal + heartbeat to detect the race under `-race`.

```go
func TestHandleCertRenewal_NoDataRace(t *testing.T) {
	// Setup server with registry, CA, and a registered host.
	// In parallel goroutines:
	//   1. Call handleCertRenewal
	//   2. Call registry.UpdateLastSeen
	// If there's a data race, `go test -race` will catch it.
}
```

**Step 2: Run test with race detector**

Run: `go test -race ./internal/cluster/server/ -run TestHandleCertRenewal_NoDataRace -v`
Expected: FAIL (data race detected)

**Step 3: Add `Registry.UpdateCertSerial` method**

In `internal/cluster/server/registry.go`, add a new method that operates entirely under the write lock:

```go
// UpdateCertSerial atomically updates the stored cert serial for a host,
// revoking the old serial if one exists. Returns the old serial (if any)
// so the caller can handle revocation.
func (r *Registry) UpdateCertSerial(hostID, newSerial string, store interface{ AddRevokedCert(string) error; SaveClusterHost(string, []byte) error }) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	hs, ok := r.hosts[hostID]
	if !ok {
		return fmt.Errorf("host %s not found", hostID)
	}

	// Revoke old cert serial.
	if hs.Info.CertSerial != "" {
		if err := store.AddRevokedCert(hs.Info.CertSerial); err != nil {
			return fmt.Errorf("revoke old cert: %w", err)
		}
	}

	hs.Info.CertSerial = newSerial
	data, err := json.Marshal(hs.Info)
	if err != nil {
		return fmt.Errorf("marshal host info: %w", err)
	}
	return store.SaveClusterHost(hostID, data)
}
```

**Step 4: Rewrite `handleCertRenewal` to use it and fix all three silent failures**

In `internal/cluster/server/server.go:687-716`, replace the mutation block with:

```go
func (s *Server) handleCertRenewal(hostID string, as *agentStream, csr *proto.CertRenewalCSR) {
	certPEM, serial, err := s.ca.SignCSR(csr.Csr, hostID)
	if err != nil {
		s.log.Error("cert renewal failed", "hostID", hostID, "error", err)
		return
	}

	if err := s.registry.UpdateCertSerial(hostID, serial, s.store); err != nil {
		s.log.Error("cert renewal: failed to update serial", "hostID", hostID, "error", err)
		return
	}

	// Non-blocking send — matches SendCommand pattern.
	msg := &proto.ServerMessage{
		Payload: &proto.ServerMessage_CertRenewalResponse{
			CertRenewalResponse: &proto.CertRenewalResponse{
				AgentCert: certPEM,
			},
		},
	}
	select {
	case as.send <- msg:
	default:
		s.log.Error("cert renewal: send buffer full", "hostID", hostID)
		return
	}

	s.log.Info("cert renewed", "hostID", hostID, "newSerial", serial)
}
```

**Step 5: Run test with race detector**

Run: `go test -race ./internal/cluster/server/ -run TestHandleCertRenewal_NoDataRace -v`
Expected: PASS (no data race)

**Step 6: Run full server tests**

Run: `go test -race ./internal/cluster/server/ -v`
Expected: All pass

**Step 7: Commit**

```bash
git add internal/cluster/server/registry.go internal/cluster/server/server.go internal/cluster/server/server_test.go
git commit -m "fix: cert renewal data race and blocking send

Move HostState mutation into Registry.UpdateCertSerial under write lock.
Use non-blocking send for cert renewal response matching SendCommand pattern.
Propagate errors instead of silently discarding store write failures."
```

---

## Task 2: Fix concurrent sync request clobbering in `registerPending`

**Critical:** Two concurrent sync requests for the same host silently overwrite each other's response channel.

**Files:**
- Modify: `internal/cluster/server/server.go:747-763`
- Test: `internal/cluster/server/server_test.go`

**Step 1: Write the failing test**

```go
func TestRegisterPending_RejectsExisting(t *testing.T) {
	// Register a pending channel for hostID "h1".
	// Try to register again for "h1".
	// Should return nil + error.
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/cluster/server/ -run TestRegisterPending_RejectsExisting -v`
Expected: FAIL

**Step 3: Modify `registerPending` to reject duplicates**

```go
func (s *Server) registerPending(hostID string) (chan *proto.AgentMessage, error) {
	ch := make(chan *proto.AgentMessage, 1)
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if _, exists := s.pending[hostID]; exists {
		return nil, fmt.Errorf("agent %s already has an outstanding request", hostID)
	}
	s.pending[hostID] = ch
	return ch, nil
}
```

**Step 4: Update all callers**

In each sync method (`ListContainersSync`, `UpdateContainerSync`, `ContainerActionSync`), update from:

```go
ch := s.registerPending(hostID)
```

to:

```go
ch, err := s.registerPending(hostID)
if err != nil {
    return nil, err  // or return err, depending on method signature
}
```

**Step 5: Add pending cleanup to Channel disconnect defer**

In the `Channel` defer (around line 365), add:

```go
// Clean up any orphaned pending channel so blocked callers unblock.
s.pendingMu.Lock()
if ch, ok := s.pending[hostID]; ok {
    delete(s.pending, hostID)
    close(ch)
}
s.pendingMu.Unlock()
```

**Step 6: Run tests**

Run: `go test -race ./internal/cluster/server/ -v`
Expected: All pass

**Step 7: Commit**

```bash
git add internal/cluster/server/server.go internal/cluster/server/server_test.go
git commit -m "fix: reject concurrent sync requests for same host

registerPending now returns error if a request is already outstanding.
Clean up orphaned pending channels on agent disconnect."
```

---

## Task 3: Fix CRL check fail-open and `isCertRevoked` error handling

**Critical (security):** Revoked agents can reconnect during store failures.

**Files:**
- Modify: `internal/cluster/server/server.go:326, 425, 912-917`
- Test: `internal/cluster/server/server_test.go`

**Step 1: Write the test**

```go
func TestVerifyCRL_FailsClosed(t *testing.T) {
	// Setup server with a store that returns an error on IsRevokedCert.
	// Call verifyCRL with a valid cert.
	// Should return an error (reject connection), not nil (allow).
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/cluster/server/ -run TestVerifyCRL_FailsClosed -v`
Expected: FAIL (returns nil instead of error)

**Step 3: Fix `verifyCRL` to fail closed**

In `internal/cluster/server/server.go:912-917`:

```go
revoked, err := s.store.IsRevokedCert(serial)
if err != nil {
	s.log.Error("CRL check failed, rejecting connection", "serial", serial, "error", err)
	return fmt.Errorf("CRL check unavailable")
}
```

**Step 4: Fix `isCertRevoked` error handling in Channel and ReportState**

Line 326:
```go
if revoked, err := s.isCertRevoked(stream.Context()); err != nil {
	s.log.Error("cert revocation check failed", "error", err)
	return status.Error(codes.Internal, "revocation check unavailable")
} else if revoked {
	return status.Error(codes.PermissionDenied, "certificate has been revoked")
}
```

Line 425 (same pattern for ReportState):
```go
if revoked, err := s.isCertRevoked(ctx); err != nil {
	s.log.Error("cert revocation check failed", "error", err)
	return nil, status.Error(codes.Internal, "revocation check unavailable")
} else if revoked {
	return nil, status.Error(codes.PermissionDenied, "certificate has been revoked")
}
```

**Step 5: Run tests**

Run: `go test -race ./internal/cluster/server/ -v`
Expected: All pass

**Step 6: Commit**

```bash
git add internal/cluster/server/server.go internal/cluster/server/server_test.go
git commit -m "fix: CRL and cert revocation checks fail closed on store errors

Reject connections when the revocation store is unavailable instead of
silently allowing potentially revoked certificates through."
```

---

## Task 4: Fix false-success SSE events in remote container action handlers

**Critical:** All three remote action handlers publish success SSE unconditionally.

**Files:**
- Modify: `internal/web/api_control.go:33-43, 113-123, 191-202`
- Test: `internal/web/api_control_test.go` (create if needed, or add to existing test file)

**Step 1: Fix all three remote action goroutines**

The pattern is identical for all three. Replace:

```go
go func() {
    if err := s.deps.Cluster.RemoteContainerAction(context.Background(), hostID, name, "ACTION"); err != nil {
        s.deps.Log.Error("remote ACTION failed", "name", name, "host", hostID, "error", err)
    }
    s.deps.EventBus.Publish(events.SSEEvent{...})
}()
```

With (for each of restart/stop/start):

```go
go func() {
    if err := s.deps.Cluster.RemoteContainerAction(context.Background(), hostID, name, "ACTION"); err != nil {
        s.deps.Log.Error("remote ACTION failed", "name", name, "host", hostID, "error", err)
        s.deps.EventBus.Publish(events.SSEEvent{
            Type:          events.EventContainerState,
            ContainerName: name,
            Message:       "ACTION failed on " + hostID + ": " + err.Error(),
            Timestamp:     time.Now(),
        })
        return
    }
    s.deps.EventBus.Publish(events.SSEEvent{
        Type:          events.EventContainerState,
        ContainerName: name,
        Message:       "Container ACTION: " + name,
        Timestamp:     time.Now(),
    })
}()
```

**Step 2: Fix all three local action goroutines too (lines 70, 149, 228)**

Add failure SSE events to the local error paths. For each:

```go
go func() {
    if err := s.deps.ACTION.ACTIONContainer(context.Background(), containerID); err != nil {
        s.deps.Log.Error("ACTION failed", "name", name, "error", err)
        s.deps.EventBus.Publish(events.SSEEvent{
            Type:          events.EventContainerState,
            ContainerName: name,
            Message:       "ACTION failed: " + err.Error(),
            Timestamp:     time.Now(),
        })
        return
    }
    s.deps.EventBus.Publish(events.SSEEvent{...})
}()
```

**Step 3: Fix manual update failure SSE (line 297)**

```go
if err != nil {
    s.deps.Log.Error("manual update failed", "name", name, "error", err)
    s.deps.EventBus.Publish(events.SSEEvent{
        Type:          events.EventContainerUpdate,
        ContainerName: name,
        Message:       "update failed: " + err.Error(),
        Timestamp:     time.Now(),
    })
}
```

**Step 4: Fix self-update failure SSE (line 464)**

```go
go func() {
    if err := s.deps.SelfUpdater.Update(context.Background()); err != nil {
        s.deps.Log.Error("self-update failed", "error", err)
        s.deps.EventBus.Publish(events.SSEEvent{
            Type:      events.EventContainerUpdate,
            Message:   "self-update failed: " + err.Error(),
            Timestamp: time.Now(),
        })
    }
}()
```

**Step 5: Fix rollback policy error handling (line 336)**

```go
if s.deps.SettingsStore != nil && s.deps.Policy != nil {
    if rp, err := s.deps.SettingsStore.LoadSetting("rollback_policy"); err != nil {
        s.deps.Log.Error("failed to load rollback policy", "name", name, "error", err)
    } else if rp == "manual" || rp == "pinned" {
        if err := s.deps.Policy.SetPolicyOverride(name, rp); err != nil {
            s.deps.Log.Error("failed to set policy after rollback", "name", name, "policy", rp, "error", err)
        } else {
            s.deps.Log.Info("policy changed after manual rollback", "name", name, "policy", rp)
        }
    }
}
```

**Step 6: Run build check**

Run: `go build ./...`
Expected: Clean build

**Step 7: Run existing tests**

Run: `go test ./internal/web/ -v`
Expected: All pass

**Step 8: Commit**

```bash
git add internal/web/api_control.go
git commit -m "fix: publish failure SSE events for all container actions

Remote and local stop/start/restart/update/self-update/rollback now emit
SSE failure events so the dashboard reflects actual errors instead of
showing stale success state."
```

---

## Task 5: Fix `renderError` double WriteHeader

**Important:** Template failure after `renderError` causes double WriteHeader and dropped Content-Type.

**Files:**
- Modify: `internal/web/handlers.go:1161-1174`

**Step 1: Fix `renderError`**

```go
func (s *Server) renderError(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, "error.html", errorPageData{Title: title, Message: message}); err != nil {
		s.deps.Log.Error("error template render failed", "error", err)
		// Headers already committed — best effort plain text.
		fmt.Fprintf(w, "Internal server error")
	}
}
```

Update `renderTemplate` to not call `http.Error` when headers are already committed. The simplest fix is to leave `renderTemplate` as-is (it's only broken when called from `renderError`), and have `renderError` bypass it.

**Step 2: Run build**

Run: `go build ./...`
Expected: Clean

**Step 3: Commit**

```bash
git add internal/web/handlers.go
git commit -m "fix: renderError sets Content-Type before WriteHeader

Prevents double WriteHeader when template execution fails and ensures
Content-Type header is not silently dropped."
```

---

## Task 6: Fix `offlineSince` race in autonomous mode

**Important:** Race detector hit on log read of `a.offlineSince` without lock.

**Files:**
- Modify: `internal/cluster/agent/autonomous.go:142-146`

**Step 1: Fix the read**

```go
func (a *Agent) runAutonomous(ctx context.Context) error {
	a.mu.Lock()
	offlineSince := a.offlineSince
	a.mu.Unlock()

	a.log.Warn("entering autonomous mode -- server unreachable",
		"offline_since", offlineSince,
		"grace_period", a.cfg.GracePeriodOffline,
	)
```

**Step 2: Run with race detector**

Run: `go test -race ./internal/cluster/agent/ -v`
Expected: No race warnings

**Step 3: Commit**

```bash
git add internal/cluster/agent/autonomous.go
git commit -m "fix: read offlineSince under lock in runAutonomous"
```

---

## Task 7: Fix `configFromInspect` pointer alias

**Important:** Pointer copy mutates original inspect data.

**Files:**
- Modify: `internal/cluster/agent/agent.go:766-783`

**Step 1: Fix the copy**

```go
func configFromInspect(inspect *container.InspectResponse, targetImage string) (*container.Config, *container.HostConfig, *network.NetworkingConfig) {
	cfgCopy := *inspect.Config
	cfgCopy.Image = targetImage

	hostCfg := inspect.HostConfig

	netCfg := &network.NetworkingConfig{}
	if inspect.NetworkSettings != nil && len(inspect.NetworkSettings.Networks) > 0 {
		netCfg.EndpointsConfig = make(map[string]*network.EndpointSettings, len(inspect.NetworkSettings.Networks))
		for name, ep := range inspect.NetworkSettings.Networks {
			netCfg.EndpointsConfig[name] = ep
		}
	}

	return &cfgCopy, hostCfg, netCfg
}
```

**Step 2: Run build**

Run: `go build ./...`
Expected: Clean

**Step 3: Commit**

```bash
git add internal/cluster/agent/agent.go
git commit -m "fix: deep-copy Config in configFromInspect to avoid mutating original"
```

---

## Task 8: Fix `UpdateLastSeen` and `handleDashboardStats` error logging

**High:** Silent discards of store write errors across heartbeat, disconnect, and state report paths.

**Files:**
- Modify: `internal/cluster/server/server.go:374, 431, 569`
- Modify: `internal/web/handlers.go:809-812`

**Step 1: Fix three `UpdateLastSeen` locations**

Line 374 (disconnect defer):
```go
if err := s.registry.UpdateLastSeen(hostID, time.Now()); err != nil {
    s.log.Warn("failed to update last seen on disconnect", "hostID", hostID, "error", err)
}
```

Line 431 (ReportState):
```go
if err := s.registry.UpdateLastSeen(hostID, time.Now()); err != nil {
    s.log.Warn("failed to update last seen on state report", "hostID", hostID, "error", err)
}
```

Line 569 (heartbeat):
```go
if err := s.registry.UpdateLastSeen(hostID, time.Now()); err != nil {
    s.log.Warn("failed to update last seen on heartbeat", "hostID", hostID, "error", err)
}
```

**Step 2: Fix `handleDashboardStats` missing log**

Line 809-812:
```go
containers, err := s.deps.Docker.ListAllContainers(r.Context())
if err != nil {
    s.deps.Log.Error("failed to list containers for stats", "error", err)
    writeError(w, http.StatusInternalServerError, "failed to list containers")
    return
}
```

**Step 3: Run build**

Run: `go build ./...`
Expected: Clean

**Step 4: Commit**

```bash
git add internal/cluster/server/server.go internal/web/handlers.go
git commit -m "fix: log UpdateLastSeen and dashboard stats errors

Stop silently discarding BoltDB write failures on heartbeat, disconnect,
and state report paths. Add missing error logging to stats endpoint."
```

---

## Task 9: Fix silent store write discards in engine and adapters

**High/Medium:** History records, notify state, cleanup operations, rate limits all silently fail.

**Files:**
- Modify: `internal/engine/updater.go:525-529, 741`
- Modify: `internal/engine/update.go:96, 283-286`
- Modify: `internal/engine/service_update.go:163-167, 265`
- Modify: `cmd/sentinel/adapters.go:573-577`

**Step 1: Fix `RecordUpdate` in updater.go (remote update history)**

Line 741:
```go
if err := u.store.RecordUpdate(store.UpdateRecord{...}); err != nil {
    u.log.Warn("failed to record remote update history", "name", scopedName, "error", err)
}
```

**Step 2: Fix `SetNotifyState` in updater.go**

Lines 525-529:
```go
if err := u.store.SetNotifyState(name, &store.NotifyState{...}); err != nil {
    u.log.Warn("failed to persist notify state", "name", name, "error", err)
}
```

**Step 3: Fix `SetNotifyState` in service_update.go**

Lines 163-167:
```go
if err := u.store.SetNotifyState(name, &store.NotifyState{...}); err != nil {
    u.log.Warn("failed to persist service notify state", "name", name, "error", err)
}
```

**Step 4: Fix `RecordUpdate` in service_update.go**

Line 265:
```go
if err := u.store.RecordUpdate(record); err != nil {
    u.log.Warn("failed to record service update history", "name", name, "error", err)
}
```

**Step 5: Fix `ImageDigest` in update.go**

Line 96:
```go
newDigest, err := u.docker.ImageDigest(ctx, pullImage)
if err != nil {
    u.log.Debug("could not resolve new image digest", "image", pullImage, "error", err)
}
```

**Step 6: Fix `ClearNotifyState` and `ClearIgnoredVersions` in update.go**

Lines 283-286:
```go
if err := u.store.ClearNotifyState(name); err != nil {
    u.log.Warn("failed to clear notify state after update", "name", name, "error", err)
}
if err := u.store.ClearIgnoredVersions(name); err != nil {
    u.log.Warn("failed to clear ignored versions after update", "name", name, "error", err)
}
```

**Step 7: Fix rate limit persistence in adapters.go**

Lines 573-577:
```go
if a.saver != nil {
    data, exportErr := a.t.Export()
    if exportErr != nil {
        log.Printf("failed to export rate limit state: %v", exportErr)
    } else if err := a.saver(data); err != nil {
        log.Printf("failed to persist rate limit state: %v", err)
    }
}
```

Note: Check if the adapter has access to a structured logger. If not, use `log.Printf` as a fallback.

**Step 8: Run full test suite**

Run: `go test ./internal/engine/ ./cmd/sentinel/ -v`
Expected: All pass

**Step 9: Commit**

```bash
git add internal/engine/updater.go internal/engine/update.go internal/engine/service_update.go cmd/sentinel/adapters.go
git commit -m "fix: log all silent store write failures in engine and adapters

Replace _ = err with logged warnings for RecordUpdate, SetNotifyState,
ClearNotifyState, ClearIgnoredVersions, ImageDigest, and rate limit
persistence. Makes store failures visible in logs for debugging."
```

---

## Task 10: Fix `handleLogs` rendering empty page on DB failure

**Medium:** Activity Log page shows empty table with 200 OK when store query fails.

**Files:**
- Modify: `internal/web/handlers.go:643-654`

**Step 1: Fix to render error page on failure**

```go
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	var logs []LogEntry
	if s.deps.EventLog != nil {
		var err error
		logs, err = s.deps.EventLog.ListLogs(200)
		if err != nil {
			s.deps.Log.Error("failed to list logs", "error", err)
			s.renderError(w, http.StatusInternalServerError, "Database Error",
				"Failed to load activity logs. The database may be temporarily unavailable.")
			return
		}
	}
	if logs == nil {
		logs = []LogEntry{}
	}

	data := pageData{
		Page:       "logs",
		Logs:       logs,
		QueueCount: len(s.deps.Queue.List()),
	}
	s.withAuth(r, &data)
	s.withCluster(&data)
	s.renderTemplate(w, "logs.html", data)
}
```

**Step 2: Run build**

Run: `go build ./...`
Expected: Clean

**Step 3: Commit**

```bash
git add internal/web/handlers.go
git commit -m "fix: render error page when activity log DB query fails"
```

---

## Task 11: Final build and full test run

**Files:** None modified — verification only.

**Step 1: Full build**

Run: `go build ./...`
Expected: Clean

**Step 2: Full test suite with race detector**

Run: `go test -race ./... -count=1`
Expected: All pass

**Step 3: Verify proto still compiles (no changes needed)**

Run: `go vet ./...`
Expected: Clean

---

## Summary

| Task | Severity | Category | Fix |
|------|----------|----------|-----|
| 1 | Critical | Race condition | `Registry.UpdateCertSerial` under lock + non-blocking send |
| 2 | Critical | Concurrency | `registerPending` rejects duplicates + cleanup on disconnect |
| 3 | Critical | Security | CRL + `isCertRevoked` fail closed on store errors |
| 4 | Critical/High | Silent failure | SSE failure events for all container action goroutines |
| 5 | Important | HTTP bug | `renderError` Content-Type + double WriteHeader |
| 6 | Important | Race | `offlineSince` read under lock |
| 7 | Important | Memory safety | Deep-copy Config in `configFromInspect` |
| 8 | High | Logging | `UpdateLastSeen` + dashboard stats error logging |
| 9 | High/Medium | Logging | All silent `_ = err` store writes in engine/adapters |
| 10 | Medium | UX | Activity Log renders error page on DB failure |
| 11 | — | Verification | Full build + test suite |

**Not addressed in this plan (intentional):**
- Medium-severity `GetMaintenance` silent defaults (3 locations) — safe fallback, low risk
- `LoadSetting` error discards across handlers — most have safe fallbacks
- `extractImageFromSnapshot` returning empty — informational only
- Stack order fallback to alphabetical — safe degradation
