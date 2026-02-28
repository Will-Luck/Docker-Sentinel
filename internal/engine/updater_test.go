package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/moby/moby/api/types/container"
)

func newTestUpdater(t *testing.T, mock *mockDocker) (*Updater, *mockClock) {
	t.Helper()
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	cfg.SetGracePeriod(1 * time.Second)
	notifier := notify.NewMulti(log)
	return NewUpdater(mock, checker, s, q, cfg, log, clk, notifier, nil), clk
}

func TestScanSkipsPinned(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "aaa", Names: []string{"/pinned-app"}, Image: "nginx:1.25",
			Labels: map[string]string{"sentinel.policy": "pinned"}},
	}

	u, _ := newTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Total != 1 {
		t.Errorf("Total = %d, want 1", result.Total)
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", result.Skipped)
	}
}

func TestScanChecksSentinelButNeverAutoUpdates(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "aaa", Names: []string{"/sentinel"}, Image: "ghcr.io/will-luck/docker-sentinel:2.3.2",
			Labels: map[string]string{"sentinel.self": "true"}},
	}
	// Simulate an update being available.
	mock.distDigests["ghcr.io/will-luck/docker-sentinel:2.3.2"] = "sha256:remote999"
	mock.imageDigests["ghcr.io/will-luck/docker-sentinel:2.3.2"] = "sha256:local123"

	u, _ := newTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	// Sentinel should be queued, not auto-updated.
	if result.Queued != 1 {
		t.Errorf("Queued = %d, want 1 (sentinel queued for manual action)", result.Queued)
	}
	if result.Updated != 0 {
		t.Errorf("Updated = %d, want 0 (sentinel must never be auto-updated)", result.Updated)
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
	result := u.Scan(context.Background(), ScanScheduled)

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
	result := u.Scan(context.Background(), ScanScheduled)

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
	// Include the maintenance label to exercise the finaliseContainer path.
	mock.inspectResults["new-nginx"] = container.InspectResponse{
		ID:   "new-nginx",
		Name: "/nginx",
		State: &container.State{
			Running:    true,
			Restarting: false,
		},
		Config: &container.Config{
			Image:  "docker.io/library/nginx:1.25",
			Labels: map[string]string{"sentinel.maintenance": "true"},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	u, _ := newTestUpdater(t, mock)
	u.cfg.SetDefaultPolicy("auto")
	result := u.Scan(context.Background(), ScanScheduled)

	if result.AutoCount != 1 {
		t.Errorf("AutoCount = %d, want 1", result.AutoCount)
	}
	if result.Updated != 1 {
		t.Errorf("Updated = %d, want 1", result.Updated)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}

	// Verify the lifecycle steps including finalise.
	// pull(1) + stop(old:1, finalise:1) + remove(old:1, finalise:1)
	// + create(new:1, finalise:1) + start(new:1, finalise:1)
	if len(mock.pullCalls) != 1 {
		t.Errorf("pullCalls = %d, want 1", len(mock.pullCalls))
	}
	if len(mock.stopCalls) != 2 {
		t.Errorf("stopCalls = %d, want 2 (old + finalise)", len(mock.stopCalls))
	}
	if len(mock.removeCalls) != 2 {
		t.Errorf("removeCalls = %d, want 2 (old + finalise)", len(mock.removeCalls))
	}
	if len(mock.createCalls) != 2 {
		t.Errorf("createCalls = %d, want 2 (new + finalise)", len(mock.createCalls))
	}
	if len(mock.startCalls) != 2 {
		t.Errorf("startCalls = %d, want 2 (new + finalise)", len(mock.startCalls))
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
	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
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

func TestFinaliseContainerRemovesMaintenanceLabel(t *testing.T) {
	mock := newMockDocker()

	// Container with the maintenance label set.
	mock.inspectResults["new-abc"] = container.InspectResponse{
		ID:   "new-abc",
		Name: "/myapp",
		State: &container.State{
			Running:    true,
			Restarting: false,
		},
		Config: &container.Config{
			Image: "myapp:latest",
			Labels: map[string]string{
				"sentinel.maintenance": "true",
				"sentinel.policy":      "auto",
			},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	u, _ := newTestUpdater(t, mock)
	newID, err := u.finaliseContainer(context.Background(), "new-abc", "myapp")
	if err != nil {
		t.Fatalf("finaliseContainer: %v", err)
	}

	// The mock returns "new-" + name for CreateContainer.
	if newID != "new-myapp" {
		t.Errorf("newID = %q, want new-myapp", newID)
	}

	// Should have stopped, removed, created, and started.
	if len(mock.stopCalls) != 1 || mock.stopCalls[0] != "new-abc" {
		t.Errorf("stopCalls = %v, want [new-abc]", mock.stopCalls)
	}
	if len(mock.removeCalls) != 1 || mock.removeCalls[0] != "new-abc" {
		t.Errorf("removeCalls = %v, want [new-abc]", mock.removeCalls)
	}
	if len(mock.createCalls) != 1 || mock.createCalls[0] != "myapp" {
		t.Errorf("createCalls = %v, want [myapp]", mock.createCalls)
	}
	if len(mock.startCalls) != 1 || mock.startCalls[0] != "new-myapp" {
		t.Errorf("startCalls = %v, want [new-myapp]", mock.startCalls)
	}
}

func TestFinaliseContainerSkipsWhenNoLabel(t *testing.T) {
	mock := newMockDocker()

	// Container WITHOUT the maintenance label.
	mock.inspectResults["new-abc"] = container.InspectResponse{
		ID:   "new-abc",
		Name: "/myapp",
		Config: &container.Config{
			Image:  "myapp:latest",
			Labels: map[string]string{"sentinel.policy": "auto"},
		},
	}

	u, _ := newTestUpdater(t, mock)
	newID, err := u.finaliseContainer(context.Background(), "new-abc", "myapp")
	if err != nil {
		t.Fatalf("finaliseContainer: %v", err)
	}

	// Should return original ID without making any docker calls.
	if newID != "new-abc" {
		t.Errorf("newID = %q, want new-abc (unchanged)", newID)
	}
	if len(mock.stopCalls) != 0 {
		t.Errorf("stopCalls = %d, want 0 (no finalise needed)", len(mock.stopCalls))
	}
	if len(mock.createCalls) != 0 {
		t.Errorf("createCalls = %d, want 0 (no finalise needed)", len(mock.createCalls))
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

func TestTryLockPreventsDoubleUpdate(t *testing.T) {
	mock := newMockDocker()
	u, _ := newTestUpdater(t, mock)

	// First lock should succeed.
	if !u.tryLock("nginx") {
		t.Fatal("first tryLock should succeed")
	}

	// Second lock on the same container should fail.
	if u.tryLock("nginx") {
		t.Fatal("second tryLock on same container should fail")
	}

	// Different container should succeed.
	if !u.tryLock("redis") {
		t.Fatal("tryLock on different container should succeed")
	}

	// After unlock, the container should be lockable again.
	u.unlock("nginx")
	if !u.tryLock("nginx") {
		t.Fatal("tryLock should succeed after unlock")
	}

	// Clean up.
	u.unlock("nginx")
	u.unlock("redis")
}

func TestIsUpdating(t *testing.T) {
	mock := newMockDocker()
	u, _ := newTestUpdater(t, mock)

	// Not locked -- should report false.
	if u.IsUpdating("nginx") {
		t.Fatal("IsUpdating should be false for unlocked container")
	}

	// Lock it.
	if !u.tryLock("nginx") {
		t.Fatal("tryLock should succeed")
	}

	// Now should report true.
	if !u.IsUpdating("nginx") {
		t.Fatal("IsUpdating should be true for locked container")
	}

	// Different container still false.
	if u.IsUpdating("redis") {
		t.Fatal("IsUpdating should be false for different container")
	}

	// Unlock and verify.
	u.unlock("nginx")
	if u.IsUpdating("nginx") {
		t.Fatal("IsUpdating should be false after unlock")
	}
}

func TestUpdateContainerReturnsErrUpdateInProgress(t *testing.T) {
	mock := newMockDocker()

	mock.inspectResults["aaa"] = container.InspectResponse{
		ID:   "aaa",
		Name: "/nginx",
		Config: &container.Config{
			Image:  "nginx:latest",
			Labels: map[string]string{},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	u, _ := newTestUpdater(t, mock)

	// Manually acquire the lock to simulate an in-progress update.
	if !u.tryLock("nginx") {
		t.Fatal("tryLock should succeed")
	}

	// Second call should return ErrUpdateInProgress.
	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
	if err != ErrUpdateInProgress {
		t.Fatalf("expected ErrUpdateInProgress, got: %v", err)
	}

	// Clean up.
	u.unlock("nginx")
}

// --- Stage-aware finalise error handling tests ---

func TestFinaliseErrorType(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	fErr := &finaliseError{stage: "create", err: inner}

	if fErr.Error() != "finalise create: connection refused" {
		t.Errorf("Error() = %q, want %q", fErr.Error(), "finalise create: connection refused")
	}
	if !errors.Is(fErr, inner) {
		t.Error("Unwrap should return the inner error")
	}
}

func TestFinaliseStageIsDestructive(t *testing.T) {
	tests := []struct {
		stage       string
		destructive bool
	}{
		{"inspect", false},
		{"stop", false},
		{"remove", true},
		{"create", true},
		{"start", true},
	}
	for _, tt := range tests {
		if got := finaliseStageIsDestructive(tt.stage); got != tt.destructive {
			t.Errorf("finaliseStageIsDestructive(%q) = %v, want %v", tt.stage, got, tt.destructive)
		}
	}
}

// TestFinaliseContainerReturnsStageErrors verifies that each error return in
// finaliseContainer is wrapped with the correct stage.
func TestFinaliseContainerReturnsStageErrors(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*mockDocker)
		wantStage string
	}{
		{
			name: "inspect failure",
			setup: func(m *mockDocker) {
				m.inspectErr["cid"] = fmt.Errorf("not found")
			},
			wantStage: "inspect",
		},
		{
			name: "stop failure",
			setup: func(m *mockDocker) {
				m.inspectResults["cid"] = container.InspectResponse{
					ID: "cid",
					Config: &container.Config{
						Image:  "img:latest",
						Labels: map[string]string{"sentinel.maintenance": "true"},
					},
					HostConfig:      &container.HostConfig{},
					NetworkSettings: &container.NetworkSettings{},
				}
				m.stopErr["cid"] = fmt.Errorf("timeout")
			},
			wantStage: "stop",
		},
		{
			name: "remove failure",
			setup: func(m *mockDocker) {
				m.inspectResults["cid"] = container.InspectResponse{
					ID: "cid",
					Config: &container.Config{
						Image:  "img:latest",
						Labels: map[string]string{"sentinel.maintenance": "true"},
					},
					HostConfig:      &container.HostConfig{},
					NetworkSettings: &container.NetworkSettings{},
				}
				m.removeErr["cid"] = fmt.Errorf("device busy")
			},
			wantStage: "remove",
		},
		{
			name: "create failure",
			setup: func(m *mockDocker) {
				m.inspectResults["cid"] = container.InspectResponse{
					ID: "cid",
					Config: &container.Config{
						Image:  "img:latest",
						Labels: map[string]string{"sentinel.maintenance": "true"},
					},
					HostConfig:      &container.HostConfig{},
					NetworkSettings: &container.NetworkSettings{},
				}
				m.createErr["myapp"] = fmt.Errorf("image not found")
			},
			wantStage: "create",
		},
		{
			name: "start failure",
			setup: func(m *mockDocker) {
				m.inspectResults["cid"] = container.InspectResponse{
					ID: "cid",
					Config: &container.Config{
						Image:  "img:latest",
						Labels: map[string]string{"sentinel.maintenance": "true"},
					},
					HostConfig:      &container.HostConfig{},
					NetworkSettings: &container.NetworkSettings{},
				}
				m.startErr["new-myapp"] = fmt.Errorf("OCI error")
			},
			wantStage: "start",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockDocker()
			tt.setup(mock)

			u, _ := newTestUpdater(t, mock)
			_, err := u.finaliseContainer(context.Background(), "cid", "myapp")
			if err == nil {
				t.Fatal("expected error")
			}

			var fErr *finaliseError
			if !errors.As(err, &fErr) {
				t.Fatalf("expected *finaliseError, got %T: %v", err, err)
			}
			if fErr.stage != tt.wantStage {
				t.Errorf("stage = %q, want %q", fErr.stage, tt.wantStage)
			}
		})
	}
}

// setupUpdateMock sets up a mockDocker with inspect results for a standard
// update lifecycle that reaches the finalise stage. The container "aaa" is the
// original; "new-nginx" is the newly created one (with maintenance label) that
// passes validation. Returns the mock and a configured Updater.
func setupUpdateMock(t *testing.T) (*mockDocker, *Updater) {
	t.Helper()
	mock := newMockDocker()

	// Original container.
	mock.inspectResults["aaa"] = container.InspectResponse{
		ID:   "aaa",
		Name: "/nginx",
		Config: &container.Config{
			Image:  "nginx:latest",
			Labels: map[string]string{},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	// New container after step 5 create -- has maintenance label, passes validation.
	mock.inspectResults["new-nginx"] = container.InspectResponse{
		ID:   "new-nginx",
		Name: "/nginx",
		State: &container.State{
			Running:    true,
			Restarting: false,
		},
		Config: &container.Config{
			Image:  "nginx:latest",
			Labels: map[string]string{"sentinel.maintenance": "true"},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	u, _ := newTestUpdater(t, mock)
	return mock, u
}

// TestUpdateFinaliseStopFailureRecordsWarning tests the non-destructive path:
// finalise stop fails, so the container is still running (with maintenance label).
// Should record "finalise_warning" and NOT attempt rollback.
func TestUpdateFinaliseStopFailureRecordsWarning(t *testing.T) {
	mock, u := setupUpdateMock(t)

	// Make finalise stop fail (non-destructive stage).
	// Step 4 stops "aaa" (different ID), so stopErr only affects finalise.
	mock.stopErr["new-nginx"] = fmt.Errorf("stop timeout")

	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
	if err == nil {
		t.Fatal("expected error from finalise stop failure")
	}

	// Should be a finaliseError with stage "stop".
	var fErr *finaliseError
	if !errors.As(err, &fErr) {
		t.Fatalf("expected *finaliseError, got %T: %v", err, err)
	}
	if fErr.stage != "stop" {
		t.Errorf("stage = %q, want %q", fErr.stage, "stop")
	}

	// Should NOT have attempted rollback.
	// createCalls: 1 (step 5 new container) + 0 (no rollback) = 1
	if len(mock.createCalls) != 1 {
		t.Errorf("createCalls = %d, want 1 (no rollback on non-destructive finalise failure)", len(mock.createCalls))
	}

	// Verify update was recorded as "finalise_warning" (not "success").
	history, hErr := u.store.ListHistory(10, "")
	if hErr != nil {
		t.Fatalf("ListHistory: %v", hErr)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	if history[0].Outcome != "finalise_warning" {
		t.Errorf("outcome = %q, want %q", history[0].Outcome, "finalise_warning")
	}
	if history[0].Error == "" {
		t.Error("error field should be non-empty for finalise_warning")
	}
}

// TestUpdateFinaliseRemoveFailureTriggersRollback tests the destructive path:
// finalise remove fails after the container was stopped, so it is likely down.
// Should trigger rollback and record "failed".
func TestUpdateFinaliseRemoveFailureTriggersRollback(t *testing.T) {
	mock, u := setupUpdateMock(t)

	// Make finalise remove fail. Step 4 removes "aaa" (different ID), so
	// removeErr["new-nginx"] only affects finalise.
	mock.removeErr["new-nginx"] = fmt.Errorf("device busy")

	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
	if err == nil {
		t.Fatal("expected error from finalise remove failure")
	}

	var fErr *finaliseError
	if !errors.As(err, &fErr) {
		t.Fatalf("expected *finaliseError, got %T: %v", err, err)
	}
	if fErr.stage != "remove" {
		t.Errorf("stage = %q, want %q", fErr.stage, "remove")
	}

	// Destructive stage -- should have attempted rollback.
	// createCalls: 1 (step 5 new) + 1 (rollback from snapshot) = 2
	if len(mock.createCalls) < 2 {
		t.Errorf("createCalls = %d, want >= 2 (step-5 + rollback)", len(mock.createCalls))
	}

	// History should record "failed", never "success".
	// doRollback also records a "rollback" entry, so we may have 2 records.
	history, hErr := u.store.ListHistory(10, "")
	if hErr != nil {
		t.Fatalf("ListHistory: %v", hErr)
	}
	foundFailed := false
	for _, h := range history {
		if h.Outcome == "success" {
			t.Error("should never record 'success' when finalise failed at destructive stage")
		}
		if h.Outcome == "failed" {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Error("expected a 'failed' outcome in history")
	}
}

// TestUpdateFinaliseInspectFailureRecordsWarning tests that an inspect failure
// during finalise (non-destructive) does not trigger rollback.
func TestUpdateFinaliseInspectFailureRecordsWarning(t *testing.T) {
	// Test the non-destructive path by calling finaliseContainer directly
	// with an inspect error (the end-to-end mock cannot differentiate between
	// validation inspect and finalise inspect on the same container ID).
	mock2 := newMockDocker()
	mock2.inspectErr["cid"] = fmt.Errorf("container not found")

	u2, _ := newTestUpdater(t, mock2)
	_, err := u2.finaliseContainer(context.Background(), "cid", "myapp")
	if err == nil {
		t.Fatal("expected error")
	}

	var fErr *finaliseError
	if !errors.As(err, &fErr) {
		t.Fatalf("expected *finaliseError, got %T: %v", err, err)
	}
	if fErr.stage != "inspect" {
		t.Errorf("stage = %q, want %q", fErr.stage, "inspect")
	}

	// Verify non-destructive classification.
	if finaliseStageIsDestructive(fErr.stage) {
		t.Error("inspect should not be classified as destructive")
	}
}

// --- Shared network namespace tests ---

func TestRepairNetworkNamespace_ConsumerBrokenSandbox(t *testing.T) {
	mock := newMockDocker()

	// The updated container is a consumer of "wireguard-pia"'s namespace,
	// but after finalise it has an empty SandboxKey (broken namespace).
	mock.inspectResults["new-flare"] = container.InspectResponse{
		ID:   "new-flare",
		Name: "/flaresolverr",
		State: &container.State{
			Running: true,
		},
		Config: &container.Config{
			Image: "flaresolverr:latest",
		},
		HostConfig: &container.HostConfig{
			NetworkMode: "container:wireguard-pia",
		},
		NetworkSettings: &container.NetworkSettings{
			SandboxKey: "", // broken — empty namespace
		},
	}

	// ListContainers returns no other containers (no dependents to check).
	mock.containers = []container.Summary{}

	u, _ := newTestUpdater(t, mock)
	u.repairNetworkNamespace(context.Background(), "new-flare", "flaresolverr")

	// Should have restarted the consumer to rejoin the namespace.
	if len(mock.restartCalls) != 1 {
		t.Fatalf("restartCalls = %d, want 1", len(mock.restartCalls))
	}
	if mock.restartCalls[0] != "new-flare" {
		t.Errorf("restartCalls[0] = %q, want %q", mock.restartCalls[0], "new-flare")
	}
}

func TestRepairNetworkNamespace_ConsumerHealthySandbox(t *testing.T) {
	mock := newMockDocker()

	// Consumer with a valid SandboxKey — no restart needed.
	mock.inspectResults["new-flare"] = container.InspectResponse{
		ID:   "new-flare",
		Name: "/flaresolverr",
		Config: &container.Config{
			Image: "flaresolverr:latest",
		},
		HostConfig: &container.HostConfig{
			NetworkMode: "container:wireguard-pia",
		},
		NetworkSettings: &container.NetworkSettings{
			SandboxKey: "/var/run/docker/netns/abc123",
		},
	}

	mock.containers = []container.Summary{}

	u, _ := newTestUpdater(t, mock)
	u.repairNetworkNamespace(context.Background(), "new-flare", "flaresolverr")

	// Should NOT restart — namespace is healthy.
	if len(mock.restartCalls) != 0 {
		t.Errorf("restartCalls = %d, want 0 (sandbox is healthy)", len(mock.restartCalls))
	}
}

func TestRepairNetworkNamespace_ProviderRestartsDependents(t *testing.T) {
	mock := newMockDocker()

	// The updated container is a provider (wireguard-pia) — normal network mode.
	mock.inspectResults["new-vpn"] = container.InspectResponse{
		ID:   "new-vpn",
		Name: "/wireguard-pia",
		Config: &container.Config{
			Image: "wireguard-pia:latest",
		},
		HostConfig: &container.HostConfig{
			NetworkMode: "bridge",
		},
		NetworkSettings: &container.NetworkSettings{
			SandboxKey: "/var/run/docker/netns/vpn123",
		},
	}

	// Two dependents share this provider's namespace.
	mock.containers = []container.Summary{
		{ID: "vpn-id", Names: []string{"/wireguard-pia"}},
		{ID: "flare-id", Names: []string{"/flaresolverr"}},
		{ID: "prowlarr-id", Names: []string{"/prowlarr"}},
		{ID: "unrelated-id", Names: []string{"/nginx"}},
	}

	mock.inspectResults["flare-id"] = container.InspectResponse{
		ID:   "flare-id",
		Name: "/flaresolverr",
		HostConfig: &container.HostConfig{
			NetworkMode: "container:wireguard-pia",
		},
	}
	mock.inspectResults["prowlarr-id"] = container.InspectResponse{
		ID:   "prowlarr-id",
		Name: "/prowlarr",
		HostConfig: &container.HostConfig{
			NetworkMode: "container:wireguard-pia",
		},
	}
	mock.inspectResults["unrelated-id"] = container.InspectResponse{
		ID:   "unrelated-id",
		Name: "/nginx",
		HostConfig: &container.HostConfig{
			NetworkMode: "bridge",
		},
	}

	u, _ := newTestUpdater(t, mock)
	u.repairNetworkNamespace(context.Background(), "new-vpn", "wireguard-pia")

	// Should restart both dependents but not nginx or self.
	if len(mock.restartCalls) != 2 {
		t.Fatalf("restartCalls = %d, want 2", len(mock.restartCalls))
	}

	restarted := make(map[string]bool)
	for _, id := range mock.restartCalls {
		restarted[id] = true
	}
	if !restarted["flare-id"] {
		t.Error("expected flaresolverr to be restarted")
	}
	if !restarted["prowlarr-id"] {
		t.Error("expected prowlarr to be restarted")
	}
}

func TestRepairNetworkNamespace_NoDependents(t *testing.T) {
	mock := newMockDocker()

	// Provider with no dependents — no restarts expected.
	mock.inspectResults["new-app"] = container.InspectResponse{
		ID:   "new-app",
		Name: "/myapp",
		Config: &container.Config{
			Image: "myapp:latest",
		},
		HostConfig: &container.HostConfig{
			NetworkMode: "bridge",
		},
		NetworkSettings: &container.NetworkSettings{
			SandboxKey: "/var/run/docker/netns/abc",
		},
	}

	mock.containers = []container.Summary{
		{ID: "app-id", Names: []string{"/myapp"}},
		{ID: "other-id", Names: []string{"/other"}},
	}
	mock.inspectResults["other-id"] = container.InspectResponse{
		ID:   "other-id",
		Name: "/other",
		HostConfig: &container.HostConfig{
			NetworkMode: "bridge",
		},
	}

	u, _ := newTestUpdater(t, mock)
	u.repairNetworkNamespace(context.Background(), "new-app", "myapp")

	if len(mock.restartCalls) != 0 {
		t.Errorf("restartCalls = %d, want 0 (no dependents)", len(mock.restartCalls))
	}
}
