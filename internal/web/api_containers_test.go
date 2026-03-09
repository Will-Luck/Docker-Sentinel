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
// Mocks for container lifecycle operations
// ---------------------------------------------------------------------------

type mockRestarter struct {
	called bool
	err    error
}

func (m *mockRestarter) RestartContainer(_ context.Context, _ string) error {
	m.called = true
	return m.err
}

type mockStopper struct {
	called bool
	err    error
}

func (m *mockStopper) StopContainer(_ context.Context, _ string) error {
	m.called = true
	return m.err
}

type mockStarter struct {
	called bool
	err    error
}

func (m *mockStarter) StartContainer(_ context.Context, _ string) error {
	m.called = true
	return m.err
}

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

func newControlTestServer(
	docker ContainerLister,
	restarter ContainerRestarter,
	stopper ContainerStopper,
	starter ContainerStarter,
) *Server {
	return &Server{
		deps: Dependencies{
			Docker:    docker,
			Store:     newMockHistoryStore(),
			Queue:     &mockUpdateQueue{},
			Restarter: restarter,
			Stopper:   stopper,
			Starter:   starter,
			Cluster:   NewClusterController(),
			EventBus:  events.New(),
			Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
}

// ---------------------------------------------------------------------------
// apiRestart tests
// ---------------------------------------------------------------------------

func TestApiRestart_HappyPath(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}, State: "running"},
		},
	}
	restarter := &mockRestarter{}
	srv := newControlTestServer(docker, restarter, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/nginx/restart", nil)
	r.SetPathValue("name", "nginx")
	srv.apiRestart(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["status"] != "restarting" {
		t.Errorf("status = %q, want %q", result["status"], "restarting")
	}
	if result["name"] != "nginx" {
		t.Errorf("name = %q, want %q", result["name"], "nginx")
	}
}

func TestApiRestart_ContainerNotFound(t *testing.T) {
	docker := &mockContainerLister{containers: []ContainerSummary{}}
	restarter := &mockRestarter{}
	srv := newControlTestServer(docker, restarter, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/ghost/restart", nil)
	r.SetPathValue("name", "ghost")
	srv.apiRestart(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestApiRestart_InvalidName(t *testing.T) {
	srv := newControlTestServer(&mockContainerLister{}, &mockRestarter{}, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers//restart", nil)
	// PathValue("name") returns "" when empty.
	srv.apiRestart(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestApiRestart_SelfProtected(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{
				ID:    "abc123",
				Names: []string{"/sentinel"},
				Labels: map[string]string{
					"sentinel.self": "true",
				},
				State: "running",
			},
		},
	}
	restarter := &mockRestarter{}
	srv := newControlTestServer(docker, restarter, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/sentinel/restart", nil)
	r.SetPathValue("name", "sentinel")
	srv.apiRestart(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

func TestApiRestart_NilRestarter(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}, State: "running"},
		},
	}
	// Restarter intentionally nil.
	srv := newControlTestServer(docker, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/nginx/restart", nil)
	r.SetPathValue("name", "nginx")
	srv.apiRestart(w, r)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotImplemented)
	}
}

func TestApiRestart_DockerListError(t *testing.T) {
	docker := &errContainerLister{err: context.DeadlineExceeded}
	restarter := &mockRestarter{}
	srv := newControlTestServer(docker, restarter, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/nginx/restart", nil)
	r.SetPathValue("name", "nginx")
	srv.apiRestart(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------------------
// apiStop tests
// ---------------------------------------------------------------------------

func TestApiStop_HappyPath(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/redis"}, State: "running"},
		},
	}
	stopper := &mockStopper{}
	srv := newControlTestServer(docker, nil, stopper, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/redis/stop", nil)
	r.SetPathValue("name", "redis")
	srv.apiStop(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["status"] != "stopping" {
		t.Errorf("status = %q, want %q", result["status"], "stopping")
	}
}

func TestApiStop_ContainerNotFound(t *testing.T) {
	docker := &mockContainerLister{containers: []ContainerSummary{}}
	stopper := &mockStopper{}
	srv := newControlTestServer(docker, nil, stopper, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/ghost/stop", nil)
	r.SetPathValue("name", "ghost")
	srv.apiStop(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestApiStop_InvalidName(t *testing.T) {
	srv := newControlTestServer(&mockContainerLister{}, nil, &mockStopper{}, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers//stop", nil)
	srv.apiStop(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestApiStop_SelfProtected(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{
				ID:     "abc123",
				Names:  []string{"/sentinel"},
				Labels: map[string]string{"sentinel.self": "true"},
				State:  "running",
			},
		},
	}
	stopper := &mockStopper{}
	srv := newControlTestServer(docker, nil, stopper, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/sentinel/stop", nil)
	r.SetPathValue("name", "sentinel")
	srv.apiStop(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestApiStop_NilStopper(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}, State: "running"},
		},
	}
	srv := newControlTestServer(docker, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/nginx/stop", nil)
	r.SetPathValue("name", "nginx")
	srv.apiStop(w, r)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotImplemented)
	}
}

// ---------------------------------------------------------------------------
// apiStart tests
// ---------------------------------------------------------------------------

func TestApiStart_HappyPath(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/postgres"}, State: "exited"},
		},
	}
	starter := &mockStarter{}
	srv := newControlTestServer(docker, nil, nil, starter)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/postgres/start", nil)
	r.SetPathValue("name", "postgres")
	srv.apiStart(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["status"] != "starting" {
		t.Errorf("status = %q, want %q", result["status"], "starting")
	}
}

func TestApiStart_ContainerNotFound(t *testing.T) {
	docker := &mockContainerLister{containers: []ContainerSummary{}}
	starter := &mockStarter{}
	srv := newControlTestServer(docker, nil, nil, starter)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/ghost/start", nil)
	r.SetPathValue("name", "ghost")
	srv.apiStart(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestApiStart_InvalidName(t *testing.T) {
	srv := newControlTestServer(&mockContainerLister{}, nil, nil, &mockStarter{})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers//start", nil)
	srv.apiStart(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestApiStart_SelfProtected(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{
				ID:     "abc123",
				Names:  []string{"/sentinel"},
				Labels: map[string]string{"sentinel.self": "true"},
				State:  "exited",
			},
		},
	}
	starter := &mockStarter{}
	srv := newControlTestServer(docker, nil, nil, starter)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/sentinel/start", nil)
	r.SetPathValue("name", "sentinel")
	srv.apiStart(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestApiStart_NilStarter(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}, State: "exited"},
		},
	}
	srv := newControlTestServer(docker, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/nginx/start", nil)
	r.SetPathValue("name", "nginx")
	srv.apiStart(w, r)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotImplemented)
	}
}

func TestApiStart_DockerListError(t *testing.T) {
	docker := &errContainerLister{err: context.DeadlineExceeded}
	starter := &mockStarter{}
	srv := newControlTestServer(docker, nil, nil, starter)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/containers/nginx/start", nil)
	r.SetPathValue("name", "nginx")
	srv.apiStart(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}
