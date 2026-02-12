# Bug Hunt & Code Review Findings

Date: 2026-02-12

## Scope reviewed

This review covered:

- Runtime entrypoint and wiring (`cmd/sentinel/main.go`)
- Core engine and state (`internal/engine/*`, `internal/store/*`, `internal/events/*`)
- Docker/registry integrations (`internal/docker/*`, `internal/registry/*`, `internal/guardian/*`)
- Auth/web layers (`internal/auth/*`, `internal/web/*`)
- Notification providers (`internal/notify/*`)
- Configuration and operational files (`internal/config/config.go`, `Dockerfile`, `Makefile`, `README.md`)

## Checks executed

1. `go test ./...`  
   Result: **failed** due blocked network dependency download (`github.com/go-webauthn/webauthn`).
2. `GOPROXY=direct go test ./...`  
   Result: **failed** due outbound network restrictions (GitHub access denied).
3. `go test ./internal/clock ./internal/config ./internal/docker ./internal/engine ./internal/events ./internal/guardian ./internal/logging ./internal/notify ./internal/registry ./internal/store`  
   Result: **passed** for all listed packages.

## High-priority issues

### 1) Login rate limiting can be bypassed because request source uses `RemoteAddr` (IP:port)

- Current login handlers pass `r.RemoteAddr` directly into auth rate-limiting calls.
- `RemoteAddr` usually contains an ephemeral source port (`1.2.3.4:54321`), so each attempt can appear as a different key.
- This weakens per-IP throttling and account lockout protections.

**Evidence**
- `internal/web/handlers_auth.go` uses `ip := r.RemoteAddr` then calls `Auth.Login(...)`.
- `internal/web/handlers_webauthn.go` calls `LoginWithWebAuthn(..., r.RemoteAddr, ...)`.
- `internal/auth/ratelimit.go` stores attempts in a map keyed by the supplied string.

**Recommended change**
- Normalize client identity before calling auth service:
  - Prefer trusted proxy headers only when explicitly enabled/configured.
  - Otherwise parse host from `RemoteAddr` via `net.SplitHostPort`.
  - Fallback safely if parsing fails.
- Add unit tests covering `host:port`, bare host, IPv6, malformed input.

### 2) Setup flow ignores session creation errors (can create invalid admin login state)

- In setup, session token generation and session creation errors are ignored.
- If token generation fails, an empty cookie/token path may still execute.
- If persistence fails, UI may set cookie for a non-existent session and appear "logged in" until next request.

**Evidence**
- `internal/web/handlers_auth.go`:
  - `sessionToken, _ := auth.GenerateSessionToken()`
  - `_ = s.deps.Auth.Sessions.CreateSession(session)`

**Recommended change**
- Handle both errors explicitly and return `500` with safe message.
- Only set session cookie after successful token generation + persisted session.
- Add setup handler tests for each failure path.

## Medium-priority issues

### 3) Some auth error paths use `http.Error` with JSON strings but do not set `Content-Type: application/json`

- Middleware writes JSON-shaped strings via `http.Error`, which defaults to `text/plain`.
- Clients expecting JSON can mis-handle responses.

**Evidence**
- `internal/auth/middleware.go` uses `http.Error(w, '{"error":...}', status)` in multiple branches.

**Recommended change**
- Replace with a small helper that sets `Content-Type: application/json` and marshals consistently.

### 4) TLS auto-cert SAN population can contain duplicate private IPs

- `selfSignedIPs()` appends private interface IPs without de-duplication.
- Not a functional correctness failure in most cases, but avoidable certificate bloat/noise.

**Evidence**
- `internal/web/tls.go` appends all matching interface addresses directly.

**Recommended change**
- Deduplicate via map/set before returning.

### 5) Dead/unused helper in config package (`envInt`)

- `envInt` exists but is not used.
- Increases maintenance surface and can mislead contributors.

**Evidence**
- `internal/config/config.go` defines `envInt` and no references in repository usage paths.

**Recommended change**
- Remove function or add a lint rule/check to catch unused helpers earlier.

## Test/quality gaps to prioritize

1. Add web/auth handler tests that validate:
   - client IP normalization,
   - setup-session error handling,
   - JSON content-type consistency.
2. Add CI fallback strategy for environments with restricted network:
   - vendor dependencies or internal module proxy option.
3. Add static checks in CI if not already enforced per PR:
   - `go vet ./...`
   - `golangci-lint run` with auth/web focused rules.

## Summary (what should be changed first)

1. Fix login IP normalization and add tests (security hardening).
2. Fix setup session error handling (correctness/reliability).
3. Standardize JSON error responses in auth middleware.
4. Clean minor hygiene items (TLS SAN dedupe, dead helper cleanup).
