# Issue 59 Completion Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement all 6 remaining Nice-to-Have features from issue #59 and close it out.

**Architecture:** Six independent features, none sharing code. Queue export and health endpoints are trivial backend additions. Keyboard shortcuts are pure frontend. Atom feed is a new read-only endpoint with query-param token auth. Bulk actions extend the existing manage mode UI. Webhook retry wraps the existing `dispatch()` loop.

**Tech Stack:** Go stdlib (`encoding/xml`, `encoding/csv`, `net/http`), existing JS module system (esbuild), BoltDB settings.

---

## Task 1: Queue CSV/JSON Export

**Files:**
- Modify: `internal/web/api_queue.go` (add handler after line 37)
- Modify: `internal/web/server.go:350-351` (add route)
- Modify: `internal/web/static/queue.html:69-70` (add export buttons)
- Test: `internal/web/api_queue_test.go` (new)

**Step 1: Write failing tests**

Create `internal/web/api_queue_test.go`:

```go
package web

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type mockQueueForExport struct {
	items []PendingUpdate
}

func (m *mockQueueForExport) List() []PendingUpdate { return m.items }

func TestApiQueueExport_CSV(t *testing.T) {
	srv := &Server{deps: Deps{
		Queue: &mockQueueForExport{items: []PendingUpdate{
			{ContainerName: "nginx", CurrentImage: "nginx:1.24", NewImage: "nginx:1.25", DetectedAt: time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)},
		}},
	}}
	req := httptest.NewRequest("GET", "/api/queue/export?format=csv", nil)
	w := httptest.NewRecorder()
	srv.apiQueueExport(w, req)

	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/csv" {
		t.Fatalf("content-type = %q, want text/csv", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "sentinel-queue") {
		t.Fatalf("content-disposition = %q, want sentinel-queue filename", cd)
	}
	r := csv.NewReader(w.Body)
	rows, _ := r.ReadAll()
	if len(rows) != 2 { // header + 1 data row
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[1][0] != "nginx" {
		t.Fatalf("container = %q, want nginx", rows[1][0])
	}
}

func TestApiQueueExport_JSON(t *testing.T) {
	srv := &Server{deps: Deps{
		Queue: &mockQueueForExport{items: []PendingUpdate{
			{ContainerName: "redis", CurrentImage: "redis:7.0", NewImage: "redis:7.2"},
		}},
	}}
	req := httptest.NewRequest("GET", "/api/queue/export?format=json", nil)
	w := httptest.NewRecorder()
	srv.apiQueueExport(w, req)

	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "sentinel-queue") {
		t.Fatalf("missing content-disposition")
	}
	var items []PendingUpdate
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ContainerName != "redis" {
		t.Fatalf("got %+v", items)
	}
}

func TestApiQueueExport_Empty(t *testing.T) {
	srv := &Server{deps: Deps{
		Queue: &mockQueueForExport{},
	}}
	req := httptest.NewRequest("GET", "/api/queue/export?format=csv", nil)
	w := httptest.NewRecorder()
	srv.apiQueueExport(w, req)
	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
}
```

Note: The mock type name needs to match whatever `Queue` interface the `Deps` struct expects. Read `interfaces.go` to find the exact interface and adapt the mock. The `PendingUpdate` type may be in `interfaces.go` or `engine` package -- use the correct import.

**Step 2: Run tests, verify they fail**

```bash
cd /home/lns/Docker-Sentinel && go test ./internal/web/ -run TestApiQueueExport -v
```

Expected: compilation error, `apiQueueExport` not defined.

**Step 3: Implement handler**

Add to `internal/web/api_queue.go` after the `apiQueue` function:

```go
func (s *Server) apiQueueExport(w http.ResponseWriter, r *http.Request) {
	items := s.deps.Queue.List()
	format := r.URL.Query().Get("format")

	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=sentinel-queue.csv")
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"container", "current_image", "new_image", "detected_at", "type", "host_id"})
		for _, item := range items {
			_ = cw.Write([]string{
				item.ContainerName,
				item.CurrentImage,
				item.NewImage,
				item.DetectedAt.Format(time.RFC3339),
				item.Type,
				item.HostID,
			})
		}
		cw.Flush()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=sentinel-queue.json")
		_ = json.NewEncoder(w).Encode(items)
	}
}
```

Add import for `"encoding/csv"` and `"time"` if not already present.

**Step 4: Register route**

In `server.go` after line 351 (queue routes):

```go
s.mux.Handle("GET /api/queue/export", perm(auth.PermContainersView, s.apiQueueExport))
```

**Step 5: Run tests, verify they pass**

```bash
cd /home/lns/Docker-Sentinel && go test ./internal/web/ -run TestApiQueueExport -v
```

**Step 6: Add UI buttons**

In `queue.html`, find the page header area and add export links matching the history page pattern:

```html
<div class="page-header-actions">
    <a href="/api/queue/export?format=json" class="btn btn-sm">Export JSON</a>
    <a href="/api/queue/export?format=csv" class="btn btn-sm">Export CSV</a>
</div>
```

**Step 7: Commit**

```bash
git add internal/web/api_queue.go internal/web/api_queue_test.go internal/web/server.go internal/web/static/queue.html
git commit -m "feat: add queue CSV/JSON export (#59)"
```

---

## Task 2: Health Check Endpoints

**Files:**
- Create: `internal/web/api_health.go`
- Modify: `internal/web/server.go:285-312` (add routes in pre-auth section)
- Test: `internal/web/api_health_test.go` (new)

**Step 1: Write failing tests**

Create `internal/web/api_health_test.go`:

```go
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz_ReturnsOK(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	srv.apiHealthz(w, req)

	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Fatalf("status = %q, want ok", resp["status"])
	}
}

func TestReadyz_AllHealthy(t *testing.T) {
	srv := &Server{deps: Deps{
		Docker:        &mockContainerLister{},
		SettingsStore: &mockSettingsStore{data: map[string]string{}},
	}}
	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	srv.apiReadyz(w, req)

	if w.Code != 200 {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var resp struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "ready" {
		t.Fatalf("status = %q, want ready", resp.Status)
	}
	if resp.Checks["db"] != "ok" {
		t.Fatalf("db check = %q", resp.Checks["db"])
	}
}

func TestReadyz_DockerDown(t *testing.T) {
	srv := &Server{deps: Deps{
		Docker:        &mockContainerLister{err: fmt.Errorf("connection refused")},
		SettingsStore: &mockSettingsStore{data: map[string]string{}},
	}}
	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	srv.apiReadyz(w, req)

	if w.Code != 503 {
		t.Fatalf("got %d, want 503", w.Code)
	}
}
```

Note: Adapt mock types to match whatever interfaces `Deps.Docker` and `Deps.SettingsStore` use. The Docker mock needs a `ListContainers(ctx)` that can return an error. The settings mock needs `LoadSetting(key)` that succeeds.

**Step 2: Run tests, verify they fail**

```bash
cd /home/lns/Docker-Sentinel && go test ./internal/web/ -run "TestHealthz|TestReadyz" -v
```

**Step 3: Implement handlers**

Create `internal/web/api_health.go`:

```go
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

func (s *Server) apiHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) apiReadyz(w http.ResponseWriter, _ *http.Request) {
	checks := map[string]string{}
	healthy := true

	// Check DB.
	if s.deps.SettingsStore != nil {
		if _, err := s.deps.SettingsStore.LoadSetting("_healthcheck"); err != nil && err.Error() != "" {
			checks["db"] = err.Error()
			healthy = false
		} else {
			checks["db"] = "ok"
		}
	} else {
		checks["db"] = "not configured"
		healthy = false
	}

	// Check Docker.
	if s.deps.Docker != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := s.deps.Docker.ListContainers(ctx); err != nil {
			checks["docker"] = err.Error()
			healthy = false
		} else {
			checks["docker"] = "ok"
		}
	} else {
		checks["docker"] = "not configured"
		healthy = false
	}

	status := "ready"
	code := http.StatusOK
	if !healthy {
		status = "not_ready"
		code = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"status": status,
		"checks": checks,
	})
}
```

Note: The DB check approach needs adjustment based on what `LoadSetting` actually returns for missing keys. Read the implementation -- it likely returns `("", nil)` for missing keys. A non-nil error means BoltDB itself is broken, which is what we want to detect.

**Step 4: Register routes in pre-auth section**

In `server.go`, in the public routes section (after line 312):

```go
s.mux.HandleFunc("GET /healthz", s.apiHealthz)
s.mux.HandleFunc("GET /readyz", s.apiReadyz)
```

No auth wrapper -- these are public.

**Step 5: Run tests, verify they pass**

```bash
cd /home/lns/Docker-Sentinel && go test ./internal/web/ -run "TestHealthz|TestReadyz" -v
```

**Step 6: Commit**

```bash
git add internal/web/api_health.go internal/web/api_health_test.go internal/web/server.go
git commit -m "feat: add /healthz and /readyz health check endpoints (#59)"
```

---

## Task 3: Keyboard Shortcuts for Queue

**Files:**
- Modify: `internal/web/static/src/js/queue.js` (add keyboard handler)
- Modify: `internal/web/static/src/css/queue.css` or similar (add focus style)
- Modify: `internal/web/static/queue.html` (add help hint button)
- Modify: `internal/web/static/src/js/main.js` (export new functions)
- Rebuild: `make frontend`

**Step 1: Add keyboard handler to queue.js**

At the end of `queue.js`, before the export block, add:

```js
let kbFocusIndex = -1;

function getQueueRows() {
    return Array.from(document.querySelectorAll('tr.container-row[data-queue-key]'));
}

function setKbFocus(index) {
    const rows = getQueueRows();
    rows.forEach(r => r.classList.remove('kb-focused'));
    if (index < 0 || index >= rows.length) return;
    kbFocusIndex = index;
    rows[index].classList.add('kb-focused');
    rows[index].scrollIntoView({ block: 'nearest' });
}

function handleQueueKeydown(e) {
    // Skip if typing in an input, modal open, or manage mode active.
    const tag = document.activeElement?.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
    if (document.querySelector('.modal.active, .modal[style*="flex"]')) return;

    const rows = getQueueRows();
    if (rows.length === 0) return;

    switch (e.key) {
        case 'j':
            e.preventDefault();
            setKbFocus(Math.min(kbFocusIndex + 1, rows.length - 1));
            break;
        case 'k':
            e.preventDefault();
            setKbFocus(Math.max(kbFocusIndex - 1, 0));
            break;
        case 'a':
            e.preventDefault();
            if (kbFocusIndex >= 0 && rows[kbFocusIndex]) {
                approveUpdate(rows[kbFocusIndex].dataset.queueKey);
            }
            break;
        case 'r':
            e.preventDefault();
            if (kbFocusIndex >= 0 && rows[kbFocusIndex]) {
                rejectUpdate(rows[kbFocusIndex].dataset.queueKey);
            }
            break;
        case 'i':
            e.preventDefault();
            if (kbFocusIndex >= 0 && rows[kbFocusIndex]) {
                ignoreUpdate(rows[kbFocusIndex].dataset.queueKey);
            }
            break;
        case 'Enter':
        case ' ':
            e.preventDefault();
            if (kbFocusIndex >= 0) {
                toggleQueueAccordion(kbFocusIndex);
            }
            break;
        case '?':
            e.preventDefault();
            toggleShortcutsHelp();
            break;
    }
}

function toggleShortcutsHelp() {
    let overlay = document.getElementById('shortcuts-help');
    if (overlay) {
        overlay.remove();
        return;
    }
    overlay = document.createElement('div');
    overlay.id = 'shortcuts-help';
    overlay.className = 'shortcuts-overlay';

    const card = document.createElement('div');
    card.className = 'shortcuts-card';

    const title = document.createElement('h3');
    title.textContent = 'Keyboard Shortcuts';
    card.appendChild(title);

    const shortcuts = [
        ['j / k', 'Navigate down / up'],
        ['a', 'Approve focused update'],
        ['r', 'Reject focused update'],
        ['i', 'Ignore focused update'],
        ['Enter', 'Toggle details'],
        ['?', 'Toggle this help'],
    ];

    const table = document.createElement('table');
    for (const [key, desc] of shortcuts) {
        const tr = document.createElement('tr');
        const tdKey = document.createElement('td');
        const kbd = document.createElement('kbd');
        kbd.textContent = key;
        tdKey.appendChild(kbd);
        const tdDesc = document.createElement('td');
        tdDesc.textContent = desc;
        tr.appendChild(tdKey);
        tr.appendChild(tdDesc);
        table.appendChild(tr);
    }
    card.appendChild(table);

    const btn = document.createElement('button');
    btn.className = 'btn btn-sm';
    btn.textContent = 'Close';
    btn.addEventListener('click', toggleShortcutsHelp);
    card.appendChild(btn);

    overlay.appendChild(card);
    document.body.appendChild(overlay);
}

function initQueueKeyboard() {
    document.addEventListener('keydown', handleQueueKeydown);
}

function cleanupQueueKeyboard() {
    document.removeEventListener('keydown', handleQueueKeydown);
    kbFocusIndex = -1;
}
```

Export `initQueueKeyboard`, `cleanupQueueKeyboard`, `toggleShortcutsHelp` and bind to `window` in `main.js`.

**Step 2: Add CSS for focus and overlay**

Add to the appropriate CSS file (queue.css or components.css):

```css
tr.container-row.kb-focused {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
}

.shortcuts-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
}

.shortcuts-card {
    background: var(--card-bg);
    border-radius: 8px;
    padding: 1.5rem;
    min-width: 300px;
}

.shortcuts-card kbd {
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 3px;
    padding: 2px 6px;
    font-family: monospace;
}

.shortcuts-card table td {
    padding: 4px 12px;
}
```

**Step 3: Add hint button in queue toolbar**

In `queue.html`, add in the toolbar area:

```html
<button class="btn btn-sm btn-icon" onclick="toggleShortcutsHelp()" title="Keyboard shortcuts (?)">?</button>
```

**Step 4: Wire up init**

Call `initQueueKeyboard()` in the queue page initialisation path (find where queue-specific setup happens in `main.js` DOMContentLoaded and add the call there, or call it from the queue template's inline script).

**Step 5: Build frontend**

```bash
cd /home/lns/Docker-Sentinel && make frontend
```

**Step 6: Commit**

```bash
git add internal/web/static/
git commit -m "feat: add keyboard shortcuts for queue page (#59)"
```

---

## Task 4: Atom Feed for History

**Files:**
- Create: `internal/web/api_feed.go`
- Modify: `internal/web/server.go` (add route in pre-auth section)
- Modify: `internal/web/static/history.html` (add auto-discovery link)
- Test: `internal/web/api_feed_test.go` (new)

**Step 1: Write failing tests**

Create `internal/web/api_feed_test.go`:

```go
package web

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAtomFeed_ValidToken(t *testing.T) {
	// Create mock with history records and auth that validates "test-token".
	// Details depend on exact auth interface -- read auth.Service.
	// Call srv.apiHistoryFeed, assert 200 + valid Atom XML + correct entry count.
}

func TestAtomFeed_MissingToken(t *testing.T) {
	// No token param -> 401.
}

func TestAtomFeed_InvalidToken(t *testing.T) {
	// Bad token param -> 401.
}

func TestAtomFeed_EmptyHistory(t *testing.T) {
	// Valid token, no records -> 200 + empty feed.
}
```

Note: The auth mock needs to implement the token validation interface. Read `auth.Service` to understand what method validates tokens (`ValidateBearerToken`) and mock accordingly. The history mock needs `ListHistory(limit, cursor)`.

**Step 2: Implement handler**

Create `internal/web/api_feed.go`:

```go
package web

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"time"
)

type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	XMLNS   string      `xml:"xmlns,attr"`
	Title   string      `xml:"title"`
	Link    atomLink    `xml:"link"`
	Updated string      `xml:"updated"`
	ID      string      `xml:"id"`
	Entry   []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
}

type atomEntry struct {
	Title   string `xml:"title"`
	ID      string `xml:"id"`
	Updated string `xml:"updated"`
	Summary string `xml:"summary"`
}

const maxFeedEntries = 50

func (s *Server) apiHistoryFeed(w http.ResponseWriter, r *http.Request) {
	// Validate API token from query parameter.
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "token required", http.StatusUnauthorized)
		return
	}
	if _, err := s.deps.Auth.ValidateBearerToken(token); err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	records, err := s.deps.Store.ListHistory(maxFeedEntries, "")
	if err != nil {
		http.Error(w, "failed to load history", http.StatusInternalServerError)
		return
	}

	updated := time.Now().Format(time.RFC3339)
	if len(records) > 0 {
		updated = records[0].Timestamp.Format(time.RFC3339)
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	selfLink := fmt.Sprintf("%s://%s%s", scheme, r.Host, r.URL.Path)

	feed := atomFeed{
		XMLNS:   "http://www.w3.org/2005/Atom",
		Title:   "Docker Sentinel Updates",
		Link:    atomLink{Href: selfLink, Rel: "self", Type: "application/atom+xml"},
		Updated: updated,
		ID:      selfLink,
	}

	for _, rec := range records {
		title := fmt.Sprintf("Updated %s: %s to %s", rec.ContainerName, rec.OldImage, rec.NewImage)
		summary := fmt.Sprintf("Outcome: %s, Duration: %s", rec.Outcome, rec.Duration)
		if rec.Error != "" {
			summary += fmt.Sprintf(", Error: %s", rec.Error)
		}
		if rec.HostName != "" {
			summary += fmt.Sprintf(" (host: %s)", rec.HostName)
		}
		feed.Entry = append(feed.Entry, atomEntry{
			Title:   title,
			ID:      fmt.Sprintf("urn:sentinel:update:%s:%d", rec.ContainerName, rec.Timestamp.UnixNano()),
			Updated: rec.Timestamp.Format(time.RFC3339),
			Summary: summary,
		})
	}

	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	xml.NewEncoder(w).Encode(feed)
}
```

Note: Adjust the `s.deps.Auth.ValidateBearerToken` call to match the actual method signature. Read `auth.Service` to confirm. The `s.deps.Store.ListHistory` call needs to match the actual interface signature (check if it takes `limit int, cursor string` or different params).

**Step 3: Register route (pre-auth section)**

In `server.go`, in the public routes section:

```go
s.mux.HandleFunc("GET /api/history/feed", s.apiHistoryFeed)
```

No auth middleware -- the handler does its own token validation via query param.

**Step 4: Add auto-discovery link**

In `history.html`, in the `<head>` section (before `</head>`):

```html
<link rel="alternate" type="application/atom+xml" title="Docker Sentinel Updates" href="/api/history/feed">
```

**Step 5: Run tests**

```bash
cd /home/lns/Docker-Sentinel && go test ./internal/web/ -run "TestAtomFeed" -v
```

**Step 6: Commit**

```bash
git add internal/web/api_feed.go internal/web/api_feed_test.go internal/web/server.go internal/web/static/history.html
git commit -m "feat: add Atom feed for update history with API token auth (#59)"
```

---

## Task 5: Bulk Container Actions

**Files:**
- Modify: `internal/web/static/index.html:346-355` (add buttons to bulk bar)
- Modify: `internal/web/static/src/js/dashboard.js` (add bulk action functions)
- Modify: `internal/web/static/src/css/dashboard.css` or similar (add divider style)
- Modify: `internal/web/static/src/js/main.js` (export new functions)
- Rebuild: `make frontend`

**Step 1: Add buttons to bulk bar**

In `index.html`, update the `#bulk-bar` div to add restart/stop/start buttons after a visual divider:

```html
<div id="bulk-bar" class="bulk-bar" style="display:none">
    <span id="bulk-count" class="bulk-count">0 selected</span>
    <select id="bulk-policy" class="policy-select">
        <option value="auto">auto</option>
        <option value="manual">manual</option>
        <option value="pinned">pinned</option>
    </select>
    <button class="btn btn-success" onclick="applyBulkPolicy()">Apply Policy</button>
    <div class="bulk-divider"></div>
    <button class="btn btn-warning" onclick="bulkContainerAction('restart')">Restart</button>
    <button class="btn" onclick="bulkContainerAction('stop')">Stop</button>
    <button class="btn" onclick="bulkContainerAction('start')">Start</button>
    <button class="btn" onclick="clearSelection()">Cancel</button>
</div>
```

Add CSS for `.bulk-divider`:

```css
.bulk-divider {
    width: 1px;
    height: 24px;
    background: var(--border);
    margin: 0 8px;
}
```

**Step 2: Implement bulk action function**

In `dashboard.js`, add:

```js
async function bulkContainerAction(action) {
    const names = Object.keys(selectedContainers).filter(k => selectedContainers[k]);
    if (names.length === 0) return;

    const label = action.charAt(0).toUpperCase() + action.slice(1);
    const confirmed = await showConfirm(
        label + ' ' + names.length + ' container' + (names.length > 1 ? 's' : '') + '?',
        'This will ' + action + ': ' + names.join(', '),
        action === 'stop' || action === 'restart' ? 'danger' : 'confirm'
    );
    if (!confirmed) return;

    const countEl = document.getElementById('bulk-count');
    const originalText = countEl.textContent;
    let succeeded = 0;
    const failed = [];

    for (let idx = 0; idx < names.length; idx++) {
        const name = names[idx];
        countEl.textContent = label + 'ing ' + (idx + 1) + '/' + names.length + '...';
        try {
            const resp = await fetch('/api/containers/' + encodeURIComponent(name) + '/' + action, { method: 'POST' });
            if (!resp.ok) {
                const data = await resp.json().catch(function() { return { error: resp.statusText }; });
                failed.push(name + ': ' + (data.error || resp.statusText));
            } else {
                succeeded++;
            }
        } catch (err) {
            failed.push(name + ': ' + err.message);
        }
        // 200ms stagger between requests.
        if (idx < names.length - 1) {
            await new Promise(function(resolve) { setTimeout(resolve, 200); });
        }
    }

    // Summary.
    if (failed.length === 0) {
        showToast(label + 'ed ' + succeeded + ' container' + (succeeded > 1 ? 's' : ''), 'success');
    } else {
        showToast(succeeded + ' succeeded, ' + failed.length + ' failed: ' + failed.join('; '), 'error');
    }

    clearSelection();
    countEl.textContent = originalText;
}
```

Export and bind to `window` in `main.js`.

**Step 3: Build frontend**

```bash
cd /home/lns/Docker-Sentinel && make frontend
```

**Step 4: Commit**

```bash
git add internal/web/static/
git commit -m "feat: add bulk restart/stop/start in manage mode (#59)"
```

---

## Task 6: Webhook Retry with Backoff

**Files:**
- Modify: `internal/notify/notifier.go:71-80` (add retry fields to Multi)
- Modify: `internal/notify/notifier.go:147-170` (update dispatch with retry)
- Modify: `internal/store/bolt.go:87-99` (add setting constants)
- Create: `internal/web/api_settings_notifications.go` (retry settings endpoints)
- Modify: `internal/web/server.go` (add routes)
- Modify: `internal/web/static/settings.html` (add retry section)
- Modify: `internal/web/static/src/js/settings-core.js` (add retry functions)
- Modify: `internal/web/static/src/js/main.js` (export new functions)
- Modify: `cmd/sentinel/main.go` (load retry settings at startup)
- Test: `internal/notify/retry_test.go` (new)
- Rebuild: `make frontend`

**Step 1: Write failing tests**

Create `internal/notify/retry_test.go`:

```go
package notify

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type failingNotifier struct {
	name  string
	calls atomic.Int32
	failN int // fail first N calls
}

func (f *failingNotifier) Name() string { return f.name }
func (f *failingNotifier) Send(_ context.Context, _ Event) error {
	n := int(f.calls.Add(1))
	if n <= f.failN {
		return fmt.Errorf("transient error attempt %d", n)
	}
	return nil
}

func TestDispatch_RetrySucceedsOnSecondAttempt(t *testing.T) {
	fn := &failingNotifier{name: "test", failN: 1}
	m := NewMulti(discardLogger(), fn)
	m.SetRetry(3, 10*time.Millisecond)

	ok := m.dispatch(context.Background(), Event{Type: EventUpdateSuccess, ContainerName: "nginx"})
	if !ok {
		t.Fatal("dispatch should have succeeded after retry")
	}
	if got := int(fn.calls.Load()); got != 2 {
		t.Fatalf("expected 2 calls, got %d", got)
	}
}

func TestDispatch_RetryExhausted(t *testing.T) {
	fn := &failingNotifier{name: "test", failN: 10}
	m := NewMulti(discardLogger(), fn)
	m.SetRetry(2, 10*time.Millisecond)

	ok := m.dispatch(context.Background(), Event{Type: EventUpdateSuccess, ContainerName: "nginx"})
	if ok {
		t.Fatal("dispatch should have failed after exhausting retries")
	}
	// 1 initial + 2 retries = 3
	if got := int(fn.calls.Load()); got != 3 {
		t.Fatalf("expected 3 calls, got %d", got)
	}
}

func TestDispatch_NoRetryByDefault(t *testing.T) {
	fn := &failingNotifier{name: "test", failN: 1}
	m := NewMulti(discardLogger(), fn)
	// No SetRetry call -- default is 0 retries.

	ok := m.dispatch(context.Background(), Event{Type: EventUpdateSuccess, ContainerName: "nginx"})
	if ok {
		t.Fatal("dispatch should have failed with no retries")
	}
	if got := int(fn.calls.Load()); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
}

func TestDispatch_RetryRespectsContextCancellation(t *testing.T) {
	fn := &failingNotifier{name: "test", failN: 10}
	m := NewMulti(discardLogger(), fn)
	m.SetRetry(5, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	m.dispatch(ctx, Event{Type: EventUpdateSuccess, ContainerName: "nginx"})
	// Should not retry since context is already cancelled.
	if got := int(fn.calls.Load()); got > 1 {
		t.Fatalf("expected at most 1 call with cancelled context, got %d", got)
	}
}
```

Note: `discardLogger()` -- check if a test logger helper already exists in the notify test files. If not, create one using `slog.New(slog.NewTextHandler(io.Discard, nil))`.

**Step 2: Run tests, verify they fail**

```bash
cd /home/lns/Docker-Sentinel && go test ./internal/notify/ -run TestDispatch_Retry -v
```

**Step 3: Add retry fields to Multi struct**

In `notifier.go`, add fields to `Multi` (after the `flushTimer` field):

```go
maxRetries   int           // 0 = disabled (default)
retryBackoff time.Duration // initial backoff, doubles each retry
```

Add setter:

```go
func (m *Multi) SetRetry(maxRetries int, backoff time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxRetries = maxRetries
	m.retryBackoff = backoff
}
```

**Step 4: Add retrySend method and update dispatch**

Add the `retrySend` method to `notifier.go`:

```go
func (m *Multi) retrySend(ctx context.Context, n Notifier, event Event) bool {
	m.mu.RLock()
	maxRetries := m.maxRetries
	backoff := m.retryBackoff
	m.mu.RUnlock()

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := n.Send(ctx, event); err != nil {
			m.log.Error("notification failed",
				"provider", n.Name(),
				"event", string(event.Type),
				"container", event.ContainerName,
				"attempt", attempt+1,
				"max_attempts", maxRetries+1,
				"error", err.Error(),
			)
			if attempt < maxRetries {
				delay := backoff * time.Duration(1<<uint(attempt))
				if delay > 30*time.Second {
					delay = 30 * time.Second
				}
				select {
				case <-ctx.Done():
					return false
				case <-time.After(delay):
				}
			}
		} else {
			if attempt > 0 {
				m.log.Info("notification succeeded after retry",
					"provider", n.Name(),
					"attempt", attempt+1,
				)
			}
			return true
		}
	}
	return false
}
```

Update `dispatch()` to use `retrySend` instead of direct `n.Send()`:

Replace the provider loop in `dispatch()` with:

```go
anyOK := false
for _, n := range notifiers {
    if m.retrySend(ctx, n, event) {
        anyOK = true
    }
}
return anyOK
```

**Step 5: Run tests, verify they pass**

```bash
cd /home/lns/Docker-Sentinel && go test ./internal/notify/ -run TestDispatch_Retry -v
```

**Step 6: Add settings constants**

In `store/bolt.go`, add after the verifier settings constants:

```go
// Notification retry settings.
SettingNotifyRetryCount   = "notification_retry_count"
SettingNotifyRetryBackoff = "notification_retry_backoff"
```

**Step 7: Add settings API**

Create `internal/web/api_settings_notifications.go` following the scanning settings pattern:

- `GET /api/settings/notifications/retry` -- returns `{"retry_count": "0", "retry_backoff": "2s"}`
- `POST /api/settings/notifications/retry` -- validates fields (count 0-3, backoff parseable as duration), saves, calls setter on the notifier

Register routes in `server.go`.

**Step 8: Add settings UI**

In `settings.html`, in the Notifications tab after the existing accordion sections, add a "Retry Behaviour" accordion:

- Retry count dropdown: 0 (Disabled), 1, 2, 3
- Initial backoff input: text field, default "2s"
- Description text explaining exponential backoff with 30s cap
- Save button

In `settings-core.js`, add `loadRetrySettings()` and `saveRetrySettings()` following the scanner settings pattern. Export and bind to `window` in `main.js`.

**Step 9: Wire up in main.go**

After notification setup, load retry settings from DB and call `notifier.SetRetry()`:

```go
if countStr, _ := db.LoadSetting(store.SettingNotifyRetryCount); countStr != "" {
	count, _ := strconv.Atoi(countStr)
	backoffStr, _ := db.LoadSetting(store.SettingNotifyRetryBackoff)
	backoff, err := time.ParseDuration(backoffStr)
	if err != nil {
		backoff = 2 * time.Second
	}
	notifier.SetRetry(count, backoff)
}
```

**Step 10: Build frontend and run full tests**

```bash
cd /home/lns/Docker-Sentinel && make frontend && make lint && make test
```

**Step 11: Commit**

```bash
git add internal/notify/notifier.go internal/notify/retry_test.go internal/store/bolt.go \
    internal/web/api_settings_notifications.go internal/web/server.go \
    internal/web/static/ cmd/sentinel/main.go
git commit -m "feat: add notification retry with exponential backoff (#59)"
```

---

## Final Verification

```bash
cd /home/lns/Docker-Sentinel && make lint && make test && go test -cover ./...
```

Update issue 59 on Gitea: tick all remaining checkboxes, add closing comment, close the issue.

---

## Execution Groups

These tasks can be parallelised:

**Group 1 (all independent, no shared files except server.go routes):**
- Agent A: Task 1 (Queue export) + Task 2 (Health endpoints)
- Agent B: Task 3 (Keyboard shortcuts)
- Agent C: Task 4 (Atom feed)

**Group 2 (after Group 1, independent of each other):**
- Agent D: Task 5 (Bulk container actions)
- Agent E: Task 6 (Webhook retry)

**Group 3 (after all):**
- `make lint && make test` -- verify no regressions
- `make frontend` -- rebuild bundle
- Deploy to .57, verify UI
