package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// ---------------------------------------------------------------------------
// Mock: HistoryStore (minimal for dashboard tests)
// ---------------------------------------------------------------------------

type mockHistoryStore struct {
	maintenance map[string]bool
}

func newMockHistoryStore() *mockHistoryStore {
	return &mockHistoryStore{maintenance: make(map[string]bool)}
}

func (m *mockHistoryStore) ListHistory(_ int, _ string) ([]UpdateRecord, error) {
	return nil, nil
}

func (m *mockHistoryStore) ListAllHistory() ([]UpdateRecord, error) { return nil, nil }

func (m *mockHistoryStore) ListHistoryByContainer(_ string, _ int) ([]UpdateRecord, error) {
	return nil, nil
}

func (m *mockHistoryStore) GetMaintenance(name string) (bool, error) {
	return m.maintenance[name], nil
}

func (m *mockHistoryStore) RecordUpdate(_ UpdateRecord) error { return nil }

// ---------------------------------------------------------------------------
// Test helper: build a Server suitable for dashboard/container API tests
// ---------------------------------------------------------------------------

func newDashboardTestServer(
	docker ContainerLister,
	store HistoryStore,
	queue UpdateQueue,
	settings SettingsStore,
	swarm SwarmProvider,
	cluster *ClusterController,
) *Server {
	if cluster == nil {
		cluster = NewClusterController()
	}
	if queue == nil {
		queue = &mockUpdateQueue{}
	}
	if settings == nil {
		settings = newMockSettingsStore()
	}
	return &Server{
		deps: Dependencies{
			Docker:        docker,
			Store:         store,
			Queue:         queue,
			SettingsStore: settings,
			Swarm:         swarm,
			Cluster:       cluster,
			EventBus:      events.New(),
			Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
}

// ---------------------------------------------------------------------------
// apiContainers tests (JSON API)
// ---------------------------------------------------------------------------

func TestApiContainers_HappyPath(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{
				ID:     "c1",
				Names:  []string{"/nginx"},
				Image:  "nginx:latest",
				Labels: map[string]string{"sentinel.policy": "auto"},
				State:  "running",
			},
			{
				ID:     "c2",
				Names:  []string{"/redis"},
				Image:  "redis:7",
				Labels: map[string]string{"com.docker.compose.project": "cache"},
				State:  "running",
			},
		},
	}
	store := newMockHistoryStore()
	srv := newDashboardTestServer(docker, store, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers", nil)
	srv.apiContainers(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var result []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("got %d containers, want 2", len(result))
	}

	// Verify container fields.
	if result[0]["name"] != "nginx" {
		t.Errorf("result[0].name = %v, want %q", result[0]["name"], "nginx")
	}
	if result[0]["state"] != "running" {
		t.Errorf("result[0].state = %v, want %q", result[0]["state"], "running")
	}
	if result[1]["name"] != "redis" {
		t.Errorf("result[1].name = %v, want %q", result[1]["name"], "redis")
	}
	if result[1]["stack"] != "cache" {
		t.Errorf("result[1].stack = %v, want %q", result[1]["stack"], "cache")
	}
}

func TestApiContainers_FiltersSwarmTasks(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{
				ID:     "c1",
				Names:  []string{"/nginx"},
				Image:  "nginx:latest",
				Labels: map[string]string{},
				State:  "running",
			},
			{
				ID:    "c2",
				Names: []string{"/swarm-task.1"},
				Image: "traefik:v3",
				Labels: map[string]string{
					"com.docker.swarm.task": "abc123",
				},
				State: "running",
			},
		},
	}
	store := newMockHistoryStore()
	srv := newDashboardTestServer(docker, store, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers", nil)
	srv.apiContainers(w, r)

	var result []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Swarm task container should be filtered out.
	if len(result) != 1 {
		t.Fatalf("got %d containers, want 1 (Swarm tasks filtered)", len(result))
	}
	if result[0]["name"] != "nginx" {
		t.Errorf("result[0].name = %v, want %q", result[0]["name"], "nginx")
	}
}

func TestApiContainers_WithSwarmServices(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{
				ID:     "c1",
				Names:  []string{"/nginx"},
				Image:  "nginx:latest",
				Labels: map[string]string{},
				State:  "running",
			},
		},
	}
	swarm := &mockSwarmProvider{
		swarmMode: true,
		services: []ServiceDetail{
			{
				ServiceSummary: ServiceSummary{
					ID:    "svc1",
					Name:  "traefik",
					Image: "traefik:v3",
				},
			},
		},
	}
	store := newMockHistoryStore()
	srv := newDashboardTestServer(docker, store, nil, nil, swarm, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers", nil)
	srv.apiContainers(w, r)

	var result []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Should include both the local container and the Swarm service.
	if len(result) != 2 {
		t.Fatalf("got %d entries, want 2 (1 container + 1 service)", len(result))
	}

	// The service entry should have state="service".
	var serviceFound bool
	for _, entry := range result {
		if entry["name"] == "traefik" {
			serviceFound = true
			if entry["state"] != "service" {
				t.Errorf("traefik state = %v, want %q", entry["state"], "service")
			}
			if entry["stack"] != "swarm" {
				t.Errorf("traefik stack = %v, want %q", entry["stack"], "swarm")
			}
		}
	}
	if !serviceFound {
		t.Error("traefik service not found in response")
	}
}

func TestApiContainers_EmptyState(t *testing.T) {
	docker := &mockContainerLister{containers: []ContainerSummary{}}
	store := newMockHistoryStore()
	srv := newDashboardTestServer(docker, store, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers", nil)
	srv.apiContainers(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var result []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(result) != 0 {
		t.Errorf("got %d containers, want 0", len(result))
	}
}

func TestApiContainers_DockerError(t *testing.T) {
	docker := &errContainerLister{err: context.DeadlineExceeded}
	store := newMockHistoryStore()
	srv := newDashboardTestServer(docker, store, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers", nil)
	srv.apiContainers(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestApiContainers_WithMaintenance(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{
				ID:     "c1",
				Names:  []string{"/nginx"},
				Image:  "nginx:latest",
				Labels: map[string]string{},
				State:  "running",
			},
		},
	}
	store := newMockHistoryStore()
	store.maintenance["nginx"] = true
	srv := newDashboardTestServer(docker, store, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers", nil)
	srv.apiContainers(w, r)

	var result []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("got %d containers, want 1", len(result))
	}
	if result[0]["maintenance"] != true {
		t.Errorf("maintenance = %v, want true", result[0]["maintenance"])
	}
}

// ---------------------------------------------------------------------------
// apiContainerDetail tests
// ---------------------------------------------------------------------------

func TestApiContainerDetail_HappyPath(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{
				ID:     "abc123",
				Names:  []string{"/nginx"},
				Image:  "nginx:latest",
				Labels: map[string]string{"sentinel.policy": "auto"},
				State:  "running",
			},
		},
	}
	store := newMockHistoryStore()
	srv := newDashboardTestServer(docker, store, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerDetail(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if result["name"] != "nginx" {
		t.Errorf("name = %v, want %q", result["name"], "nginx")
	}
	if result["id"] != "abc123" {
		t.Errorf("id = %v, want %q", result["id"], "abc123")
	}
	if result["state"] != "running" {
		t.Errorf("state = %v, want %q", result["state"], "running")
	}
}

func TestApiContainerDetail_NotFound(t *testing.T) {
	docker := &mockContainerLister{containers: []ContainerSummary{}}
	store := newMockHistoryStore()
	srv := newDashboardTestServer(docker, store, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/ghost", nil)
	r.SetPathValue("name", "ghost")
	srv.apiContainerDetail(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestApiContainerDetail_InvalidName(t *testing.T) {
	docker := &mockContainerLister{}
	store := newMockHistoryStore()
	srv := newDashboardTestServer(docker, store, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/../etc/passwd", nil)
	r.SetPathValue("name", "../etc/passwd")
	srv.apiContainerDetail(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestApiContainerDetail_DockerError(t *testing.T) {
	docker := &errContainerLister{err: context.DeadlineExceeded}
	store := newMockHistoryStore()
	srv := newDashboardTestServer(docker, store, nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerDetail(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}
