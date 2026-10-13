package engine

import (
	"context"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// setupBrokenLatestScan builds an auto-updated nginx:latest whose new
// digest produces a container that never comes up.
func setupBrokenLatestScan(t *testing.T) (*mockDocker, *Updater) {
	t.Helper()
	mock := newMockDocker()
	ref := "localhost:1/nginx:latest"
	mock.containers = []container.Summary{
		{ID: "aaa", Names: []string{"/nginx"}, Image: ref, Labels: map[string]string{"sentinel.policy": "auto"}},
	}
	mock.imageDigests[ref] = "localhost:1/nginx@sha256:old"
	mock.distDigests[ref] = "sha256:broken"
	mock.inspectResults["aaa"] = container.InspectResponse{
		ID: "aaa", Name: "/nginx",
		Config:          &container.Config{Image: ref, Labels: map[string]string{"sentinel.policy": "auto"}},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}
	mock.inspectResults["new-nginx"] = container.InspectResponse{
		ID: "new-nginx", Name: "/nginx",
		State:  &container.State{Running: false, Restarting: true},
		Config: &container.Config{Image: ref},
	}
	u, _ := newTestUpdater(t, mock)
	return mock, u
}

// A container set aside by a swap in progress is not checked or updated by
// a scan; a container that merely has such a name, with no swap recorded,
// is treated like any other.
func TestScanSkipsContainerSetAsideBySwap(t *testing.T) {
	mock, u := setupBrokenLatestScan(t)
	ref := "localhost:1/nginx:latest"
	labels := map[string]string{"sentinel.policy": "auto"}
	mock.containers = []container.Summary{
		{ID: "aside", Names: []string{"/web-sentinel-old"}, Image: ref, Labels: labels},
		{ID: "user", Names: []string{"/docker-sentinel-old"}, Image: ref, Labels: labels},
	}
	if err := u.store.SaveSwap(store.SwapRecord{Name: "web", OldID: "aside"}); err != nil {
		t.Fatal(err)
	}

	result := u.Scan(context.Background(), ScanScheduled)

	if result.AutoCount != 1 {
		t.Errorf("AutoCount = %d, want only docker-sentinel-old considered", result.AutoCount)
	}
}

// The rolled-back marker only stops auto updates: a manual container still
// gets the update queued for the user to decide.
func TestRolledBackUpdateStillQueuedForManualContainer(t *testing.T) {
	mock, u := setupBrokenLatestScan(t)
	mock.containers[0].Labels = map[string]string{"sentinel.policy": "manual"}
	if err := u.store.SetFailedUpdate("nginx", store.FailedUpdate{Digest: "sha256:broken"}); err != nil {
		t.Fatal(err)
	}

	result := u.Scan(context.Background(), ScanScheduled)

	if result.Queued != 1 {
		t.Errorf("Queued = %d, want the update queued for the manual container", result.Queued)
	}
}

// A container that is removed takes its rolled-back marker with it.
func TestRolledBackMarkerClearedWhenContainerRemoved(t *testing.T) {
	_, u := setupBrokenLatestScan(t)
	if err := u.store.SetFailedUpdate("gone", store.FailedUpdate{Digest: "sha256:x"}); err != nil {
		t.Fatal(err)
	}
	if err := u.store.SetFailedUpdate("nginx", store.FailedUpdate{Digest: "sha256:y"}); err != nil {
		t.Fatal(err)
	}

	u.pruneFailedUpdates(context.Background())

	if _, found, _ := u.store.GetFailedUpdate("gone"); found {
		t.Error("marker of a removed container kept")
	}
	if _, found, _ := u.store.GetFailedUpdate("nginx"); !found {
		t.Error("marker of an existing container dropped")
	}
}

// The marker matches on the image digest: a fixed build pushed again under
// the same version (or tag) is tried.
func TestRolledBackMarkerMatchesOnDigest(t *testing.T) {
	mock, u := setupBrokenLatestScan(t)
	ref := "localhost:1/app:1.0"
	mock.distDigests["localhost:1/app:1.2"] = "sha256:bad"
	if err := u.store.SetFailedUpdate("app", store.FailedUpdate{Version: "1.2", Digest: "sha256:bad"}); err != nil {
		t.Fatal(err)
	}
	semver := registry.CheckResult{NewerVersions: []string{"1.2"}}
	if !u.rolledBackBefore(context.Background(), "app", ref, semver) {
		t.Error("the same 1.2 build is not recognised")
	}
	mock.distDigests["localhost:1/app:1.2"] = "sha256:fixed"
	if u.rolledBackBefore(context.Background(), "app", ref, semver) {
		t.Error("a fixed 1.2 build pushed again is still hidden")
	}
	if u.rolledBackBefore(context.Background(), "app", ref, registry.CheckResult{NewerVersions: []string{"1.3"}}) {
		t.Error("a newer version is hidden")
	}

	if err := u.store.SetFailedUpdate("web", store.FailedUpdate{Digest: "sha256:broken"}); err != nil {
		t.Fatal(err)
	}
	if !u.rolledBackBefore(context.Background(), "web", "localhost:1/web:latest", registry.CheckResult{RemoteDigest: "sha256:broken"}) {
		t.Error("the same latest digest is not recognised")
	}
	if u.rolledBackBefore(context.Background(), "web", "localhost:1/web:latest", registry.CheckResult{RemoteDigest: "sha256:fixed"}) {
		t.Error("a new latest digest is hidden")
	}
}

// With the default rollback policy a broken :latest digest was pulled,
// started, failed and rolled back on every scan, notifying each time.
// After a rollback that exact update is left alone until a new digest
// appears.
func TestScanSkipsUpdateThatWasRolledBack(t *testing.T) {
	mock, u := setupBrokenLatestScan(t)
	rec := recordNotifications(u)

	first := u.Scan(context.Background(), ScanScheduled)
	second := u.Scan(context.Background(), ScanScheduled)

	if len(mock.pullCalls) != 1 {
		t.Errorf("pulls = %d over two scans, want the broken update tried once", len(mock.pullCalls))
	}
	if first.Failed != 1 || second.Failed != 0 {
		t.Errorf("failed = %d then %d, want 1 then 0", first.Failed, second.Failed)
	}
	if n := len(rec.ofType(notify.EventRollbackOK)) + len(rec.ofType(notify.EventRollbackFailed)); n != 1 {
		t.Errorf("rollback notifications = %d, want 1", n)
	}

	// A new digest is a new update and is tried.
	mock.distDigests["localhost:1/nginx:latest"] = "sha256:fixed"
	u.Scan(context.Background(), ScanScheduled)
	if len(mock.pullCalls) != 2 {
		t.Errorf("pulls = %d, want the new digest tried", len(mock.pullCalls))
	}
}
