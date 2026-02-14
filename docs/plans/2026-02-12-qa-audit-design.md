# QA Audit: Web Dashboard Deep Dive

> Date: 2026-02-12
> Method: Playwright live testing against the Sentinel dashboard
> Approach: Flow-based walkthrough (every user journey end-to-end)

## Test Categories

### 1. Auth Flows
- Login (valid, invalid, empty fields)
- Logout and session clearing
- Passkey registration/login (HTTPS secure context)
- Setup page redirect (already configured)
- Rate limiting behaviour

### 2. Dashboard Interactions
- Container list rendering
- Filter pills (Running, Stopped, Updatable, A-Z, Priority)
- Stack expand/collapse (individual + bulk)
- Container row accordion expansion
- Manage mode toggle
- Scan button spinner and SSE completion
- SSE indicator status

### 3. Container Actions (non-destructive)
- Per-container check button
- Policy dropdown changes (revert after)
- Container detail page navigation
- Row click accordion

### 4. Queue, History, Logs Pages
- Page load and table rendering
- Empty states
- Navigation between pages
- Queue badge accuracy

### 5. Settings Page
- Tab switching (Scanning, Notifications, Registries, Appearance, General, Security)
- Poll interval, default policy, grace period changes (revert after)
- Pause toggle
- Notification channel list
- User management list (admin)

### 6. Account Page
- User info display
- Sessions list
- API tokens section
- Passkey section (HTTPS)
- Password change validation

### 7. Edge Cases
- Mobile viewport (480px)
- Console errors on every page
- Back/forward navigation

## Safety Boundaries
- No production container stops/restarts/updates
- No password changes
- No auth disable
- All settings changes reverted after testing
