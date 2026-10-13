package engine

import (
	"context"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/moby/moby/api/types/container"
)

// setupBrokenLatestScan builds an auto-updated nginx:latest whose new
// digest produces a container that never comes up.
func setupBrokenLatestScan(t *testing.T) (*mockDocker, *Updater) {
	t.Helper()
	mock := newMockDocker()
	ref := "docker.io/library/nginx:latest"
	mock.containers = []container.Summary{
		{ID: "aaa", Names: []string{"/nginx"}, Image: ref, Labels: map[string]string{"sentinel.policy": "auto"}},
	}
	mock.imageDigests[ref] = "docker.io/library/nginx@sha256:old"
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
	mock.distDigests["docker.io/library/nginx:latest"] = "sha256:fixed"
	u.Scan(context.Background(), ScanScheduled)
	if len(mock.pullCalls) != 2 {
		t.Errorf("pulls = %d, want the new digest tried", len(mock.pullCalls))
	}
}
