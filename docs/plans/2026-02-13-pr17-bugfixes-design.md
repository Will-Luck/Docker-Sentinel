# PR #17 Bugfix Design

## Context

Code review of PR #17 (rate limit awareness + stack reordering + credential management) identified 16 potential issues. After verification against the actual codebase, 13 are confirmed real bugs — 3 were false positives (C1 SettingsStore nil, L4 OverallHealth Limit==0, and the break-in-loop from the initial review).

## Verified Issues

### High Priority (fix before merge)

| # | Issue | File(s) | Root Cause |
|---|-------|---------|------------|
| H1 | FetchToken hardcoded to Docker Hub auth — sends non-Hub credentials to Hub's auth endpoint | `auth.go`, `checker.go` | Function only designed for Docker Hub, never generalised |
| H2 | `saveRegistryCredentials()` missing event parameter — broken in Firefox | `app.js`, `settings.html` | Implicit `event` global not available in Firefox strict mode |
| H3 | Test credential lookup by registry name, not ID — fails if registry field was edited | `api.go` | No ID field in test request body |
| L2 | Bulk queue action button never re-enabled after completion | `app.js` | `triggerBtn` disabled but never passed to `clearLoading()` |

### Medium Priority

| # | Issue | File(s) | Root Cause |
|---|-------|---------|------------|
| M2 | No input validation on credential save — empty fields, duplicates accepted | `api.go` | Validation step missing before save |
| M3 | CanProceed uses stale data within a scan — could overshoot reserve | `ratelimit.go` | No request counting between Record() calls |
| L1 | Drag starts from anywhere in tbody, not just handle | `app.js` | Empty if-block in dragstart handler |
| L3 | apiTestRegistryCredential uses http.DefaultClient (no timeout) | `api.go` | Should use custom client with explicit timeout |
| L5 | apiSaveDigestSettings partially applies before validation failure | `api.go` | Saves one-by-one instead of validate-all-then-save |

### Low Priority

| # | Issue | File(s) | Root Cause |
|---|-------|---------|------------|
| M1 | Rate limit headers only captured from ListTags | `checker.go` | DistributionInspect SDK doesn't expose headers; document limitation |
| M4 | ListTags hardcoded to Docker Hub URL | `tags.go`, `checker.go` | Known limitation; document it |
| M5 | json.Unmarshal error swallowed in stack order loading | `handlers.go` | Error assigned to `_` |
| M6 | "Images" column header should be "Containers" | `app.js` | Misleading label |
| M7 | Design doc says 3 tabs, implementation has 6 | `docs/plans/` | Outdated documentation |

### False Positives (no action)

| # | Issue | Why false positive |
|---|-------|--------------------|
| C1 | SettingsStore nil dereference | SettingsStore always initialised in NewServer(); LoadSetting returns "" for missing keys |
| L4 | OverallHealth returns "ok" when Limit==0 | Intentional — unknown limits skip health degradation |

## Fix Approach

All fixes are isolated, single-concern changes grouped by file to minimise churn. No architectural changes.

### auth.go + checker.go (H1)
Make `FetchToken()` registry-aware. Accept `registryHost` parameter. Only hit `auth.docker.io` for Docker Hub. For other registries, use `https://<host>/v2/` with WWW-Authenticate challenge flow.

### app.js (H2, L1, L2, M6)
- H2: Add `event` parameter to `saveRegistryCredentials(event)` function signature
- L1: In `dragstart`, check `e.target.closest(".stack-drag-handle")` and return early if not from handle
- L2: Re-enable `triggerBtn` in the final `setTimeout` callback after all rows processed
- M6: Rename "Images" → "Containers" in rate limits table header

### settings.html (H2)
- Update onclick to pass event: `onclick="saveRegistryCredentials(event)"`

### api.go (H3, M2, L3, L5)
- H3: Add `id` field to test request; lookup by ID first, fall back to registry name
- M2: Validate non-empty registry/username/secret, reject duplicate registries
- L3: Replace `http.DefaultClient` with `&http.Client{Timeout: 10 * time.Second}`
- L5: Validate all digest settings fields first, then save all-or-nothing

### ratelimit.go (M3)
- Add `requestCount` to `RegistryState`, increment in `CanProceed`, reset on `Record()`

### handlers.go (M5)
- Log json.Unmarshal error at warn level instead of discarding

### checker.go + tags.go (M1, M4)
- Add documentation comments explaining Docker Hub-only limitations

### docs/plans/ (M7)
- Update QA design doc tab list to match implementation (6 tabs)
