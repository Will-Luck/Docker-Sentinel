package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
)

// setupSemverAttempt builds "nginx" running nginx:1.1 after an earlier
// successful update from 1.0, whose snapshot S(1.0) is in the store.
func setupSemverAttempt(t *testing.T) (*mockDocker, *Updater) {
	t.Helper()
	mock := newMockDocker()
	mock.inspectResults["aaa"] = container.InspectResponse{
		ID:              "aaa",
		Name:            "/nginx",
		Image:           "sha256:v11",
		Config:          &container.Config{Image: "nginx:1.1", Labels: map[string]string{}},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}
	mock.imageIDs["nginx:1.2"] = "sha256:v12"
	u, _ := newTestUpdater(t, mock)

	prev, _ := json.Marshal(container.InspectResponse{Name: "/nginx", Config: &container.Config{Image: "nginx:1.0"}})
	if err := u.store.SaveSnapshot("nginx", prev); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond) // snapshot keys are timestamps
	return mock, u
}

func latestSnapshotImage(t *testing.T, u *Updater) string {
	t.Helper()
	data, err := u.store.GetLatestSnapshot("nginx")
	if err != nil || data == nil {
		t.Fatalf("GetLatestSnapshot: %v (data %v)", err, data)
	}
	var got container.InspectResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got.Config.Image
}

// An attempt that never replaces the container must not replace the
// snapshot a dashboard rollback restores, or that rollback recreates the
// version already running.
func TestUpdateContainerFailedPullKeepsRollbackSnapshot(t *testing.T) {
	mock, u := setupSemverAttempt(t)
	mock.pullErr["nginx:1.2"] = errors.New("registry unavailable")

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", "nginx:1.2"); err == nil {
		t.Fatal("expected pull failure")
	}
	if got := latestSnapshotImage(t, u); got != "nginx:1.0" {
		t.Fatalf("latest snapshot image = %q after a failed pull; want nginx:1.0", got)
	}
}

// The same goes for an attempt whose replacement failed and was undone.
func TestUpdateContainerRestoredAttemptKeepsRollbackSnapshot(t *testing.T) {
	mock, u := setupSemverAttempt(t)
	mock.startErr["new-nginx"] = errors.New("OCI error")

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", "nginx:1.2"); err == nil {
		t.Fatal("expected start failure")
	}
	if got := latestSnapshotImage(t, u); got != "nginx:1.0" {
		t.Fatalf("latest snapshot image = %q after a restored attempt; want nginx:1.0", got)
	}
}

// A successful update keeps one snapshot: the version it replaced.
func TestUpdateContainerSuccessSnapshotsReplacedVersion(t *testing.T) {
	mock, u := setupSemverAttempt(t)
	mock.mirrorCreate = true

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", "nginx:1.2"); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	if got := latestSnapshotImage(t, u); got != "nginx:1.1" {
		t.Fatalf("latest snapshot image = %q after updating 1.1 to 1.2; want nginx:1.1", got)
	}
	if entries, err := u.store.ListSnapshots("nginx"); err != nil || len(entries) != 1 {
		t.Errorf("snapshots = %d (err %v), want 1", len(entries), err)
	}
}
