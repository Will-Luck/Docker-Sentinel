package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// Mock: ContainerLister that can fail
// ---------------------------------------------------------------------------

type mockContainerListerErr struct {
	err error
}

func (m *mockContainerListerErr) ListContainers(_ context.Context) ([]ContainerSummary, error) {
	return nil, m.err
}

func (m *mockContainerListerErr) ListAllContainers(_ context.Context) ([]ContainerSummary, error) {
	return nil, m.err
}

func (m *mockContainerListerErr) InspectContainer(_ context.Context, _ string) (ContainerInspect, error) {
	return ContainerInspect{}, m.err
}

// ---------------------------------------------------------------------------
// Mock: SettingsStore that can fail
// ---------------------------------------------------------------------------

type mockSettingsStoreErr struct {
	err error
}

func (m *mockSettingsStoreErr) SaveSetting(_, _ string) error        { return m.err }
func (m *mockSettingsStoreErr) LoadSetting(_ string) (string, error) { return "", m.err }
func (m *mockSettingsStoreErr) GetAllSettings() (map[string]string, error) {
	return nil, m.err
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newHealthTestServer(docker ContainerLister, settings SettingsStore) *Server {
	return &Server{
		deps: Dependencies{
			Docker:        docker,
			SettingsStore: settings,
			Log:           slog.Default(),
		},
	}
}

// ---------------------------------------------------------------------------
// apiHealthz tests
// ---------------------------------------------------------------------------

func TestApiHealthz(t *testing.T) {
	srv := newHealthTestServer(nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.apiHealthz(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["status"] != "ok" {
		t.Errorf("status = %q, want %q", got["status"], "ok")
	}
}

// ---------------------------------------------------------------------------
// apiReadyz tests
// ---------------------------------------------------------------------------

func TestApiReadyz_AllHealthy(t *testing.T) {
	docker := &mockContainerLister{containers: []ContainerSummary{
		{ID: "c1", Names: []string{"/nginx"}},
	}}
	settings := newMockSettingsStore()
	srv := newHealthTestServer(docker, settings)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	srv.apiReadyz(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["status"] != "ready" {
		t.Errorf("status = %q, want %q", got["status"], "ready")
	}

	checks, ok := got["checks"].(map[string]any)
	if !ok {
		t.Fatalf("checks is not an object: %T", got["checks"])
	}
	if checks["db"] != "ok" {
		t.Errorf("checks.db = %q, want %q", checks["db"], "ok")
	}
	if checks["docker"] != "ok" {
		t.Errorf("checks.docker = %q, want %q", checks["docker"], "ok")
	}
}

func TestApiReadyz_DockerDown(t *testing.T) {
	docker := &mockContainerListerErr{err: errors.New("connection refused")}
	settings := newMockSettingsStore()
	srv := newHealthTestServer(docker, settings)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	srv.apiReadyz(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["status"] != "not_ready" {
		t.Errorf("status = %q, want %q", got["status"], "not_ready")
	}

	checks := got["checks"].(map[string]any)
	if checks["db"] != "ok" {
		t.Errorf("checks.db = %q, want %q", checks["db"], "ok")
	}
	if checks["docker"] == "ok" {
		t.Error("checks.docker should not be 'ok' when Docker is down")
	}
}

func TestApiReadyz_DBDown(t *testing.T) {
	docker := &mockContainerLister{containers: []ContainerSummary{}}
	settings := &mockSettingsStoreErr{err: errors.New("db corrupted")}
	srv := newHealthTestServer(docker, settings)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	srv.apiReadyz(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["status"] != "not_ready" {
		t.Errorf("status = %q, want %q", got["status"], "not_ready")
	}

	checks := got["checks"].(map[string]any)
	if checks["db"] == "ok" {
		t.Error("checks.db should not be 'ok' when DB is down")
	}
	if checks["docker"] != "ok" {
		t.Errorf("checks.docker = %q, want %q", checks["docker"], "ok")
	}
}

func TestApiReadyz_NilDeps(t *testing.T) {
	// Both Docker and SettingsStore are nil — should report not_ready.
	srv := newHealthTestServer(nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	srv.apiReadyz(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["status"] != "not_ready" {
		t.Errorf("status = %q, want %q", got["status"], "not_ready")
	}
}
