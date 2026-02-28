package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/swarm"
)

// --- Scan error paths ---

func TestScanServicesListError(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.servicesErr = fmt.Errorf("daemon unavailable")

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Services != 0 {
		t.Errorf("Services = %d, want 0", result.Services)
	}
	if len(result.Errors) == 0 {
		t.Error("expected error in result.Errors")
	}
}

func TestScanServicesRegistryCheckFails(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-1", Spec: svcSpec("broken-svc", "ghcr.io/owner/app:latest", nil)},
	}
	// Both local and remote lookups fail.
	mock.imageDigestErr["ghcr.io/owner/app:latest"] = fmt.Errorf("image not found locally")
	mock.distErr["ghcr.io/owner/app:latest"] = fmt.Errorf("401 unauthorized")

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Queued != 0 {
		t.Errorf("Queued = %d, want 0 (both lookups failed)", result.Queued)
	}
	// Both lookups failed: service silently skipped, not reported as error.
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors (silent skip), got %v", result.Errors)
	}
}

func TestScanServicesLocalImage(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-local", Spec: svcSpec("local-svc", "myapp:latest", nil)},
	}
	mock.imageDigests["myapp:latest"] = "sha256:local123"
	mock.distErr["myapp:latest"] = fmt.Errorf("401 unauthorized")

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Queued != 0 {
		t.Errorf("Queued = %d, want 0 (local image)", result.Queued)
	}
}

func TestScanServicesContextCancelled(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-1", Spec: svcSpec("svc-a", "fake.local/a:1.0", nil)},
		{ID: "svc-2", Spec: svcSpec("svc-b", "fake.local/b:1.0", nil)},
	}
	mock.imageDigests["fake.local/a:1.0"] = "sha256:old"
	mock.distDigests["fake.local/a:1.0"] = "sha256:new"
	mock.imageDigests["fake.local/b:1.0"] = "sha256:old"
	mock.distDigests["fake.local/b:1.0"] = "sha256:new"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(ctx, ScanScheduled)

	// With cancelled context, services are listed but processing should bail early.
	if result.Queued > 0 {
		t.Errorf("Queued = %d, want 0 (context cancelled)", result.Queued)
	}
}

// --- Mixed containers + services ---

func TestScanMixedContainersAndServices(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true

	mock.containers = []container.Summary{
		{ID: "c1", Names: []string{"/webapp"}, Image: "fake.local/web:2.0", Labels: map[string]string{}},
	}
	mock.imageDigests["fake.local/web:2.0"] = "fake.local/web@sha256:oldweb"
	mock.distDigests["fake.local/web:2.0"] = "sha256:newweb"

	mock.services = []swarm.Service{
		{ID: "svc-1", Spec: svcSpec("api-svc", "fake.local/api:3.0", nil)},
	}
	mock.imageDigests["fake.local/api:3.0"] = "sha256:oldapi"
	mock.distDigests["fake.local/api:3.0"] = "sha256:newapi"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Total != 1 {
		t.Errorf("Total containers = %d, want 1", result.Total)
	}
	if result.Services != 1 {
		t.Errorf("Services = %d, want 1", result.Services)
	}
	if result.Queued != 2 {
		t.Errorf("Queued = %d, want 2 (1 container + 1 service)", result.Queued)
	}

	// Verify types.
	cEntry, ok := u.queue.Get("webapp")
	if !ok {
		t.Fatal("expected queue entry for 'webapp'")
	}
	if cEntry.Type == "service" {
		t.Error("container queue entry should not have Type='service'")
	}

	sEntry, ok := u.queue.Get("api-svc")
	if !ok {
		t.Fatal("expected queue entry for 'api-svc'")
	}
	if sEntry.Type != "service" {
		t.Errorf("service queue Type = %q, want 'service'", sEntry.Type)
	}
}

// --- UpdateService error paths ---

func TestUpdateServiceInspectError(t *testing.T) {
	mock := newMockDocker()
	mock.inspectSvcErr["svc-1"] = fmt.Errorf("service not found")

	u, _ := newSwarmTestUpdater(t, mock)
	err := u.UpdateService(context.Background(), "svc-1", "test-svc", "nginx:1.26")

	if err == nil {
		t.Fatal("expected error from inspect failure")
	}
	if len(mock.updateSvcCalls) != 0 {
		t.Errorf("UpdateService calls = %d, want 0 (inspect failed)", len(mock.updateSvcCalls))
	}
}

func TestUpdateServiceDockerUpdateError(t *testing.T) {
	mock := newMockDocker()
	mock.inspectService["svc-1"] = swarm.Service{
		ID:   "svc-1",
		Meta: swarm.Meta{Version: swarm.Version{Index: 5}},
		Spec: svcSpec("test-svc", "nginx:1.25", nil),
	}
	mock.updateSvcErr["svc-1"] = fmt.Errorf("version conflict")

	u, _ := newSwarmTestUpdater(t, mock)
	err := u.UpdateService(context.Background(), "svc-1", "test-svc", "nginx:1.26")

	if err == nil {
		t.Fatal("expected error from docker UpdateService")
	}
}

func TestUpdateServicePollPaused(t *testing.T) {
	mock := newMockDocker()
	mock.inspectService["svc-1"] = swarm.Service{
		ID:           "svc-1",
		Meta:         swarm.Meta{Version: swarm.Version{Index: 5}},
		Spec:         svcSpec("paused-svc", "nginx:1.25", nil),
		UpdateStatus: &swarm.UpdateStatus{State: swarm.UpdateStatePaused, Message: "failure threshold reached"},
	}

	u, _ := newSwarmTestUpdater(t, mock)
	err := u.UpdateService(context.Background(), "svc-1", "paused-svc", "nginx:1.26")

	if err == nil {
		t.Fatal("expected error for paused service update")
	}

	history, _ := u.store.ListHistory(10, "")
	if len(history) == 0 {
		t.Fatal("expected history record")
	}
	if history[0].Outcome != "failed" {
		t.Errorf("outcome = %q, want 'failed'", history[0].Outcome)
	}
}

func TestUpdateServicePollRollback(t *testing.T) {
	mock := newMockDocker()
	mock.inspectService["svc-1"] = swarm.Service{
		ID:           "svc-1",
		Meta:         swarm.Meta{Version: swarm.Version{Index: 5}},
		Spec:         svcSpec("rb-svc", "nginx:1.25", nil),
		UpdateStatus: &swarm.UpdateStatus{State: swarm.UpdateStateRollbackCompleted, Message: "rolled back"},
	}

	u, _ := newSwarmTestUpdater(t, mock)
	err := u.UpdateService(context.Background(), "svc-1", "rb-svc", "nginx:1.26")

	if err == nil {
		t.Fatal("expected error for rolled-back service")
	}

	history, _ := u.store.ListHistory(10, "")
	if len(history) == 0 {
		t.Fatal("expected history record")
	}
	if history[0].Outcome != "rollback" {
		t.Errorf("outcome = %q, want 'rollback'", history[0].Outcome)
	}
}

func TestUpdateServicePollContextCancel(t *testing.T) {
	mock := newMockDocker()
	// UpdateStatus nil — poll will loop until context cancelled.
	mock.inspectService["svc-1"] = swarm.Service{
		ID:   "svc-1",
		Meta: swarm.Meta{Version: swarm.Version{Index: 5}},
		Spec: svcSpec("hang-svc", "nginx:1.25", nil),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	u, _ := newSwarmTestUpdater(t, mock)
	err := u.UpdateService(ctx, "svc-1", "hang-svc", "nginx:1.26")

	if err == nil {
		t.Fatal("expected error from context cancellation")
	}

	history, _ := u.store.ListHistory(10, "")
	if len(history) == 0 {
		t.Fatal("expected history record")
	}
	if history[0].Outcome != "failed" {
		t.Errorf("outcome = %q, want 'failed'", history[0].Outcome)
	}
}

func TestUpdateServiceSuccess(t *testing.T) {
	mock := newMockDocker()
	mock.inspectService["svc-1"] = swarm.Service{
		ID:           "svc-1",
		Meta:         swarm.Meta{Version: swarm.Version{Index: 5}},
		Spec:         svcSpec("good-svc", "nginx:1.25", nil),
		UpdateStatus: &swarm.UpdateStatus{State: swarm.UpdateStateCompleted},
	}

	u, _ := newSwarmTestUpdater(t, mock)

	// Pre-seed queue to verify it gets cleaned up on success.
	u.queue.Add(PendingUpdate{ContainerName: "good-svc", Type: "service"})

	err := u.UpdateService(context.Background(), "svc-1", "good-svc", "nginx:1.26")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.updateSvcCalls) != 1 {
		t.Errorf("UpdateService calls = %d, want 1", len(mock.updateSvcCalls))
	}

	// Queue should be cleared on success.
	if _, ok := u.queue.Get("good-svc"); ok {
		t.Error("queue entry should be removed after successful update")
	}

	history, _ := u.store.ListHistory(10, "")
	if len(history) == 0 {
		t.Fatal("expected history record")
	}
	if history[0].Outcome != "success" {
		t.Errorf("outcome = %q, want 'success'", history[0].Outcome)
	}
}

func TestUpdateServiceNoTargetImage(t *testing.T) {
	mock := newMockDocker()
	mock.inspectService["svc-1"] = swarm.Service{
		ID:           "svc-1",
		Meta:         swarm.Meta{Version: swarm.Version{Index: 3}},
		Spec:         svcSpec("digest-svc", "nginx:1.25", nil),
		UpdateStatus: &swarm.UpdateStatus{State: swarm.UpdateStateCompleted},
	}

	u, _ := newSwarmTestUpdater(t, mock)
	err := u.UpdateService(context.Background(), "svc-1", "digest-svc", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still call UpdateService (forces re-pull via QueryRegistry).
	if len(mock.updateSvcCalls) != 1 {
		t.Errorf("UpdateService calls = %d, want 1", len(mock.updateSvcCalls))
	}
}

// --- Service scan policy variations ---

func TestScanServicesDefaultPolicy(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-1", Spec: svcSpec("no-label-svc", "fake.local/app:1.0", nil)},
	}
	mock.imageDigests["fake.local/app:1.0"] = "sha256:old"
	mock.distDigests["fake.local/app:1.0"] = "sha256:new"

	u, _ := newSwarmTestUpdater(t, mock)
	u.cfg.SetDefaultPolicy("pinned")

	result := u.Scan(context.Background(), ScanScheduled)

	// No label → falls back to default policy (pinned) → skip.
	if result.Queued != 0 {
		t.Errorf("Queued = %d, want 0 (default policy is pinned)", result.Queued)
	}
}

func TestScanServicesLabelOverridesDefault(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-1", Spec: svcSpec("labeled-svc", "fake.local/app:1.0",
			map[string]string{"sentinel.policy": "manual"})},
	}
	mock.imageDigests["fake.local/app:1.0"] = "sha256:old"
	mock.distDigests["fake.local/app:1.0"] = "sha256:new"

	u, _ := newSwarmTestUpdater(t, mock)
	u.cfg.SetDefaultPolicy("pinned")

	result := u.Scan(context.Background(), ScanScheduled)

	// Label says manual → should queue despite default being pinned.
	if result.Queued != 1 {
		t.Errorf("Queued = %d, want 1 (label overrides default)", result.Queued)
	}
}

func TestScanServicesMultiple(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-1", Spec: svcSpec("pinned-svc", "fake.local/a:1.0",
			map[string]string{"sentinel.policy": "pinned"})},
		{ID: "svc-2", Spec: svcSpec("manual-svc", "fake.local/b:1.0",
			map[string]string{"sentinel.policy": "manual"})},
		{ID: "svc-3", Spec: svcSpec("uptodate-svc", "fake.local/c:1.0", nil)},
	}
	mock.imageDigests["fake.local/a:1.0"] = "sha256:old"
	mock.distDigests["fake.local/a:1.0"] = "sha256:new"
	mock.imageDigests["fake.local/b:1.0"] = "sha256:old"
	mock.distDigests["fake.local/b:1.0"] = "sha256:new"
	mock.imageDigests["fake.local/c:1.0"] = "sha256:same"
	mock.distDigests["fake.local/c:1.0"] = "sha256:same"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Services != 3 {
		t.Errorf("Services = %d, want 3", result.Services)
	}
	if result.Queued != 1 {
		t.Errorf("Queued = %d, want 1 (only manual-svc)", result.Queued)
	}
}

// --- Queue dedup for services ---

func TestScanServicesDeduplicatesQueue(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-1", Spec: svcSpec("dup-svc", "fake.local/dup:1.0", nil)},
	}
	mock.imageDigests["fake.local/dup:1.0"] = "sha256:old"
	mock.distDigests["fake.local/dup:1.0"] = "sha256:new"

	u, _ := newSwarmTestUpdater(t, mock)
	u.Scan(context.Background(), ScanScheduled)
	u.Scan(context.Background(), ScanScheduled)

	if u.queue.Len() != 1 {
		t.Errorf("queue.Len() = %d, want 1 (deduped)", u.queue.Len())
	}
}

func TestScanServicesRemovesStaleQueue(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-1", Spec: svcSpec("stale-svc", "fake.local/stale:1.0", nil)},
	}
	mock.imageDigests["fake.local/stale:1.0"] = "sha256:old"
	mock.distDigests["fake.local/stale:1.0"] = "sha256:new"

	u, _ := newSwarmTestUpdater(t, mock)
	u.Scan(context.Background(), ScanScheduled)

	if u.queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 after first scan", u.queue.Len())
	}

	// Now digests match — stale entry should be removed.
	mock.distDigests["fake.local/stale:1.0"] = "sha256:old"
	u.Scan(context.Background(), ScanScheduled)

	if u.queue.Len() != 0 {
		t.Errorf("queue.Len() = %d, want 0 (stale entry removed)", u.queue.Len())
	}
}

// Swarm auto-pins digests on service images (nginx:1.27@sha256:abc...).
// Sentinel must strip the digest before checking, or it thinks the image is pinned.
func TestScanServicesStripsSwarmDigest(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	// Image ref with Swarm-appended digest — should NOT be treated as pinned.
	mock.services = []swarm.Service{
		{ID: "svc-digest", Spec: svcSpec("web", "fake.local/web:2.0@sha256:olddigest", nil)},
	}
	// Checker should see "fake.local/web:2.0" (stripped), not the full @sha256: ref.
	mock.imageDigests["fake.local/web:2.0"] = "sha256:olddigest"
	mock.distDigests["fake.local/web:2.0"] = "sha256:newdigest"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Queued == 0 {
		t.Error("expected update to be queued — digest stripping should allow registry check")
	}
	if u.queue.Len() != 1 {
		t.Errorf("queue.Len() = %d, want 1", u.queue.Len())
	}
}

// Service image WITHOUT @sha256: should still work normally.
func TestScanServicesNoDigestSuffix(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-plain", Spec: svcSpec("api", "fake.local/api:3.1", nil)},
	}
	mock.imageDigests["fake.local/api:3.1"] = "sha256:aaa"
	mock.distDigests["fake.local/api:3.1"] = "sha256:bbb"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if result.Queued != 1 {
		t.Errorf("Queued = %d, want 1", result.Queued)
	}
}

// --- Multi-node swarm: local ImageDigest fails ---

// Service on multi-node swarm where image only exists on worker nodes.
// Local ImageDigest fails but DistributionDigest works - should not error.
func TestScanServicesNoLocalDigestFallsBack(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-remote", Spec: svcSpec("remote-svc", "fake.local/web:1.0", nil)},
	}
	mock.imageDigestErr["fake.local/web:1.0"] = fmt.Errorf("No such image: fake.local/web:1.0")
	mock.distDigests["fake.local/web:1.0"] = "sha256:remote999"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if len(result.Errors) > 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
	}
}

// When local inspect fails and registry returns same digest for both
// the fallback and remote check, no update should be detected.
func TestScanServicesNoLocalDigestSameRegistryDigest(t *testing.T) {
	mock := newMockDocker()
	mock.swarmManager = true
	mock.services = []swarm.Service{
		{ID: "svc-fb", Spec: svcSpec("fb-svc", "fake.local/fb:2.0", nil)},
	}
	mock.imageDigestErr["fake.local/fb:2.0"] = fmt.Errorf("No such image: fake.local/fb:2.0")
	mock.distDigests["fake.local/fb:2.0"] = "sha256:same999"

	u, _ := newSwarmTestUpdater(t, mock)
	result := u.Scan(context.Background(), ScanScheduled)

	if len(result.Errors) > 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
	}
	if result.Queued != 0 {
		t.Errorf("Queued = %d, want 0 (no digest change)", result.Queued)
	}
}
