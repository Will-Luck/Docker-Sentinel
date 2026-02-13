# Docker-Sentinel QA Deep Dive — Full Sweep

**Date:** 2026-02-13
**Scope:** All pages, flows, buttons, and interactive elements
**Method:** Interactive Playwright walkthrough on live instance (`http://192.168.1.57:62850`)
**Constraint:** No destructive operations (stop/start/update) on production containers

## Surface Area

- 10 pages (Dashboard, Login, Setup, Queue, Container Detail, Settings, History, Logs, Account, Error)
- 70+ API endpoints with RBAC
- 100+ interactive elements (buttons, dropdowns, forms, accordions)
- 3 auth flows (password, passkey/WebAuthn, API tokens)
- SSE real-time updates
- Bulk operations, digest notifications, per-container preferences

## Test Plan

### Phase 1: Auth Flows

| # | Test | Expected |
|---|------|----------|
| 1 | Login page render | Form with username, password, sign-in button; passkey button if WebAuthn available |
| 2 | Wrong password | Error message displayed, not redirected |
| 3 | Successful login | Redirect to dashboard, session cookie set |
| 4 | Logout | Session cleared, redirect to login |
| 5 | Passkey availability check | Button present/hidden based on registered passkeys |
| 6 | Account page: change password | Form submits, success toast, can re-login with new password |
| 7 | Account page: sessions list | Shows current session, revoke buttons work |
| 8 | Account page: API tokens | Create token, copy to clipboard, delete token |
| 9 | Rate limiting / account lockout | Repeated wrong passwords trigger lockout message |

### Phase 2: Dashboard

| # | Test | Expected |
|---|------|----------|
| 10 | Container list renders | All running containers visible, stats cards correct |
| 11 | Filter pills — Running | Only running containers shown |
| 12 | Filter pills — Stopped | Only stopped containers shown |
| 13 | Filter pills — Updatable | Only containers with pending updates shown |
| 14 | Sort — A-Z | Containers sorted alphabetically |
| 15 | Sort — Status | Containers sorted by status |
| 16 | Stack expand/collapse | Click stack header toggles group |
| 17 | Expand All / Collapse All | All stacks expand or collapse |
| 18 | Manage mode toggle | Checkboxes appear, bulk action bar visible |
| 19 | Policy dropdown per container | Dropdown changes policy, toast confirms |
| 20 | Status badge hover | Stop/Start button appears on hover |
| 21 | SSE connection indicator | Green dot + "Connected" text |
| 22 | Container row click | Accordion expands with detail |

### Phase 3: Container Detail

| # | Test | Expected |
|---|------|----------|
| 23 | Navigate to detail page | Page loads with container info |
| 24 | Accordion sections | All expand/collapse correctly |
| 25 | Policy radio buttons | Changing policy shows toast, persists on refresh |
| 26 | Check for Updates button | Triggers check, result displayed |
| 27 | History section | Shows past updates if any |
| 28 | Snapshots section | Shows available rollback points if any |
| 29 | Versions section | Shows available image versions |
| 30 | Notification preference selector | Changes persist on refresh |
| 31 | Container ID copy button | Copies full ID to clipboard |

### Phase 4: Queue

| # | Test | Expected |
|---|------|----------|
| 32 | Queue page renders | Shows pending items or "empty" state |
| 33 | Approve/Reject buttons | Buttons present if items exist, wired to correct endpoints |

### Phase 5: Settings

| # | Test | Expected |
|---|------|----------|
| 34 | Tab navigation | All 4 tabs (General/Notifications/Appearance/Security) switch content |
| 35 | Poll interval change | Preset and custom values save correctly |
| 36 | Default policy change | Dropdown changes, persists on refresh |
| 37 | Grace period change | Preset and custom values save correctly |
| 38 | Pause/unpause scanning | Toggle works, pause banner appears on dashboard |
| 39 | Filter patterns save | Textarea saves, patterns applied |
| 40 | Notification channel add/edit/test | Channel form renders, test button fires notification |
| 41 | Theme toggle | Dark/light/auto applies immediately |
| 42 | Security tab: user management | Create user form, user list, delete button (admin only) |

### Phase 6: History & Logs

| # | Test | Expected |
|---|------|----------|
| 43 | History page | Renders past updates with timestamps, digests, outcomes |
| 44 | Logs page | Renders activity events in timeline |

### Phase 7: Edge Cases

| # | Test | Expected |
|---|------|----------|
| 45 | Non-existent container URL | Error page with message and dashboard link |
| 46 | Direct URL while logged out | Redirect to login, then back to original URL after login |
| 47 | CSRF token on forms | All POST/PUT/DELETE include X-CSRF-Token header |
| 48 | Bulk policy preview + cancel | Preview shows changes, cancel returns to normal |
| 49 | SSE reconnection | Disconnect SSE, verify auto-reconnect |

## Bug Reporting

Each bug found will be documented with:
- Screenshot of the issue
- Steps to reproduce
- Expected vs actual behaviour
- Severity (Critical / Major / Minor / Cosmetic)
- Affected file(s) if identifiable from code
