package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestScanListContainersError(t *testing.T) {
	mock := newMockDocker()
	mock.containersErr = fmt.Errorf("daemon unavailable")

	u, _ := newTestUpdater(t, mock)
	res := u.Scan(context.Background(), ScanScheduled)

	if res.Total != 0 {
		t.Errorf("Total = %d, want 0", res.Total)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("Errors len = %d, want 1", len(res.Errors))
	}
	if res.Errors[0].Error() != "daemon unavailable" {
		t.Errorf("Errors[0] = %q, want %q", res.Errors[0], "daemon unavailable")
	}
}

func TestScanUpToDate(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "aaa", Names: []string{"/myapp"}, Image: "fake.local/app:1.0"},
	}
	mock.imageDigests["fake.local/app:1.0"] = "fake.local/app@sha256:same111"
	mock.distDigests["fake.local/app:1.0"] = "sha256:same111"

	u, _ := newTestUpdater(t, mock)
	res := u.Scan(context.Background(), ScanScheduled)

	if res.Total != 1 {
		t.Errorf("Total = %d, want 1", res.Total)
	}
	if u.queue.Len() != 0 {
		t.Errorf("queue.Len() = %d, want 0", u.queue.Len())
	}
	if res.Queued != 0 {
		t.Errorf("Queued = %d, want 0", res.Queued)
	}
	if res.AutoCount != 0 {
		t.Errorf("AutoCount = %d, want 0", res.AutoCount)
	}
}

func TestScanMixedPolicies(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{
			ID: "pin1", Names: []string{"/pinned-svc"}, Image: "fake.local/pinned:2.0",
			Labels: map[string]string{"sentinel.policy": "pinned"},
		},
		{
			ID: "man1", Names: []string{"/manual-svc"}, Image: "fake.local/manual:3.0",
			Labels: map[string]string{"sentinel.policy": "manual"},
		},
		{
			ID: "auto1", Names: []string{"/auto-svc"}, Image: "fake.local/auto:4.0",
			Labels: map[string]string{"sentinel.policy": "auto"},
		},
	}

	// manual-svc has an update
	mock.imageDigests["fake.local/manual:3.0"] = "fake.local/manual@sha256:oldmanual"
	mock.distDigests["fake.local/manual:3.0"] = "sha256:newmanual"

	// auto-svc has an update
	mock.imageDigests["fake.local/auto:4.0"] = "fake.local/auto@sha256:oldauto"
	mock.distDigests["fake.local/auto:4.0"] = "sha256:newauto"

	// Inspect for auto-update lifecycle
	mock.inspectResults["auto1"] = container.InspectResponse{
		ID:   "auto1",
		Name: "/auto-svc",
		Config: &container.Config{
			Image:  "fake.local/auto:4.0",
			Labels: map[string]string{"sentinel.policy": "auto"},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}
	mock.inspectResults["new-auto-svc"] = container.InspectResponse{
		ID:   "new-auto-svc",
		Name: "/auto-svc",
		State: &container.State{
			Running:    true,
			Restarting: false,
		},
		Config: &container.Config{
			Image:  "fake.local/auto:4.0",
			Labels: map[string]string{"sentinel.maintenance": "true"},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	u, _ := newTestUpdater(t, mock)
	res := u.Scan(context.Background(), ScanScheduled)

	if res.Total != 3 {
		t.Errorf("Total = %d, want 3", res.Total)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (pinned)", res.Skipped)
	}
	if res.Queued != 1 {
		t.Errorf("Queued = %d, want 1 (manual)", res.Queued)
	}
	if res.AutoCount != 1 {
		t.Errorf("AutoCount = %d, want 1", res.AutoCount)
	}
	if res.Updated != 1 {
		t.Errorf("Updated = %d, want 1", res.Updated)
	}

	// Verify the manual one landed in the queue.
	if _, ok := u.queue.Get("manual-svc"); !ok {
		t.Error("manual-svc should be in the queue")
	}
}

func TestScanContextCancelled(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "aaa", Names: []string{"/app1"}, Image: "fake.local/a:1"},
		{ID: "bbb", Names: []string{"/app2"}, Image: "fake.local/b:1"},
	}
	mock.imageDigests["fake.local/a:1"] = "fake.local/a@sha256:old"
	mock.distDigests["fake.local/a:1"] = "sha256:new"
	mock.imageDigests["fake.local/b:1"] = "fake.local/b@sha256:old"
	mock.distDigests["fake.local/b:1"] = "sha256:new"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	u, _ := newTestUpdater(t, mock)
	res := u.Scan(ctx, ScanScheduled)

	// With a pre-cancelled context the loop body should bail early.
	// Total is set before the loop, so it reflects the container count.
	if res.Total != 2 {
		t.Errorf("Total = %d, want 2", res.Total)
	}
	// No containers should have been processed.
	if res.Queued+res.AutoCount+res.Updated > 0 {
		t.Errorf("expected no processing, got Queued=%d AutoCount=%d Updated=%d",
			res.Queued, res.AutoCount, res.Updated)
	}
}

func TestUpdateContainerPullFailure(t *testing.T) {
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
	mock.pullErr["nginx:latest"] = fmt.Errorf("network timeout")

	u, _ := newTestUpdater(t, mock)
	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
	if err == nil {
		t.Fatal("expected error from pull failure")
	}

	if len(mock.pullCalls) != 1 {
		t.Errorf("pullCalls = %d, want 1", len(mock.pullCalls))
	}
	// Should not have progressed to stop/remove/create.
	if len(mock.stopCalls) != 0 {
		t.Errorf("stopCalls = %d, want 0 (pull failed before stop)", len(mock.stopCalls))
	}
	if len(mock.removeCalls) != 0 {
		t.Errorf("removeCalls = %d, want 0", len(mock.removeCalls))
	}
}

func TestUpdateContainerStopFailure(t *testing.T) {
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
	// Stop fails but the code logs a warning and proceeds to remove.
	// If remove also fails, it returns an error. Set remove to succeed
	// so the flow continues through create/start/validate.
	mock.stopErr["aaa"] = fmt.Errorf("timeout")

	// New container passes validation.
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
	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")

	// Stop failure is non-fatal — update proceeds via force remove.
	if err != nil {
		t.Fatalf("unexpected error: %v (stop failure should be non-fatal)", err)
	}
	if len(mock.stopCalls) < 1 {
		t.Errorf("stopCalls = %d, want >= 1", len(mock.stopCalls))
	}
}

func TestUpdateContainerCreateFailure(t *testing.T) {
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
	mock.createErr["nginx"] = fmt.Errorf("image not found")

	u, _ := newTestUpdater(t, mock)
	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
	if err == nil {
		t.Fatal("expected error from create failure")
	}

	// Create failed means rollback was attempted (doRollback calls
	// ListAllContainers then CreateContainer for the rollback).
	// History should show a "rollback" outcome.
	history, hErr := u.store.ListHistory(10, "")
	if hErr != nil {
		t.Fatalf("ListHistory: %v", hErr)
	}
	foundRollback := false
	for _, h := range history {
		if h.Outcome == "rollback" {
			foundRollback = true
		}
	}
	if !foundRollback {
		t.Error("expected a 'rollback' outcome in history after create failure")
	}
}

func TestUpdateContainerStartFailure(t *testing.T) {
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
	mock.startErr["new-nginx"] = fmt.Errorf("OCI error")

	u, _ := newTestUpdater(t, mock)
	err := u.UpdateContainer(context.Background(), "aaa", "nginx", "")
	if err == nil {
		t.Fatal("expected error from start failure")
	}

	// Start failed → code removes the broken container then rolls back.
	// removeCalls: 1 (old container) + 1 (failed new-nginx) + possibly rollback cleanup
	if len(mock.removeCalls) < 2 {
		t.Errorf("removeCalls = %d, want >= 2 (old + failed new)", len(mock.removeCalls))
	}

	// Rollback should have been attempted (creates another container).
	// createCalls: 1 (new-nginx that failed to start) + 1 (rollback)
	if len(mock.createCalls) < 2 {
		t.Errorf("createCalls = %d, want >= 2 (new + rollback)", len(mock.createCalls))
	}
}

func TestScanDeduplicatesQueue(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{
			ID: "aaa", Names: []string{"/webapp"}, Image: "fake.local/web:5.0",
			Labels: map[string]string{"sentinel.policy": "manual"},
		},
	}
	mock.imageDigests["fake.local/web:5.0"] = "fake.local/web@sha256:oldweb"
	mock.distDigests["fake.local/web:5.0"] = "sha256:newweb"

	u, _ := newTestUpdater(t, mock)

	u.Scan(context.Background(), ScanScheduled)
	if u.queue.Len() != 1 {
		t.Fatalf("after first scan: queue.Len() = %d, want 1", u.queue.Len())
	}

	u.Scan(context.Background(), ScanScheduled)
	if u.queue.Len() != 1 {
		t.Errorf("after second scan: queue.Len() = %d, want 1 (deduplicated)", u.queue.Len())
	}
}

func TestScanRemovesStaleQueue(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{
			ID: "aaa", Names: []string{"/staleapp"}, Image: "fake.local/stale:1.0",
			Labels: map[string]string{"sentinel.policy": "manual"},
		},
	}
	// First scan: update available.
	mock.imageDigests["fake.local/stale:1.0"] = "fake.local/stale@sha256:v1"
	mock.distDigests["fake.local/stale:1.0"] = "sha256:v2"

	u, _ := newTestUpdater(t, mock)
	u.Scan(context.Background(), ScanScheduled)

	if u.queue.Len() != 1 {
		t.Fatalf("after first scan: queue.Len() = %d, want 1", u.queue.Len())
	}

	// Second scan: digests now match (user pulled manually, or upstream reverted).
	mock.imageDigests["fake.local/stale:1.0"] = "fake.local/stale@sha256:v2"
	// distDigests stays "sha256:v2" — now they match.

	u.Scan(context.Background(), ScanScheduled)

	if u.queue.Len() != 0 {
		t.Errorf("after second scan: queue.Len() = %d, want 0 (stale entry removed)", u.queue.Len())
	}
}
