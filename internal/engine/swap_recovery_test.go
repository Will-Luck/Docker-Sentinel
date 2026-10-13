package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// flakyJournal is a store journal whose nth SaveSwap (1-based) fails.
type flakyJournal struct {
	*store.Store
	fail  map[int]bool
	saves int
}

func (j *flakyJournal) SaveSwap(rec store.SwapRecord) error {
	j.saves++
	if j.fail[j.saves] {
		return errors.New("disk full")
	}
	return j.Store.SaveSwap(rec)
}

func swapFor(t *testing.T, f *fakeDocker, u *Updater, j swapJournal) (*swap, container.InspectResponse) {
	t.Helper()
	old, err := f.InspectContainer(context.Background(), "old")
	if err != nil {
		t.Fatal(err)
	}
	return newSwap(f, u.log, j, swapSpec{kind: swapUpdate, name: "app", oldID: "old", old: old, stopTimeout: 10, snapshot: []byte(`{}`)}), old
}

// --- item 1: pending swaps ---

func pendingRecord(t *testing.T, u *Updater) store.SwapRecord {
	t.Helper()
	rec := store.SwapRecord{Name: "app", Kind: swapUpdate, OldID: "stuck", NewID: "old", OldRunning: true}
	if err := u.store.SaveSwap(rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// An update or rollback of a container with an unfinished swap changes
// nothing: no hook, no pull, no rename, and the record is kept.
func TestPendingSwapBlocksUpdateBeforeAnyChange(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	pendingRecord(t, u)

	err := u.UpdateContainer(context.Background(), "old", "app", "")
	if !errors.Is(err, ErrSwapPending) || !strings.Contains(err.Error(), "DELETE /api/swaps/app") {
		t.Fatalf("err = %v, want ErrSwapPending pointing at the DELETE endpoint", err)
	}
	if len(f.pullCalls) != 0 || len(f.ops()) != 0 {
		t.Errorf("pulls = %v ops = %v, want nothing done", f.pullCalls, f.ops())
	}
	if err := u.RollbackContainer(context.Background(), "app"); !errors.Is(err, ErrSwapPending) {
		t.Errorf("rollback err = %v, want ErrSwapPending", err)
	}
	if recs := journal(t, u); len(recs) != 1 || recs[0].OldID != "stuck" {
		t.Errorf("journal = %+v, want the record kept", recs)
	}
}

// A swap whose recovery fails for a passing reason is retried at the next
// scan, without a restart.
func TestScanRetriesPendingSwap(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, false, nil)
	interrupted(t, f, u, store.SwapRecord{Name: "app", Kind: swapUpdate, OldID: "old", NewID: "new", OldRunning: true}, false, nil)
	f.failRenameOnce["app"] = errors.New("daemon busy")

	u.RecoverInterruptedUpdates(context.Background())
	if recs := journal(t, u); len(recs) != 1 || recs[0].Attempts != 1 {
		t.Fatalf("journal after the failed attempt = %+v, want one attempt counted", recs)
	}

	u.Scan(context.Background(), ScanScheduled)

	// The scan restores the old container first (it may then go on to
	// update it, as latest auto-update is on in the test config).
	assertFakeOps(t, f, "remove new", "rename old app", "start old")
	if f.runningWithName("app") != 1 || len(journal(t, u)) != 0 {
		t.Errorf("containers = %v journal = %+v, want one running app and an empty journal", f.names(), journal(t, u))
	}
}

// After three failed attempts the user is told once, with what to do.
func TestPendingSwapNotifiesOnceAfterThreeFailures(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, false, nil)
	interrupted(t, f, u, store.SwapRecord{Name: "app", Kind: swapUpdate, OldID: "old", OldRunning: true}, false, nil)
	f.add("someone", "app", "app:latest", true, nil, nil) // holds the name for good
	rec := recordNotifications(u)

	for range 5 {
		u.resolvePendingSwaps(context.Background())
	}

	got := rec.ofType(notify.EventRollbackFailed)
	if len(got) != 1 || !strings.Contains(got[0].Error, "DELETE /api/swaps/app") {
		t.Fatalf("notifications = %+v, want one pointing at the DELETE endpoint", got)
	}
	if recs := journal(t, u); len(recs) != 1 || recs[0].Attempts != 5 || !recs[0].Notified {
		t.Errorf("journal = %+v, want five attempts counted and notified", recs)
	}
}

func TestDiscardSwap(t *testing.T) {
	_, u, _ := setupFakeUpdate(t, false, nil)
	pendingRecord(t, u)

	if err := u.DiscardSwap("other"); !errors.Is(err, ErrNoSwap) {
		t.Errorf("DiscardSwap(other) = %v, want ErrNoSwap", err)
	}
	if !u.tryLock("app") {
		t.Fatal("tryLock")
	}
	if err := u.DiscardSwap("app"); !errors.Is(err, ErrUpdateInProgress) {
		t.Errorf("DiscardSwap while locked = %v, want ErrUpdateInProgress", err)
	}
	u.unlock("app")
	if err := u.DiscardSwap("app"); err != nil {
		t.Fatalf("DiscardSwap: %v", err)
	}
	if len(journal(t, u)) != 0 {
		t.Error("record still there")
	}
}

// --- item 2: lost create result ---

// When the replacement's ID never reached the journal, recovery recognises
// it by its swap token label and removes it.
func TestRecoverFindsReplacementByToken(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	interrupted(t, f, u, store.SwapRecord{Name: "app", Kind: swapUpdate, OldID: "old", Token: "t0k", OldRunning: true}, false, nil)
	f.add("orphan", "app", "app:latest", false, &container.Config{Labels: map[string]string{swapLabel: "t0k"}}, nil)

	u.RecoverInterruptedUpdates(context.Background())

	if c := f.byName("app"); c == nil || c.id != "old" || f.find("orphan") != nil {
		t.Errorf("containers = %v, want the token holder removed and the old container restored", f.names())
	}
}

// A failure to journal the replacement's ID fails the swap, which is then
// undone, instead of carrying on with a record recovery could not use.
func TestReplaceFailsWhenReplacementCannotBeJournalled(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	j := &flakyJournal{Store: u.store, fail: map[int]bool{2: true}}
	sw, old := swapFor(t, f, u, j)

	err := sw.replace(context.Background(), recreateFrom(old), true, nil)
	var se *swapError
	if !errors.As(err, &se) || se.stage != "journal" {
		t.Fatalf("err = %v, want a journal-stage failure", err)
	}
	assertNoFakeOp(t, f, "stop old")
	if undoErr := sw.undo(context.Background()); undoErr != nil {
		t.Fatalf("undo: %v", undoErr)
	}
	if c := f.byName("app"); c == nil || c.id != "old" || !c.running || len(f.containers) != 1 {
		t.Errorf("containers = %v, want only the old container", f.names())
	}
}

// --- item 3: rollback of a --rm container ---

// The live --rm container is deleted by the daemon on stop. When the
// rollback container then fails, the live one is rebuilt from its snapshot.
func TestRollbackRebuildsLiveContainerWhenGone(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, &container.HostConfig{AutoRemove: true})
	f.imageIDs["sha256:new"] = "sha256:new"
	f.imageIDs["sha256:old"] = "sha256:old" // the rollback image is on the host
	rollbackSetup(&container.HostConfig{AutoRemove: true})(f, u)
	f.failStart["app-1"] = errors.New("OCI error")

	if err := u.RollbackContainer(context.Background(), "app"); err == nil {
		t.Fatal("expected the rollback to fail")
	}
	c := f.byName("app")
	if c == nil || !c.running || c.imageID != "sha256:new" {
		t.Fatalf("containers = %v, want the live container rebuilt on its own image", f.names())
	}
	if len(journal(t, u)) != 0 {
		t.Errorf("journal = %+v, want empty", journal(t, u))
	}
}

// --- item 5: restart policy ---

// A policy that cannot be restored is a failed undo: the old container
// still starts, the record stays, and a later recovery restores the policy.
func TestFailedPolicyRestoreKeepsRecord(t *testing.T) {
	f, u, old := setupFakeUpdate(t, true, &container.HostConfig{RestartPolicy: always})
	f.failStart["app-1"] = errors.New("OCI error")
	f.failPolicyTo = map[container.RestartPolicyMode]error{container.RestartPolicyAlways: errors.New("daemon busy")}

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the update to fail")
	}
	if !old.running || len(journal(t, u)) != 1 {
		t.Fatalf("containers = %v journal = %+v, want the old container running and the record kept", f.names(), journal(t, u))
	}

	f.failPolicyTo = nil
	u.resolvePendingSwaps(context.Background())

	if old.host.RestartPolicy != always || len(journal(t, u)) != 0 {
		t.Errorf("policy = %+v journal = %+v, want always back and an empty journal", old.host.RestartPolicy, journal(t, u))
	}
}

// When the policy change cannot be journalled it is not made, so it can
// never be lost.
func TestRestartPolicyLeftAloneWhenItCannotBeJournalled(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, &container.HostConfig{RestartPolicy: always})
	j := &flakyJournal{Store: u.store, fail: map[int]bool{3: true}} // 1 start, 2 new ID, 3 policy
	sw, old := swapFor(t, f, u, j)

	if err := sw.replace(context.Background(), recreateFrom(old), true, nil); err != nil {
		t.Fatalf("replace: %v", err)
	}
	assertNoFakeOp(t, f, "restart-policy old no")
}

// --- item 6: what marks an update as rolled back ---

func TestOnlyImageFailuresMarkUpdateRolledBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeDocker)
		want  bool
	}{
		{"start fails", func(f *fakeDocker) { f.failStart["app-1"] = errors.New("OCI error") }, true},
		{"create fails", func(f *fakeDocker) { f.failCreate[1] = errors.New("conflict") }, false},
		{"stop fails", func(f *fakeDocker) { f.failStop["old"] = errors.New("timeout") }, false},
		{"start fails, restore fails", func(f *fakeDocker) {
			f.failStart["app-1"] = errors.New("OCI error")
			f.failRename["app"] = errors.New("daemon busy")
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, u, _ := setupFakeUpdate(t, true, nil)
			tc.setup(f)
			err := u.UpdateContainer(context.Background(), "old", "app", "")
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errors.Is(err, ErrRolledBack); got != tc.want {
				t.Errorf("errors.Is(ErrRolledBack) = %v, want %v (err %v)", got, tc.want, err)
			}
		})
	}
}

// --- item 11: failed rename back ---

// When the old container cannot get its name back, it is still started
// under its set-aside name (the service runs) and the swap stays pending;
// a later recovery gives the name back.
func TestFailedRenameBackStillStartsOldContainer(t *testing.T) {
	f, u, old := setupFakeUpdate(t, true, &container.HostConfig{RestartPolicy: always})
	f.failStart["app-1"] = errors.New("OCI error")
	f.failRename["app"] = errors.New("daemon busy")

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the update to fail")
	}
	if !old.running || old.name != "app"+replacedSuffix || old.host.RestartPolicy != always {
		t.Fatalf("containers = %v policy = %+v, want the old container running as app-sentinel-old with its policy", f.names(), old.host.RestartPolicy)
	}
	if len(journal(t, u)) != 1 {
		t.Fatalf("journal = %+v, want the record kept", journal(t, u))
	}

	delete(f.failRename, "app")
	u.resolvePendingSwaps(context.Background())

	if old.name != "app" || !old.running || len(journal(t, u)) != 0 {
		t.Errorf("containers = %v journal = %+v, want app restored", f.names(), journal(t, u))
	}
}

// --- LOW ---

// A commit that cannot be journalled at first, and whose removal of the old
// container also fails, retries the save, so the record says committed and
// recovery never puts the replaced container back.
func TestCommitRetriesJournalWhenRemovalFails(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	j := &flakyJournal{Store: u.store, fail: map[int]bool{5: true}} // 1 start, 2 new ID, 3 stopping, 4 stopped, 5 commit
	sw, old := swapFor(t, f, u, j)
	if err := sw.replace(context.Background(), recreateFrom(old), true, nil); err != nil {
		t.Fatalf("replace: %v", err)
	}
	f.failRemoveOnce["old"] = errors.New("device busy")

	sw.commit(context.Background())

	rec, found, _ := u.store.GetSwap("app")
	if !found || !rec.Committed {
		t.Fatalf("record = %+v (found %v), want it kept and committed", rec, found)
	}
}

// Network repair and dependent restarts skip a container set aside by a
// journalled swap, but not a container that merely has such a name.
func TestRepairSkipsOnlyJournalledSetAsideContainers(t *testing.T) {
	f := newFakeDocker()
	f.add("vpn", "gluetun", "vpn:2", true, nil, nil)
	f.add("q1", "qbit-sentinel-old", "qbit:4", true, nil, &container.HostConfig{NetworkMode: "container:gluetun"})
	u := newFakeUpdater(t, f)

	u.repairNetworkNamespace(context.Background(), "vpn", "gluetun")

	if !slices.Contains(f.ops(), "restart q1") {
		t.Errorf("ops = %v, want the user's qbit-sentinel-old restarted", f.ops())
	}
}
