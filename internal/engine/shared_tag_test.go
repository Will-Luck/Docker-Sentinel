package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// A failed :latest update restores the old container. When another
// container on the same tag already runs the new image, moving the tag
// back would make that container look out of date, so the tag stays.
func TestRestoreLeavesSharedTagAlone(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	f.imageIDs["sha256:old"] = "sha256:old"
	f.add("sib", "app-sibling", "app:latest", true, nil, nil) // runs sha256:new
	f.failStart["app-1"] = errors.New("OCI error")

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the start failure")
	}
	if got := f.imageIDs["app:latest"]; got != "sha256:new" {
		t.Errorf("app:latest = %s, want it left on the image app-sibling runs", got)
	}
}

// Without such a sibling the tag is pointed back at the restored image.
func TestRestorePointsUnsharedTagBack(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	f.imageIDs["sha256:old"] = "sha256:old"
	f.failStart["app-1"] = errors.New("OCI error")

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the start failure")
	}
	if got := f.imageIDs["app:latest"]; got != "sha256:old" {
		t.Errorf("app:latest = %s, want it pointed back at the restored image", got)
	}
}

// A rollback that would have to move a tag another container runs on
// refuses, saying why, and leaves both containers alone.
func TestRollbackRefusesToMoveSharedTag(t *testing.T) {
	f := newFakeDocker()
	f.imageIDs["app:latest"] = "sha256:new"
	f.imageIDs["sha256:old"] = "sha256:old"
	f.add("live", "app", "app:latest", true, nil, nil)
	f.add("sib", "app-sibling", "app:latest", true, nil, nil)
	snap, _ := json.Marshal(container.InspectResponse{
		ID: "old", Name: "/app", Image: "sha256:old",
		Config: &container.Config{Image: "app:latest"}, HostConfig: &container.HostConfig{},
	})

	err := runRollback(f, "app", snap)

	if err == nil || !strings.Contains(err.Error(), "app-sibling") {
		t.Fatalf("err = %v, want a refusal naming app-sibling", err)
	}
	if got := f.imageIDs["app:latest"]; got != "sha256:new" {
		t.Errorf("app:latest = %s, want it left alone", got)
	}
	if c := f.byName("app"); c == nil || c.id != "live" || !c.running {
		t.Errorf("containers = %v, want the live container untouched", f.names())
	}
}
