package engine

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func snapshotJSON(t *testing.T, snap container.InspectResponse) []byte {
	t.Helper()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// appSnapshot is the pre-update snapshot of "app" on app:1.0.
func appSnapshot(imageID string) container.InspectResponse {
	return container.InspectResponse{
		ID:              "old",
		Name:            "/app",
		Image:           imageID,
		Config:          &container.Config{Image: "app:1.0"},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}
}

// The running container must survive a rollback whose create fails.
func TestRollbackKeepsLiveContainerWhenCreateFails(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{{ID: "live-1.1", Names: []string{"/app"}}}
	mock.createErr["app"] = errors.New("No such image: app:1.0")

	err := runRollback(mock, "app", snapshotJSON(t, appSnapshot("")))
	if err == nil {
		t.Fatal("expected create error")
	}
	if len(mock.removeCalls) != 0 {
		t.Fatalf("live container removed before the rollback container existed: removeCalls=%v, err=%v", mock.removeCalls, err)
	}
	if opIndex(mock, "rename live-1.1 app") < 0 {
		t.Errorf("live container not given its name back: ops=%v", mock.ops)
	}
}

// With image cleanup on, the pre-update image is gone after a successful
// update. A rollback must find that out before it touches the running
// container, and leave it alone.
func TestRollbackChecksImageBeforeTouchingLiveContainer(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{{ID: "live-1.1", Names: []string{"/app"}}}
	mock.pullErr["app:1.0"] = errors.New("manifest unknown")

	err := runRollback(mock, "app", snapshotJSON(t, appSnapshot("sha256:old111")))
	if err == nil {
		t.Fatal("expected an error when the snapshot image is gone and cannot be pulled")
	}
	for _, op := range mock.ops {
		if !strings.HasPrefix(op, "pull ") {
			t.Fatalf("rollback touched containers although the image was missing: ops=%v", mock.ops)
		}
	}
}

// A missing image whose tag can be pulled back to the same image ID (a
// version tag) is fine.
func TestRollbackPullsMissingImageWithSameID(t *testing.T) {
	mock := newMockDocker()
	mock.mirrorCreate = true
	mock.pullResolves["app:1.0"] = "sha256:old111"

	if err := runRollback(mock, "app", snapshotJSON(t, appSnapshot("sha256:old111"))); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !slices.Equal(mock.pullCalls, []string{"app:1.0"}) || len(mock.createCalls) != 1 {
		t.Errorf("pullCalls = %v, createCalls = %v", mock.pullCalls, mock.createCalls)
	}
}

// A pull that brings back a different image (a mutable tag that moved on)
// must not be passed off as the old version.
func TestRollbackRefusesPulledImageWithOtherID(t *testing.T) {
	mock := newMockDocker()
	mock.pullResolves["app:1.0"] = "sha256:rebuilt"

	err := runRollback(mock, "app", snapshotJSON(t, appSnapshot("sha256:old111")))
	if err == nil {
		t.Fatal("expected an error when the pulled tag is a different image")
	}
	if len(mock.createCalls) != 0 {
		t.Errorf("createCalls = %v, want none", mock.createCalls)
	}
}

// After a :latest update the tag points at the new image. The rollback must
// recreate the old image, not whatever the tag points at now.
func TestRollbackRestoresSnapshotImageNotMovedTag(t *testing.T) {
	mock := newMockDocker()
	mock.mirrorCreate = true
	mock.imageIDs["nginx:latest"] = "sha256:new222"
	mock.imageIDs["sha256:old111"] = "sha256:old111"
	snap := container.InspectResponse{
		ID:              "aaa",
		Name:            "/nginx",
		Image:           "sha256:old111",
		Config:          &container.Config{Image: "nginx:latest"},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	if err := runRollback(mock, "nginx", snapshotJSON(t, snap)); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	tag, create := opIndex(mock, "tag sha256:old111 nginx:latest"), opIndex(mock, "create nginx")
	if tag < 0 || create < tag {
		t.Fatalf("nginx:latest not pointed back at sha256:old111 before the create; ops=%v", mock.ops)
	}
	if got := mock.inspectResults["new-nginx"].Image; got != "sha256:old111" {
		t.Errorf("rollback container runs %q, want the old image sha256:old111", got)
	}
}

// If the created container does not run the snapshot's image after all,
// the rollback fails and the running container is kept.
func TestRollbackVerifiesCreatedImage(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{{ID: "live", Names: []string{"/nginx"}}}
	mock.imageIDs["nginx:latest"] = "sha256:old111"
	mock.inspectResults["new-nginx"] = container.InspectResponse{ID: "new-nginx", Image: "sha256:new222"}
	snap := container.InspectResponse{
		ID:              "aaa",
		Name:            "/nginx",
		Image:           "sha256:old111",
		Config:          &container.Config{Image: "nginx:latest"},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}

	if err := runRollback(mock, "nginx", snapshotJSON(t, snap)); err == nil {
		t.Fatal("expected an error when the rollback container runs another image")
	}
	if slices.Contains(mock.removeCalls, "live") || opIndex(mock, "stop live") >= 0 {
		t.Errorf("live container stopped or removed: ops=%v", mock.ops)
	}
	if opIndex(mock, "remove new-nginx") < 0 || opIndex(mock, "rename live nginx") < 0 {
		t.Errorf("wrong-image container not removed or live one not restored: ops=%v", mock.ops)
	}
}

// A dashboard rollback takes the container's update lock, so it cannot
// interleave with an update of the same container.
func TestUpdaterRollbackContainerTakesUpdateLock(t *testing.T) {
	mock := newMockDocker()
	u, _ := newTestUpdater(t, mock)

	if !u.tryLock("app") {
		t.Fatal("tryLock")
	}
	if err := u.RollbackContainer(context.Background(), "app"); !errors.Is(err, ErrUpdateInProgress) {
		t.Errorf("err = %v, want ErrUpdateInProgress while an update runs", err)
	}
	u.unlock("app")

	if err := u.RollbackContainer(context.Background(), "app"); err == nil {
		t.Error("expected an error with no snapshot")
	}
	if u.IsUpdating("app") {
		t.Error("lock still held after the rollback returned")
	}
}

// After a failed :latest update the old container is restored, and the tag
// is pointed back at its image so the dashboard and the next scan see the
// image it really runs.
func TestUpdateContainerRestorePointsTagBackAtOldImage(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	mock.imageIDs["sha256:old111"] = "sha256:old111"
	failed := mock.inspectResults["new-nginx"]
	failed.State.Running = false
	mock.inspectResults["new-nginx"] = failed

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("expected an error from failed validation")
	}
	if !slices.Contains(mock.tagImageCalls, "sha256:old111->docker.io/library/nginx:latest") {
		t.Errorf("tag not pointed back at the old image: tagImageCalls=%v", mock.tagImageCalls)
	}
}
