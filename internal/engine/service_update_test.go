package engine

import (
	"context"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/swarm"
)

func newSwarmTestUpdater(t *testing.T, mock *mockDocker) (*Updater, *mockClock) {
	t.Helper()
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	notifier := notify.NewMulti(log)
	return NewUpdater(mock, checker, s, q, cfg, log, clk, notifier, nil), clk
}

func svcSpec(name, image string, labels map[string]string) swarm.ServiceSpec {
	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: name, Labels: labels},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{Image: image},
		},
	}
}

func TestScanServicesNotSwarm(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = false
	mock.containers = []container.Summary{
		{ID: "c1", Names: []string{"/app"}, Image: "nginx:1.25", Labels: map[string]string{}},
	}
	mock.imageDigests["nginx:1.25"] = "sha256:aaa"
	mock.distDigests["nginx:1.25"] = "sha256:aaa"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Services != 0 {
		t.Errorf("Services = %d, want 0 (not in swarm mode)", result.Services)
	}
}

func TestScanServicesDetectsUpdate(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-1", Spec: svcSpec("web", "nginx:1.25", nil)},
	}
	mock.imageDigests["nginx:1.25"] = "sha256:old111"
	mock.distDigests["nginx:1.25"] = "sha256:new222"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Services != 1 {
		t.Errorf("Services = %d, want 1", result.Services)
	}
	if result.Queued != 1 {
		t.Errorf("Queued = %d, want 1 (manual policy)", result.Queued)
	}

	pending, ok := u.queue.Get("web")
	if !ok {
		t.Fatal("expected queue entry for 'web'")
	}
	if pending.Type != "service" {
		t.Errorf("Type = %q, want 'service'", pending.Type)
	}
}

func TestScanServicesSkipsPinned(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-1", Spec: svcSpec("pinned-svc", "nginx:1.25", map[string]string{"sentinel.policy": "pinned"})},
	}
	mock.imageDigests["nginx:1.25"] = "sha256:old"
	mock.distDigests["nginx:1.25"] = "sha256:new"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Queued != 0 {
		t.Errorf("Queued = %d, want 0 (pinned)", result.Queued)
	}
}

func TestScanServicesSkipsSentinel(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-self", Spec: svcSpec("sentinel-svc", "sentinel:latest", map[string]string{"sentinel.self": "true"})},
	}
	mock.imageDigests["sentinel:latest"] = "sha256:old"
	mock.distDigests["sentinel:latest"] = "sha256:new"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Queued != 0 {
		t.Errorf("Queued = %d, want 0 (sentinel self)", result.Queued)
	}
}

func TestScanServicesAutoPolicy(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{
			ID:   "svc-auto",
			Meta: swarm.Meta{Version: swarm.Version{Index: 5}},
			Spec: svcSpec("auto-svc", "nginx:1.25", map[string]string{"sentinel.policy": "auto"}),
		},
	}
	mock.imageDigests["nginx:1.25"] = "sha256:old"
	mock.distDigests["nginx:1.25"] = "sha256:new"
	mock.inspectService["svc-auto"] = swarm.Service{
		ID:           "svc-auto",
		Meta:         swarm.Meta{Version: swarm.Version{Index: 5}},
		Spec:         svcSpec("auto-svc", "nginx:1.25", map[string]string{"sentinel.policy": "auto"}),
		UpdateStatus: &swarm.UpdateStatus{State: swarm.UpdateStateCompleted},
	}

	u, _ := newSwarmTestUpdater(t, mock)
	u.cfg.SetDefaultPolicy("auto")

	result := u.Scan(context.Background(), ScanScheduled)

	if result.AutoCount != 1 {
		t.Errorf("AutoCount = %d, want 1", result.AutoCount)
	}
	if len(mock.updateSvcCalls) != 1 {
		t.Errorf("UpdateService calls = %d, want 1", len(mock.updateSvcCalls))
	}
}

func TestUpdateServiceLocking(t *testing.T) {
	mock := newMockDocker()
	mock.inspectService["svc-1"] = swarm.Service{
		ID:           "svc-1",
		Meta:         swarm.Meta{Version: swarm.Version{Index: 10}},
		Spec:         svcSpec("test-svc", "nginx:1.25", nil),
		UpdateStatus: &swarm.UpdateStatus{State: swarm.UpdateStateCompleted},
	}

	u, _ := newSwarmTestUpdater(t, mock)
	u.tryLock("test-svc")

	err := u.UpdateService(context.Background(), "svc-1", "test-svc", "nginx:1.26")
	if err != ErrUpdateInProgress {
		t.Errorf("err = %v, want ErrUpdateInProgress", err)
	}

	u.unlock("test-svc")
}

func TestScanServicesUpToDate(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-ok", Spec: svcSpec("ok-svc", "fake.local/app:1.0", nil)},
	}
	mock.imageDigests["fake.local/app:1.0"] = "sha256:same"
	mock.distDigests["fake.local/app:1.0"] = "sha256:same"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Services != 1 {
		t.Errorf("Services = %d, want 1", result.Services)
	}
	if result.Queued != 0 {
		t.Errorf("Queued = %d, want 0 (up to date)", result.Queued)
	}
}
