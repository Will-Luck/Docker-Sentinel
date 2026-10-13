package engine

import (
	"context"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/moby/moby/api/types/container"
)

// A successful rollback asked for from the dashboard or the API is recorded
// in the history with both images and notified, like any other rollback.
func TestManualRollbackIsRecordedAndNotified(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, &container.HostConfig{RestartPolicy: always})
	f.imageIDs["sha256:new"] = "sha256:new"
	f.imageIDs["sha256:old"] = "sha256:old"
	rollbackSetup(&container.HostConfig{RestartPolicy: always})(f, u)
	rec := recordNotifications(u)

	if err := u.RollbackContainer(context.Background(), "app"); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	history, _ := u.store.ListHistoryByContainer("app", 10)
	if len(history) != 1 || history[0].Outcome != "rollback" || history[0].OldImage != "app:latest" ||
		history[0].OldDigest != "sha256:new" || history[0].NewDigest != "sha256:old" {
		t.Errorf("history = %+v, want one rollback row from the running image to the snapshot's", history)
	}
	if n := len(rec.ofType(notify.EventRollbackOK)); n != 1 {
		t.Errorf("rollback notifications = %d, want 1", n)
	}
}
