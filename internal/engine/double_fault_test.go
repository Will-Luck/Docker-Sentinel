package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// The replacement fails to start and giving the old container its name back
// fails once. The restore is retried; nothing is rebuilt from the snapshot.
func TestUpdateContainerRetriesRestoreInsteadOfRebuilding(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	f.failStart["app-1"] = errors.New("OCI error")
	f.failRenameOnce["app"] = errors.New("daemon busy")

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the start failure")
	}
	if c := f.byName("app"); c == nil || c.id != "old" || !c.running {
		t.Fatalf("containers = %v, want the old container restored", f.names())
	}
	if f.creates != 1 {
		t.Errorf("creates = %d, want only the failed replacement", f.creates)
	}
}

// When the old container exists but cannot be restored, rebuilding it from
// the snapshot would leave two copies. The failure is reported instead.
func TestUpdateContainerRestoreFailureIsReportedNotRebuilt(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	f.imageIDs["sha256:old"] = "sha256:old" // a rebuild would be possible
	f.failStart["app-1"] = errors.New("OCI error")
	f.failRename["app"] = errors.New("daemon busy")

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the start failure")
	}
	if f.creates != 1 {
		t.Errorf("creates = %d (containers %v), want no rebuild while the old container exists", f.creates, f.names())
	}
	if f.find("old") == nil {
		t.Errorf("old container gone: %v", f.names())
	}
	history, _ := u.store.ListHistory(10, "")
	if len(history) == 0 || history[0].Outcome != "rollback" || !strings.Contains(history[0].Error, "rollback failed") {
		t.Errorf("history = %+v, want a rollback record saying the rollback failed", history)
	}
}

// A --rm old container is deleted by the daemon when it stops, so only the
// snapshot can bring it back.
func TestUpdateContainerRebuildsWhenOldIsGone(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, &container.HostConfig{AutoRemove: true})
	f.imageIDs["sha256:old"] = "sha256:old" // the old image is still on the host
	f.failStart["app-1"] = errors.New("OCI error")

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the start failure")
	}
	c := f.byName("app")
	if c == nil || !c.running || c.imageID != "sha256:old" {
		t.Fatalf("containers = %v, want app rebuilt on the old image", f.names())
	}
	if recs := journal(t, u); len(recs) != 0 {
		t.Errorf("journal = %+v, want empty after the rebuild", recs)
	}
}

// The same holds for the finalise swap: when the validated container exists
// but cannot be put back, the old version is not rebuilt next to it.
func TestUpdateContainerFinaliseRestoreFailureIsReportedNotRebuilt(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	f.failStart["app-2"] = errors.New("OCI error")  // the finalise replacement
	f.failRename["app"] = errors.New("daemon busy") // only the restore renames to "app"

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the finalise failure")
	}
	if f.creates != 2 {
		t.Errorf("creates = %d (containers %v), want the update and the finalise only", f.creates, f.names())
	}
	if f.find("app-1") == nil {
		t.Errorf("validated container gone: %v", f.names())
	}
}
