package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/moby/moby/api/types/container"
)

// exitingDocker makes the replacement exit right after it starts, like an
// image that crashes on start.
type exitingDocker struct {
	*mockDocker
}

func (d *exitingDocker) InspectContainer(ctx context.Context, id string) (container.InspectResponse, error) {
	resp, err := d.mockDocker.InspectContainer(ctx, id)
	if id == "new-nginx" && err == nil {
		resp.State = &container.State{Running: false, Status: container.StateExited, ExitCode: 3}
	}
	return resp, err
}

// A replacement that fails validation is reported with why (here its exit
// code), recorded as a failed update with both images, and the rollback
// row names the images too.
func TestValidationFailureSaysWhyAndRecordsTheUpdate(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	mock.imageDigests["docker.io/library/nginx:latest"] = "docker.io/library/nginx@sha256:new"
	u.docker = &exitingDocker{mockDocker: mock}
	u.clock = tickClock{u.clock.(*mockClock)} // history rows are keyed by time
	rec := recordNotifications(u)

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("expected the update to fail validation")
	}

	failed := rec.ofType(notify.EventUpdateFailed)
	if len(failed) != 1 || strings.Contains(failed[0].Error, "<nil>") || !strings.Contains(failed[0].Error, "exit code 3") {
		t.Errorf("failure notifications = %+v, want the reason (exit code 3)", failed)
	}
	history, _ := u.store.ListHistoryByContainer("nginx", 10)
	var failedRow, rollbackRow bool
	for _, h := range history {
		withImages := h.OldImage == "docker.io/library/nginx:latest" && h.NewImage == "docker.io/library/nginx:latest"
		switch h.Outcome {
		case "failed":
			failedRow = withImages && strings.Contains(h.Error, "exit code 3")
		case "rollback":
			rollbackRow = withImages
		}
	}
	if !failedRow || !rollbackRow {
		t.Errorf("history = %+v, want a failed row with the reason and a rollback row, both with the images", history)
	}
}
