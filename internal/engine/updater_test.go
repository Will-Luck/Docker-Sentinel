package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/GiteaLN/Docker-Sentinel/internal/config"
	"github.com/GiteaLN/Docker-Sentinel/internal/logging"
	"github.com/GiteaLN/Docker-Sentinel/internal/registry"
	"github.com/moby/moby/api/types/container"
)

func newTestUpdater(t *testing.T, mock *mockDocker) (*Updater, *mockClock) {
	t.Helper()
	s := testStore(t)
	q := NewQueue(s)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := &config.Config{
		DefaultPolicy: "manual",
		GracePeriod:   1 * time.Second,
	}
	return NewUpdater(mock, checker, s, q, cfg, log, clk), clk
}

func TestScanSkipsPinned(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "aaa", Names: []string{"/pinned-app"}, Image: "nginx:1.25",
			Labels: map[string]string{"sentinel.policy": "pinned"}},
	}

	u, _ := newTestUpdater(t, mock)
	result := u.Scan(context.Background())

	if result.Total != 1 {
		t.Errorf("Total = %d, want 1", result.Total)
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", result.Skipped)
	}
}

func TestScanSkipsSentinelSelf(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "aaa", Names: []string{"/sentinel"}, Image: "docker-sentinel:latest",
			Labels: map[string]string{"sentinel.self": "true"}},
	}

	u, _ := newTestUpdater(t, mock)
	result := u.Scan(context.Background())

	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (sentinel self)", result.Skipped)
	}
}

func TestScanSkipsUnresolvableImage(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "aaa", Names: []string{"/myapp"}, Image: "myapp:latest",
			Labels: map[string]string{}},
	}
	// Simulate a locally built image: local digest exists, registry check fails.
	mock.imageDigests["myapp:latest"] = "sha256:local123"
	mock.distErr["myapp:latest"] = fmt.Errorf("401 unauthorized")

	u, _ := newTestUpdater(t, mock)
	result := u.Scan(context.Background())

	// Should be skipped because distribution check fails (treated as local).
	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (unresolvable image)", result.Skipped)
	}
}

func TestScanQueuesManualUpdate(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "aaa", Names: []string{"/nginx"}, Image: "docker.io/library/nginx:1.25",
			Labels: map[string]string{"sentinel.policy": "manual"}},
	}
	mock.imageDigests["docker.io/library/nginx:1.25"] = "docker.io/library/nginx@sha256:old"
	mock.distDigests["docker.io/library/nginx:1.25"] = "sha256:new"

	u, _ := newTestUpdater(t, mock)
	result := u.Scan(context.Background())

	if result.Queued != 1 {
		t.Errorf("Queued = %d, want 1", result.Queued)
	}
	if u.queue.Len() != 1 {
		t.Errorf("queue.Len() = %d, want 1", u.queue.Len())
	}
}

func TestScanAutoUpdate(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "aaa", Names: []string{"/nginx"}, Image: "docker.io/library/nginx:1.25",
			Labels: map[string]string{"sentinel.policy": "auto"}},
	}
	mock.imageDigests["docker.io/library/nginx:1.25"] = "docker.io/library/nginx@sha256:old"
	mock.distDigests["docker.io/library/nginx:1.25"] = "sha256:new"

	// Set up inspect result for the update lifecycle.
	mock.inspectResults["aaa"] = container.InspectResponse{
		ID:   "aaa",
		Name: "/nginx",
		Config: &container.Config{
			Image:  "docker.io/library/nginx:1.25",
			Labels: map[string]string{"sentinel.policy": "auto"},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	// The new container after creation needs to pass validation.
	mock.inspectResults["new-nginx"] = container.InspectResponse{
		ID:   "new-nginx",
		Name: "/nginx",
		State: &container.State{
			Running:    true,
			Restarting: false,
		},
		Config: &container.Config{
			Image: "docker.io/library/nginx:1.25",
		},
	}

	u, _ := newTestUpdater(t, mock)
	u.cfg.DefaultPolicy = "auto"
	result := u.Scan(context.Background())

	if result.AutoCount != 1 {
		t.Errorf("AutoCount = %d, want 1", result.AutoCount)
	}
	if result.Updated != 1 {
		t.Errorf("Updated = %d, want 1", result.Updated)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}

	// Verify the lifecycle steps.
	if len(mock.pullCalls) != 1 {
		t.Errorf("pullCalls = %d, want 1", len(mock.pullCalls))
	}
	if len(mock.stopCalls) != 1 {
		t.Errorf("stopCalls = %d, want 1", len(mock.stopCalls))
	}
	if len(mock.removeCalls) != 1 {
		t.Errorf("removeCalls = %d, want 1", len(mock.removeCalls))
	}
	if len(mock.createCalls) != 1 {
		t.Errorf("createCalls = %d, want 1", len(mock.createCalls))
	}
	if len(mock.startCalls) != 1 {
		t.Errorf("startCalls = %d, want 1", len(mock.startCalls))
	}
}

func TestUpdateContainerRollbackOnValidationFailure(t *testing.T) {
	mock := newMockDocker()
	mock.inspectResults["aaa"] = container.InspectResponse{
		ID:   "aaa",
		Name: "/nginx",
		Config: &container.Config{
			Image:  "docker.io/library/nginx:1.25",
			Labels: map[string]string{},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}
	// New container fails validation (not running).
	mock.inspectResults["new-nginx"] = container.InspectResponse{
		ID:   "new-nginx",
		Name: "/nginx",
		State: &container.State{
			Running:    false,
			Restarting: true,
		},
		Config: &container.Config{Image: "docker.io/library/nginx:1.25"},
	}

	u, _ := newTestUpdater(t, mock)
	err := u.UpdateContainer(context.Background(), "aaa", "nginx")
	if err == nil {
		t.Fatal("expected error from failed validation")
	}

	// Should have attempted rollback: stop+remove new container, then create rollback.
	// removeCalls: 1 (old container) + 1 (failed new) + 0 (rollback doesn't remove)
	// createCalls: 1 (new container) + 1 (rollback container)
	if len(mock.createCalls) < 2 {
		t.Errorf("createCalls = %d, want >= 2 (new + rollback)", len(mock.createCalls))
	}
}

func TestContainerName(t *testing.T) {
	tests := []struct {
		names []string
		want  string
	}{
		{[]string{"/nginx"}, "nginx"},
		{[]string{"/my-app"}, "my-app"},
		{[]string{"no-slash"}, "no-slash"},
		{nil, "abcdef012345"},
	}

	for _, tt := range tests {
		c := container.Summary{ID: "abcdef0123456789", Names: tt.names}
		got := containerName(c)
		if got != tt.want {
			t.Errorf("containerName(%v) = %q, want %q", tt.names, got, tt.want)
		}
	}
}
