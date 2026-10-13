package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// setupFakeUpdate builds "app" on app:latest, running image sha256:old, and
// a pull that moves app:latest to sha256:new.
func setupFakeUpdate(t *testing.T, running bool, host *container.HostConfig) (*fakeDocker, *Updater, *fakeContainer) {
	t.Helper()
	f := newFakeDocker()
	f.imageIDs["app:latest"] = "sha256:new"
	old := f.add("old", "app", "app:latest", running, &container.Config{Labels: map[string]string{}}, host)
	old.imageID = "sha256:old"
	return f, newFakeUpdater(t, f), old
}

func journal(t *testing.T, u *Updater) []store.SwapRecord {
	t.Helper()
	recs, err := u.store.ListSwaps()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// --- Swap ---

// A swap journals the containers involved before it changes anything, and
// clears the record once it is committed.
func TestSwapJournalsBeforeChangingAnything(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	old, _ := f.InspectContainer(context.Background(), "old")
	sw := newSwap(f, u.log, u.store, swapSpec{kind: swapUpdate, name: "app", oldID: "old", old: old, stopTimeout: 10, snapshot: []byte(`{}`)})

	var seen []store.SwapRecord
	verify := func(context.Context, string) error {
		seen = journal(t, u)
		return nil
	}
	if err := sw.replace(context.Background(), recreateFrom(old), true, verify); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(seen) != 1 || seen[0].OldID != "old" || seen[0].NewID != sw.newID || !seen[0].OldRunning || seen[0].Snapshot == nil {
		t.Fatalf("journal during the swap = %+v", seen)
	}
	sw.commit(context.Background())
	if recs := journal(t, u); len(recs) != 0 {
		t.Errorf("journal after commit = %+v, want empty", recs)
	}
}

// A stop that fails while the old container keeps running must not start
// the replacement next to it: the update fails and the old container stays.
func TestUpdateContainerStopFailureKeepsOldRunning(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	f.failStop["old"] = errors.New("timeout")

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the update to fail when the old container cannot be stopped")
	}
	assertNoFakeOp(t, f, "start app-1")
	if c := f.byName("app"); c == nil || c.id != "old" || !c.running {
		t.Fatalf("containers = %v, want the old container running as app", f.names())
	}
	if f.runningWithName("app") != 1 {
		t.Errorf("containers = %v, want exactly one running copy", f.names())
	}
	if recs := journal(t, u); len(recs) != 0 {
		t.Errorf("journal = %+v, want empty", recs)
	}
}

// A stop that reports an error but did stop the container is fine.
func TestUpdateContainerStopErrorAfterStopCarriesOn(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	f.stopErrStopped["old"] = errors.New("context deadline exceeded")

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	if c := f.byName("app"); c == nil || c.id == "old" || !c.running {
		t.Errorf("containers = %v, want the replacement running as app", f.names())
	}
}

// The old container's restart policy is set to "no" before it is stopped,
// so a reboot during the swap cannot start it next to the replacement, and
// it is put back when the swap is undone.
func TestUpdateContainerDisablesRestartPolicyDuringSwap(t *testing.T) {
	always := container.RestartPolicy{Name: container.RestartPolicyAlways}
	f, u, old := setupFakeUpdate(t, true, &container.HostConfig{RestartPolicy: always})
	f.failStart["app-1"] = errors.New("OCI error")

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the start failure")
	}
	assertFakeOps(t, f, "restart-policy old no", "stop old", "rename old app", "restart-policy old always", "start old")
	if old.host.RestartPolicy != always {
		t.Errorf("restart policy = %+v, want always back", old.host.RestartPolicy)
	}
}

// After a successful update the replacement keeps the original policy.
func TestUpdateContainerKeepsRestartPolicyOnReplacement(t *testing.T) {
	always := container.RestartPolicy{Name: container.RestartPolicyAlways}
	f, u, _ := setupFakeUpdate(t, true, &container.HostConfig{RestartPolicy: always})

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	if c := f.byName("app"); c == nil || c.host.RestartPolicy != always {
		t.Errorf("replacement = %+v, want restart policy always", c)
	}
}

// Updating a stopped container does not stop anything and ends with one
// running replacement.
func TestUpdateContainerStoppedOldContainer(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, false, nil)

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	assertNoFakeOp(t, f, "stop old")
	if f.byName("app") == nil || f.runningWithName("app") != 1 || f.find("old") != nil {
		t.Errorf("containers = %v, want one running replacement and the old one removed", f.names())
	}
}

// A stopped <name>-sentinel-old that an earlier swap failed to remove is
// cleared before the next swap needs the name.
func TestUpdateContainerRemovesFinishedLeftover(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	f.add("stale", "app-sentinel-old", "app:latest", false, nil, nil)

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err != nil {
		t.Fatalf("UpdateContainer: %v (containers %v)", err, f.names())
	}
	assertFakeOps(t, f, "remove stale", "rename old app-sentinel-old")
}

// An earlier swap whose commit could not remove the old container left its
// record; the next swap of the same container finishes it first instead of
// overwriting the record.
func TestSwapFinishesCommittedLeftoverFirst(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	f.add("prev", "app-sentinel-old", "app:latest", false, nil, nil)
	if err := u.store.SaveSwap(store.SwapRecord{Name: "app", OldID: "prev", NewID: "old", Committed: true}); err != nil {
		t.Fatal(err)
	}

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err != nil {
		t.Fatalf("UpdateContainer: %v (containers %v)", err, f.names())
	}
	assertFakeOps(t, f, "remove prev", "rename old app-sentinel-old")
}

// An unresolved, uncommitted swap record is the only way back to the old
// container. A new swap refuses rather than overwrite it.
func TestSwapRefusesWhileEarlierSwapIsUnresolved(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	pending := store.SwapRecord{Name: "app", OldID: "stuck", NewID: "old"}
	if err := u.store.SaveSwap(pending); err != nil {
		t.Fatal(err)
	}

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the update to refuse")
	}
	assertNoFakeOp(t, f, "rename old app-sentinel-old")
	if recs := journal(t, u); len(recs) != 1 || recs[0].OldID != "stuck" {
		t.Errorf("journal = %+v, want the earlier record kept", recs)
	}
}

// --- Recovery ---

// interrupted puts the state of a swap Sentinel stopped in the middle of
// into the fake and the journal: oldID renamed aside, newID created under
// the real name.
func interrupted(t *testing.T, f *fakeDocker, u *Updater, rec store.SwapRecord, newRunning bool, newLabels map[string]string) {
	t.Helper()
	if c := f.find(rec.OldID); c != nil {
		c.name = rec.Name + replacedSuffix
	}
	if rec.NewID != "" {
		f.add(rec.NewID, rec.Name, "app:latest", newRunning, &container.Config{Labels: newLabels}, nil)
	}
	if err := u.store.SaveSwap(rec); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverUndoesUncommittedSwap(t *testing.T) {
	always := container.RestartPolicy{Name: container.RestartPolicyAlways}
	f, u, old := setupFakeUpdate(t, false, &container.HostConfig{RestartPolicy: container.RestartPolicy{Name: "no"}})
	interrupted(t, f, u, store.SwapRecord{
		Name: "app", Kind: swapUpdate, OldID: "old", NewID: "new", OldRunning: true,
		RestartPolicy: "always", RestartChanged: true,
	}, true, map[string]string{"sentinel.maintenance": "true"})
	_ = u.store.SetMaintenance("app", true)

	u.RecoverInterruptedUpdates(context.Background())

	if c := f.byName("app"); c == nil || c.id != "old" || !c.running {
		t.Fatalf("containers = %v, want the old container restored and running", f.names())
	}
	if f.find("new") != nil {
		t.Errorf("unvalidated replacement left: %v", f.names())
	}
	if old.host.RestartPolicy != always {
		t.Errorf("restart policy = %+v, want always back", old.host.RestartPolicy)
	}
	if recs := journal(t, u); len(recs) != 0 {
		t.Errorf("journal = %+v, want empty", recs)
	}
	if on, _ := u.store.GetMaintenance("app"); on {
		t.Error("maintenance flag left set")
	}
}

func TestRecoverFinishesCommittedSwap(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, false, nil)
	interrupted(t, f, u, store.SwapRecord{Name: "app", Kind: swapUpdate, OldID: "old", NewID: "new", OldRunning: true, Committed: true}, true, nil)

	u.RecoverInterruptedUpdates(context.Background())

	if f.find("old") != nil || f.byName("app") == nil || f.byName("app").id != "new" {
		t.Errorf("containers = %v, want only the replacement", f.names())
	}
}

// A --rm old container is deleted by the daemon when it stops. If Sentinel
// stopped before starting the replacement, only a created, labelled
// replacement is left: it is started, then finalised.
func TestRecoverKeepsReplacementWhenOldIsGone(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, false, nil)
	delete(f.containers, "old")
	interrupted(t, f, u, store.SwapRecord{Name: "app", Kind: swapUpdate, OldID: "old", NewID: "new", OldRunning: true, AutoRemove: true},
		false, map[string]string{"sentinel.maintenance": "true"})

	u.RecoverInterruptedUpdates(context.Background())

	c := f.byName("app")
	if c == nil || !c.running || c.cfg.Labels["sentinel.maintenance"] == "true" {
		t.Fatalf("containers = %v, want app running without the maintenance label", f.names())
	}
}

func TestRecoverRebuildsWhenBothContainersAreGone(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, false, nil)
	delete(f.containers, "old")
	snap, _ := json.Marshal(container.InspectResponse{
		ID: "old", Name: "/app", Config: &container.Config{Image: "app:latest"}, HostConfig: &container.HostConfig{},
	})
	interrupted(t, f, u, store.SwapRecord{Name: "app", OldID: "old", NewID: "gone", OldRunning: true, Snapshot: snap}, false, nil)
	delete(f.containers, "gone")

	u.RecoverInterruptedUpdates(context.Background())

	if c := f.byName("app"); c == nil || !c.running {
		t.Errorf("containers = %v, want app rebuilt and running", f.names())
	}
}

// Recovery acts only on journalled swaps: a container that is simply named
// like a set-aside one is left alone.
func TestRecoverIgnoresUnjournalledSuffix(t *testing.T) {
	f := newFakeDocker()
	f.add("a", "docker", "img:1", true, nil, nil)
	f.add("b", "docker-sentinel-old", "img:1", false, nil, nil)
	u := newFakeUpdater(t, f)

	u.RecoverInterruptedUpdates(context.Background())

	if len(f.ops()) != 0 {
		t.Errorf("ops = %v, want none", f.ops())
	}
}

// An old container that was not running before the swap is restored but
// not started.
func TestRecoverDoesNotStartOldThatWasStopped(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, false, nil)
	interrupted(t, f, u, store.SwapRecord{Name: "app", OldID: "old", NewID: "new", OldRunning: false}, false, nil)

	u.RecoverInterruptedUpdates(context.Background())

	if c := f.byName("app"); c == nil || c.id != "old" || c.running {
		t.Errorf("containers = %v, want the old container restored, not started", f.names())
	}
}

// Only the journalled replacement is ever removed. An unknown container
// holding the name is left alone, and so is the record, for the next start.
func TestRecoverLeavesUnknownContainerHoldingTheName(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, false, nil)
	interrupted(t, f, u, store.SwapRecord{Name: "app", OldID: "old", OldRunning: true}, false, nil)
	f.add("someone-else", "app", "app:latest", true, nil, nil)

	u.RecoverInterruptedUpdates(context.Background())

	if c := f.byName("app"); c == nil || c.id != "someone-else" {
		t.Errorf("containers = %v, want the unknown app untouched", f.names())
	}
	if recs := journal(t, u); len(recs) != 1 {
		t.Errorf("journal = %+v, want the record kept", recs)
	}
}

// Sentinel stopped in the finalise swap: the validated container (with the
// maintenance label) is restored and then finalised.
func TestRecoverAfterFinaliseSwap(t *testing.T) {
	f := newFakeDocker()
	f.imageIDs["app:latest"] = "sha256:new"
	f.add("n1", "app", "app:latest", false, &container.Config{Labels: map[string]string{"sentinel.maintenance": "true"}}, nil)
	u := newFakeUpdater(t, f)
	interrupted(t, f, u, store.SwapRecord{Name: "app", Kind: swapFinalise, OldID: "n1", NewID: "n2", OldRunning: true}, false, nil)
	_ = u.store.SetMaintenance("app", true)

	u.RecoverInterruptedUpdates(context.Background())

	c := f.byName("app")
	if c == nil || !c.running || c.cfg.Labels["sentinel.maintenance"] == "true" || f.runningWithName("app") != 1 {
		t.Fatalf("containers = %v, want one running app without the maintenance label", f.names())
	}
	if f.find("n2") != nil || f.find("n1") != nil {
		t.Errorf("containers = %v, want n1 finalised away and n2 removed", f.names())
	}
	if on, _ := u.store.GetMaintenance("app"); on {
		t.Error("maintenance flag left set")
	}
}

// recreateFrom builds a plain recreate request from an inspect result.
func recreateFrom(inspect container.InspectResponse) docker.Recreate {
	return docker.RecreateConfig(inspect, docker.RecreateOptions{})
}
