package engine

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// opIndex returns the position of op in the mock's call log, or -1.
func opIndex(mock *mockDocker, op string) int {
	return slices.Index(mock.ops, op)
}

// The old container must stay intact until its replacement has started:
// a create the daemon rejects must leave the service running, not deleted.
func TestUpdateContainerKeepsOldContainerWhenCreateFails(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	mock.createErr["nginx"] = errors.New("conflicting options: hostname and the network mode")

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("expected an error from the failed create")
	}
	for _, op := range mock.ops {
		if op == "remove aaa" || op == "remove-with-volumes aaa" {
			t.Fatalf("old container removed although its replacement was never created: ops=%v", mock.ops)
		}
	}
	if opIndex(mock, "rename aaa nginx") < 0 {
		t.Errorf("old container not given its name back: ops=%v", mock.ops)
	}
	if opIndex(mock, "stop aaa") >= 0 && opIndex(mock, "start aaa") < 0 {
		t.Errorf("old container stopped and not started again: ops=%v", mock.ops)
	}
}

// The replacement is created while the old container still runs, the old
// one is stopped only then, and it is removed only after validation.
func TestUpdateContainerReplacesBeforeRemoving(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	order := []string{
		"rename aaa nginx-sentinel-old",
		"create nginx",
		"stop aaa",
		"start new-nginx",
		"remove aaa",
	}
	last := -1
	for _, op := range order {
		i := opIndex(mock, op)
		if i < 0 || i < last {
			t.Fatalf("want %v in this order, got ops=%v", order, mock.ops)
		}
		last = i
	}
}

// A replacement that fails validation is removed and the old container
// itself comes back, with its own image and volumes, rather than a copy
// rebuilt from the snapshot.
func TestUpdateContainerRestoresOldContainerWhenValidationFails(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	failed := mock.inspectResults["new-nginx"]
	failed.State.Running = false
	failed.State.Restarting = true
	mock.inspectResults["new-nginx"] = failed

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("expected an error from failed validation")
	}
	for _, op := range mock.ops {
		if op == "remove aaa" {
			t.Fatalf("old container removed after a failed validation: ops=%v", mock.ops)
		}
	}
	if len(mock.createCalls) != 1 {
		t.Errorf("createCalls = %v, want only the failed replacement (the old container is restored, not rebuilt)", mock.createCalls)
	}
	restored := []string{"remove new-nginx", "rename aaa nginx", "start aaa"}
	last := -1
	for _, op := range restored {
		i := opIndex(mock, op)
		if i < 0 || i < last {
			t.Fatalf("want %v in this order, got ops=%v", restored, mock.ops)
		}
		last = i
	}
	history, _ := u.store.ListHistory(10, "")
	if len(history) == 0 || history[0].Outcome != "rollback" {
		t.Errorf("history = %+v, want a rollback record", history)
	}
}

// When the old container cannot be given its name back (here the rename
// fails; for a --rm container the daemon has already deleted it), the
// update falls back to recreating it from the snapshot.
func TestUpdateContainerFallsBackToSnapshotWhenOldContainerIsGone(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	mock.mirrorCreate = true
	mock.imageIDs["sha256:old111"] = "sha256:old111" // the old image is still on the host
	mock.createErrOnCall[1] = errors.New("No such image")
	mock.renameErrTo["nginx"] = errors.New("No such container: aaa")

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err == nil {
		t.Fatal("expected an error from the failed create")
	}
	if len(mock.createCalls) != 2 {
		t.Errorf("createCalls = %v, want the failed replacement and the snapshot rollback", mock.createCalls)
	}
	history, _ := u.store.ListHistory(10, "")
	if len(history) == 0 || history[0].Outcome != "rollback" {
		t.Errorf("history = %+v, want a rollback record", history)
	}
}

// The daemon deletes a --rm container as soon as Sentinel stops it, so
// removing it afterwards fails. The update must still go through.
func TestUpdateContainerReplacesAutoRemoveContainer(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	old := mock.inspectResults["aaa"]
	old.HostConfig.AutoRemove = true
	mock.inspectResults["aaa"] = old
	mock.removeErr["aaa"] = errors.New("No such container: aaa")

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("UpdateContainer: %v (createCalls=%v)", err, mock.createCalls)
	}
	if len(mock.createCalls) == 0 {
		t.Fatal("the --rm container was never recreated")
	}
}
