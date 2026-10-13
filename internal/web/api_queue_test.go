package web

import (
	"encoding/csv"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// ---------------------------------------------------------------------------
// Mock: UpdateQueue
// ---------------------------------------------------------------------------

type mockQueue struct {
	items []PendingUpdate
}

func (m *mockQueue) List() []PendingUpdate { return m.items }
func (m *mockQueue) Get(name string) (PendingUpdate, bool) {
	for _, item := range m.items {
		if item.Key() == name {
			return item, true
		}
	}
	return PendingUpdate{}, false
}
func (m *mockQueue) Add(update PendingUpdate)                  { m.items = append(m.items, update) }
func (m *mockQueue) Approve(name string) (PendingUpdate, bool) { return PendingUpdate{}, false }
func (m *mockQueue) Remove(name string)                        {}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newQueueExportTestServer(q UpdateQueue) *Server {
	return &Server{
		deps: Dependencies{
			Queue:    q,
			EventBus: events.New(),
			Log:      slog.Default(),
		},
	}
}

// ---------------------------------------------------------------------------
// apiQueueExport tests
// ---------------------------------------------------------------------------

func TestApiQueueExport_CSV(t *testing.T) {
	detected := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	q := &mockQueue{
		items: []PendingUpdate{
			{
				ContainerName:          "nginx",
				CurrentImage:           "nginx:1.25",
				NewerVersions:          []string{"1.26"},
				ResolvedCurrentVersion: "1.25.0",
				ResolvedTargetVersion:  "1.26.0",
				DetectedAt:             detected,
				Type:                   "container",
				HostID:                 "",
			},
			{
				ContainerName: "postgres",
				CurrentImage:  "postgres:16",
				NewerVersions: []string{"17"},
				DetectedAt:    detected,
				Type:          "container",
				HostID:        "h1",
				HostName:      "remote-host",
			},
		},
	}
	srv := newQueueExportTestServer(q)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/queue/export?format=csv", nil)
	srv.apiQueueExport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	if ct != "text/csv" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/csv")
	}

	cd := w.Header().Get("Content-Disposition")
	if cd != "attachment; filename=sentinel-queue.csv" {
		t.Errorf("Content-Disposition = %q, want %q", cd, "attachment; filename=sentinel-queue.csv")
	}

	reader := csv.NewReader(strings.NewReader(w.Body.String()))
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}

	// Header + 2 data rows.
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (header + 2 data)", len(rows))
	}

	// Verify header.
	wantHeader := []string{"container", "current_image", "new_image", "detected_at", "type", "host_id"}
	for i, col := range wantHeader {
		if rows[0][i] != col {
			t.Errorf("header[%d] = %q, want %q", i, rows[0][i], col)
		}
	}

	// Verify first data row.
	if rows[1][0] != "nginx" {
		t.Errorf("row[1] container = %q, want %q", rows[1][0], "nginx")
	}
	if rows[1][1] != "nginx:1.25" {
		t.Errorf("row[1] current_image = %q, want %q", rows[1][1], "nginx:1.25")
	}
	if rows[1][4] != "container" {
		t.Errorf("row[1] type = %q, want %q", rows[1][4], "container")
	}

	// Verify second row has host_id.
	if rows[2][5] != "h1" {
		t.Errorf("row[2] host_id = %q, want %q", rows[2][5], "h1")
	}
}

func TestApiQueueExport_JSON(t *testing.T) {
	detected := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	q := &mockQueue{
		items: []PendingUpdate{
			{
				ContainerName: "nginx",
				CurrentImage:  "nginx:1.25",
				NewerVersions: []string{"1.26"},
				DetectedAt:    detected,
				Type:          "container",
			},
		},
	}
	srv := newQueueExportTestServer(q)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/queue/export?format=json", nil)
	srv.apiQueueExport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	cd := w.Header().Get("Content-Disposition")
	if cd != "attachment; filename=sentinel-queue.json" {
		t.Errorf("Content-Disposition = %q, want %q", cd, "attachment; filename=sentinel-queue.json")
	}

	var items []PendingUpdate
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].ContainerName != "nginx" {
		t.Errorf("container name = %q, want %q", items[0].ContainerName, "nginx")
	}
}

func TestApiQueueExport_DefaultFormat(t *testing.T) {
	// No format param should default to JSON.
	q := &mockQueue{items: []PendingUpdate{
		{ContainerName: "redis", CurrentImage: "redis:7", DetectedAt: time.Now()},
	}}
	srv := newQueueExportTestServer(q)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/queue/export", nil)
	srv.apiQueueExport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
}

func TestApiQueueExport_EmptyQueue_CSV(t *testing.T) {
	q := &mockQueue{items: nil}
	srv := newQueueExportTestServer(q)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/queue/export?format=csv", nil)
	srv.apiQueueExport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	reader := csv.NewReader(strings.NewReader(w.Body.String()))
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	// Header row only.
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (header only)", len(rows))
	}
}

func TestApiQueueExport_EmptyQueue_JSON(t *testing.T) {
	q := &mockQueue{items: nil}
	srv := newQueueExportTestServer(q)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/queue/export?format=json", nil)
	srv.apiQueueExport(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var items []PendingUpdate
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("got %d items, want 0", len(items))
	}
}

type recordingIgnores struct{ added map[string][]string }

func (r *recordingIgnores) AddIgnoredVersion(name, version string) error {
	if r.added == nil {
		r.added = map[string][]string{}
	}
	r.added[name] = append(r.added[name], version)
	return nil
}
func (r *recordingIgnores) GetIgnoredVersions(string) ([]string, error) { return nil, nil }
func (r *recordingIgnores) ClearIgnoredVersions(string) error           { return nil }

// A :latest update has no version list. Ignore keys it by the version the tag
// resolves to, else by the remote digest, as the engine's filter does.
func TestApiIgnoreVersion_MutableTagUsesResolvedVersionOrDigest(t *testing.T) {
	for _, tc := range []struct {
		name string
		item PendingUpdate
		want string
	}{
		{"resolved", PendingUpdate{ContainerName: "web", CurrentImage: "nginx:latest", RemoteDigest: "sha256:abc", ResolvedTargetVersion: "1.29.2"}, "1.29.2"},
		{"digest", PendingUpdate{ContainerName: "web", CurrentImage: "nginx:latest", RemoteDigest: "sha256:abc"}, "sha256:abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ig := &recordingIgnores{}
			srv := &Server{deps: Dependencies{Queue: &mockQueue{items: []PendingUpdate{tc.item}}, IgnoredVersions: ig, EventBus: events.New(), Log: slog.Default()}}
			r := httptest.NewRequest(http.MethodPost, "/api/ignore/web", nil)
			r.SetPathValue("key", "web")
			w := httptest.NewRecorder()
			srv.apiIgnoreVersion(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
			}
			if got := ig.added["web"]; len(got) != 1 || got[0] != tc.want {
				t.Errorf("stored ignore keys = %v, want [%s]", got, tc.want)
			}
		})
	}
}

func TestApiIgnoreVersion_NothingToKeyOnIsRejected(t *testing.T) {
	srv := &Server{deps: Dependencies{Queue: &mockQueue{items: []PendingUpdate{{ContainerName: "web"}}}, IgnoredVersions: &recordingIgnores{}, EventBus: events.New(), Log: slog.Default()}}
	r := httptest.NewRequest(http.MethodPost, "/api/ignore/web", nil)
	r.SetPathValue("key", "web")
	w := httptest.NewRecorder()
	srv.apiIgnoreVersion(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when there is no version and no digest", w.Code)
	}
}
