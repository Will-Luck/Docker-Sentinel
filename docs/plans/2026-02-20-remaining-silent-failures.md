# Remaining Silent Failure Fixes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace all remaining `_ =` error discards and `_, _ :=` LoadSetting patterns across the web layer, services API, notifications API, and auth handlers with proper error logging.

**Architecture:** Grouped by file to minimize churn. All changes follow the same pattern: replace `_ =` with `if err := ...; err != nil { log }`. For `LoadSetting` reads where the fallback is safe, log at `Debug` level (not `Warn`) since missing settings are normal on fresh installs.

**Tech Stack:** Go 1.24, BoltDB, structured logging via `slog`

---

## Task 1: Fix `api_services.go` silent failures

**Files:**
- Modify: `internal/web/api_services.go:92, 155, 169-171, 177, 286, 290, 304`

**Step 1: Fix AppendLog discards (lines 92, 177, 304)**

Three `_ = s.deps.EventLog.AppendLog(...)` calls. Replace each with:

```go
if err := s.deps.EventLog.AppendLog(LogEntry{...}); err != nil {
    s.deps.Log.Warn("failed to append event log", "error", err)
}
```

**Step 2: Fix RecordUpdate discard (line 155)**

```go
if err := s.deps.Store.RecordUpdate(UpdateRecord{...}); err != nil {
    s.deps.Log.Warn("failed to record service rollback history", "name", name, "error", err)
}
```

**Step 3: Fix rollback policy block (lines 169-171)**

Replace:
```go
if rp, _ := s.deps.SettingsStore.LoadSetting("rollback_policy"); rp == "manual" || rp == "pinned" {
    _ = s.deps.Policy.SetPolicyOverride(name, rp)
    s.deps.Log.Info("policy changed after manual rollback", "name", name, "policy", rp)
}
```

With:
```go
if rp, err := s.deps.SettingsStore.LoadSetting("rollback_policy"); err != nil {
    s.deps.Log.Warn("failed to load rollback policy", "name", name, "error", err)
} else if rp == "manual" || rp == "pinned" {
    if err := s.deps.Policy.SetPolicyOverride(name, rp); err != nil {
        s.deps.Log.Warn("failed to set policy after rollback", "name", name, "error", err)
    } else {
        s.deps.Log.Info("policy changed after manual rollback", "name", name, "policy", rp)
    }
}
```

**Step 4: Fix SaveSetting discards for replica counts (lines 286, 290)**

```go
if err := s.deps.SettingsStore.SaveSetting("svc_prev_replicas::"+name, fmt.Sprintf("%d", prevReplicas)); err != nil {
    s.deps.Log.Warn("failed to save previous replica count", "name", name, "error", err)
}
```

And:
```go
if err := s.deps.SettingsStore.SaveSetting("svc_prev_replicas::"+name, ""); err != nil {
    s.deps.Log.Warn("failed to clear previous replica count", "name", name, "error", err)
}
```

**Step 5: Build and test**

Run: `go build ./...`
Run: `go test ./internal/web/ -v`

**Step 6: Commit**

```bash
git add internal/web/api_services.go
git commit -m "fix: log all silent store failures in services API"
```

---

## Task 2: Fix `api_notifications.go` silent failures

**Files:**
- Modify: `internal/web/api_notifications.go:106, 315, 318, 321, 324, 382, 421`

**Step 1: Fix json.Decode discard (line 106)**

This is `_ = json.NewDecoder(r.Body).Decode(&body)` for the test notification endpoint. An empty body is expected (backward compat), so this is fine to leave as-is. **No change needed** — the comment explains the intent.

**Step 2: Fix digest settings SaveSetting discards (lines 315, 318, 321, 324)**

Replace each `_ = s.deps.SettingsStore.SaveSetting(...)` with:
```go
if err := s.deps.SettingsStore.SaveSetting("digest_enabled", val); err != nil {
    s.deps.Log.Warn("failed to save digest setting", "key", "digest_enabled", "error", err)
}
```

Same pattern for `digest_time`, `digest_interval`, `default_notify_mode`.

**Step 3: Fix json.Unmarshal discard (line 382)**

```go
if err := json.Unmarshal([]byte(val), &dismissed); err != nil {
    s.deps.Log.Debug("failed to parse dismissed banners", "error", err)
}
```

**Step 4: Fix banner dismissed SaveSetting (line 421)**

```go
if err := s.deps.SettingsStore.SaveSetting("digest_banner_dismissed", string(data)); err != nil {
    s.deps.Log.Warn("failed to save banner dismissed state", "error", err)
}
```

**Step 5: Build and test**

Run: `go build ./...`
Run: `go test ./internal/web/ -v`

**Step 6: Commit**

```bash
git add internal/web/api_notifications.go
git commit -m "fix: log silent store failures in notifications API"
```

---

## Task 3: Fix `handlers.go` silent GetMaintenance and LoadSetting discards

**Files:**
- Modify: `internal/web/handlers.go:247, 342, 719, 921, 978`

**Step 1: Fix GetMaintenance discards (lines 247, 719, 978)**

Replace each `maintenance, _ := s.deps.Store.GetMaintenance(name)` with:
```go
maintenance, err := s.deps.Store.GetMaintenance(name)
if err != nil {
    s.deps.Log.Debug("failed to load maintenance state", "name", name, "error", err)
}
```

Use `Debug` level — a missing maintenance flag defaults to false which is safe.

**Step 2: Fix stack_order LoadSetting (line 342)**

Replace:
```go
savedJSON, _ := s.deps.SettingsStore.LoadSetting("stack_order")
```

With:
```go
savedJSON, err := s.deps.SettingsStore.LoadSetting("stack_order")
if err != nil {
    s.deps.Log.Debug("failed to load stack order", "error", err)
}
```

**Step 3: Fix cluster enabled LoadSetting (line 921)**

Replace:
```go
v, _ := s.deps.SettingsStore.LoadSetting(store.SettingClusterEnabled)
```

With:
```go
v, err := s.deps.SettingsStore.LoadSetting(store.SettingClusterEnabled)
if err != nil {
    s.deps.Log.Debug("failed to load cluster enabled setting", "error", err)
}
```

**Step 4: Build and test**

Run: `go build ./...`
Run: `go test ./internal/web/ -v`

**Step 5: Commit**

```bash
git add internal/web/handlers.go
git commit -m "fix: log GetMaintenance and LoadSetting failures in handlers"
```

---

## Task 4: Fix `api_containers.go` GetMaintenance discard

**Files:**
- Modify: `internal/web/api_containers.go:142`

**Step 1: Fix GetMaintenance (line 142)**

Replace:
```go
maintenance, _ := s.deps.Store.GetMaintenance(name)
```

With:
```go
maintenance, err := s.deps.Store.GetMaintenance(name)
if err != nil {
    s.deps.Log.Debug("failed to load maintenance state", "name", name, "error", err)
}
```

Note: Line 43 already handles the error properly — only line 142 needs fixing.

**Step 2: Build and test**

Run: `go build ./...`

**Step 3: Commit**

```bash
git add internal/web/api_containers.go
git commit -m "fix: log GetMaintenance failure in container detail API"
```

---

## Task 5: Fix `api_settings.go` and `handlers_auth.go` silent failures

**Files:**
- Modify: `internal/web/api_settings.go:559`
- Modify: `internal/web/handlers_auth.go:199, 202, 239, 378`

**Step 1: Fix cluster rollback SaveSetting (api_settings.go:559)**

Replace:
```go
_ = s.deps.SettingsStore.SaveSetting(store.SettingClusterEnabled, "false")
```

With:
```go
if err := s.deps.SettingsStore.SaveSetting(store.SettingClusterEnabled, "false"); err != nil {
    s.deps.Log.Warn("failed to rollback cluster enabled setting", "error", err)
}
```

**Step 2: Fix auth handlers (handlers_auth.go)**

Line 199 — `SeedBuiltinRoles`:
```go
if err := s.deps.Auth.Roles.SeedBuiltinRoles(); err != nil {
    s.deps.Log.Warn("failed to seed builtin roles", "error", err)
}
```

Line 202 — `SaveSetting("auth_setup_complete")`:
```go
if err := s.deps.Auth.Settings.SaveSetting("auth_setup_complete", "true"); err != nil {
    s.deps.Log.Warn("failed to save auth setup complete", "error", err)
}
```

Line 239 — `Logout`:
```go
if err := s.deps.Auth.Logout(token); err != nil {
    s.deps.Log.Debug("failed to clear session on logout", "error", err)
}
```

Line 378 — `DeleteSession`:
```go
if err := s.deps.Auth.Sessions.DeleteSession(sess.Token); err != nil {
    s.deps.Log.Debug("failed to delete revoked session", "error", err)
}
```

**Step 3: Build and test**

Run: `go build ./...`
Run: `go test ./internal/web/ -v`

**Step 4: Commit**

```bash
git add internal/web/api_settings.go internal/web/handlers_auth.go
git commit -m "fix: log silent failures in settings and auth handlers"
```

---

## Task 6: Fix `handlers_webauthn.go` credential update discards

**Files:**
- Modify: `internal/web/handlers_webauthn.go:306-307`

**Step 1: Fix WebAuthn credential update (lines 306-307)**

Replace:
```go
_ = s.deps.Auth.WebAuthnCreds.DeleteWebAuthnCredential(stored.ID)
_ = s.deps.Auth.WebAuthnCreds.CreateWebAuthnCredential(*stored)
```

With:
```go
if err := s.deps.Auth.WebAuthnCreds.DeleteWebAuthnCredential(stored.ID); err != nil {
    s.deps.Log.Warn("failed to delete old webauthn credential", "id", stored.ID, "error", err)
}
if err := s.deps.Auth.WebAuthnCreds.CreateWebAuthnCredential(*stored); err != nil {
    s.deps.Log.Warn("failed to create updated webauthn credential", "id", stored.ID, "error", err)
}
```

**Step 2: Build and test**

Run: `go build ./...`

**Step 3: Commit**

```bash
git add internal/web/handlers_webauthn.go
git commit -m "fix: log webauthn credential update failures"
```

---

## Task 7: Fix engine LoadSetting discards

**Files:**
- Modify: `internal/engine/updater.go:801`
- Modify: `internal/engine/update.go:582`

**Step 1: Fix ghcr_check_enabled (updater.go:801)**

Replace:
```go
val, _ := u.settings.LoadSetting("ghcr_check_enabled")
```

With:
```go
val, err := u.settings.LoadSetting("ghcr_check_enabled")
if err != nil {
    u.log.Debug("failed to load ghcr_check_enabled", "error", err)
}
```

**Step 2: Fix default_notify_mode (update.go:582)**

Replace:
```go
val, _ := u.settings.LoadSetting("default_notify_mode")
```

With:
```go
val, err := u.settings.LoadSetting("default_notify_mode")
if err != nil {
    u.log.Debug("failed to load default_notify_mode", "error", err)
}
```

**Step 3: Build and test**

Run: `go build ./...`
Run: `go test ./internal/engine/ -v`

**Step 4: Commit**

```bash
git add internal/engine/updater.go internal/engine/update.go
git commit -m "fix: log LoadSetting failures in engine"
```

---

## Task 8: Final build and test

**Step 1:** `go build ./...`
**Step 2:** `go test -race ./... -count=1`
**Step 3:** `go vet ./...`

---

## Summary

| Task | File(s) | Discards Fixed |
|------|---------|---------------|
| 1 | api_services.go | 7 (`AppendLog` ×3, `RecordUpdate`, `LoadSetting`, `SetPolicyOverride`, `SaveSetting` ×2) |
| 2 | api_notifications.go | 5 (`SaveSetting` ×4, `json.Unmarshal`, `SaveSetting`) |
| 3 | handlers.go | 5 (`GetMaintenance` ×3, `LoadSetting` ×2) |
| 4 | api_containers.go | 1 (`GetMaintenance`) |
| 5 | api_settings.go + handlers_auth.go | 5 (`SaveSetting`, `SeedBuiltinRoles`, `SaveSetting`, `Logout`, `DeleteSession`) |
| 6 | handlers_webauthn.go | 2 (`DeleteWebAuthnCredential`, `CreateWebAuthnCredential`) |
| 7 | updater.go + update.go | 2 (`LoadSetting` ×2) |
| 8 | — | Verification |

**Not changed (intentional):**
- `server.go` `_, _ = w.Write(data)` — HTTP response body writes after headers are committed. Nothing useful to do on error.
- `server.go` `_ = json.NewEncoder(w).Encode(v)` — same pattern, response already committed.
- `api_notifications.go:106` `_ = json.NewDecoder(r.Body).Decode(&body)` — empty body is valid (backward compat).
- `handlers_auth.go:70,153` `_ = r.ParseForm()` — stdlib pattern, errors surface later in form access.
- `handlers_auth.go:258` `passkeys, _ = ...ListWebAuthnCredentialsForUser(...)` — empty list fallback is correct UX.
