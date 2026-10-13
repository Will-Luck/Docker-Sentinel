package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// fakeCluster is a ClusterScanner with a fixed set of connected hosts and
// per-host container lists or list errors.
type fakeCluster struct {
	mu        sync.Mutex
	connected []string
	removed   map[string]bool // hosts no longer in the cluster
	lists     map[string][]RemoteContainer
	listErr   map[string]error
	updates   []string // "hostID/name -> target"
	outcome   string   // the agents' answer to an update; "success" when empty
	updateErr string   // when set, every update fails to reach the agent
	// updateErrValue, when set, is the error every update fails with.
	updateErrValue error
}

// tickingClock moves on a millisecond every time it is read.
type tickingClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *tickingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func (c *tickingClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (c *tickingClock) Since(t time.Time) time.Duration        { return c.Now().Sub(t) }

func (f *fakeCluster) ConnectedHosts() []string { return f.connected }

func (f *fakeCluster) HostInfo(hostID string) (HostContext, bool) {
	if f.removed[hostID] {
		return HostContext{}, false
	}
	return HostContext{HostID: hostID, HostName: "name-" + hostID}, true
}

func (f *fakeCluster) ListContainers(_ context.Context, hostID string) ([]RemoteContainer, error) {
	if err := f.listErr[hostID]; err != nil {
		return nil, err
	}
	return f.lists[hostID], nil
}

func (f *fakeCluster) UpdateContainer(_ context.Context, hostID, name, target, _ string) (RemoteUpdateResult, error) {
	f.mu.Lock()
	f.updates = append(f.updates, hostID+"/"+name+" -> "+target)
	f.mu.Unlock()
	if f.updateErrValue != nil {
		return RemoteUpdateResult{}, f.updateErrValue
	}
	if f.updateErr != "" {
		return RemoteUpdateResult{}, errors.New(f.updateErr)
	}
	outcome := f.outcome
	if outcome == "" {
		outcome = "success"
	}
	return RemoteUpdateResult{ContainerName: name, NewImage: target, Outcome: outcome}, nil
}

func queuedKeys(u *Updater) map[string]bool {
	out := map[string]bool{}
	for _, p := range u.queue.List() {
		out[p.Key()] = true
	}
	return out
}

// A scan prunes queue entries whose container is gone. For a remote host
// it can only know that when the host is connected and its list came back
// this scan. A host that has not reconnected yet (after a hub restart), is
// partitioned, or whose list failed keeps its entries.
func TestScanPrunesRemoteQueueOnlyForHostsItHeardFrom(t *testing.T) {
	mock := newMockDocker()
	mock.distDigests["nginx:1"] = "sha256:new" // still-here's update still applies
	u, _ := newTestUpdater(t, mock)
	fc := &fakeCluster{
		connected: []string{"listed", "failing"},
		lists:     map[string][]RemoteContainer{"listed": {{Name: "still-here", Image: "nginx:1", ImageDigest: "sha256:old"}}},
		listErr:   map[string]error{"failing": errors.New("agent could not list its containers")},
	}
	u.SetClusterScanner(fc)

	for _, p := range []PendingUpdate{
		{HostID: "listed", ContainerName: "still-here", CurrentImage: "nginx:1"},
		{HostID: "listed", ContainerName: "removed", CurrentImage: "nginx:1"},
		{HostID: "failing", ContainerName: "web", CurrentImage: "nginx:1"},
		{HostID: "offline", ContainerName: "db", CurrentImage: "postgres:16"},
	} {
		u.queue.Add(p)
	}

	u.Scan(context.Background(), ScanScheduled)

	got := queuedKeys(u)
	for _, keep := range []string{"listed::still-here", "failing::web", "offline::db"} {
		if !got[keep] {
			t.Errorf("queue entry %s was pruned", keep)
		}
	}
	if got["listed::removed"] {
		t.Error("queue entry listed::removed survived although its host listed without it")
	}
}

// A same-tag update (a new image behind the tag a remote container runs)
// has no newer tag. The scan must still send the agent a real image
// reference: agents before v2.17.0 fail on an empty one.
func TestScanRemoteSameTagUpdateSendsCurrentImage(t *testing.T) {
	mock := newMockDocker()
	mock.distDigests["nginx:latest"] = "sha256:new"
	u, _ := newTestUpdater(t, mock)
	u.cfg.SetDefaultPolicy("auto")
	fc := &fakeCluster{
		connected: []string{"h1"},
		lists: map[string][]RemoteContainer{"h1": {
			{Name: "web", Image: "nginx:latest", ImageDigest: "sha256:old", State: "running"},
		}},
	}
	u.SetClusterScanner(fc)

	u.Scan(context.Background(), ScanScheduled)

	if len(fc.updates) != 1 || fc.updates[0] != "h1/web -> nginx:latest" {
		t.Fatalf("updates sent = %q, want [h1/web -> nginx:latest]", fc.updates)
	}
}

// History rows for remote updates carry the host, so History shows the
// host name and remote rollback can find them.
func TestScanRemoteAutoUpdateHistoryNamesTheHost(t *testing.T) {
	mock := newMockDocker()
	mock.distDigests["nginx:latest"] = "sha256:new"
	u, _ := newTestUpdater(t, mock)
	// History rows are keyed by time; with the frozen test clock the scan
	// summary would overwrite the update row.
	u.clock = &tickingClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	u.cfg.SetDefaultPolicy("auto")
	u.SetClusterScanner(&fakeCluster{
		connected: []string{"h1"},
		lists: map[string][]RemoteContainer{"h1": {
			{Name: "web", Image: "nginx:latest", ImageDigest: "sha256:old", State: "running"},
		}},
	})

	u.Scan(context.Background(), ScanScheduled)

	recs, err := u.store.ListHistoryByContainer(store.ScopedKey("h1", "web"), 10)
	if err != nil || len(recs) != 1 {
		t.Fatalf("history for h1::web = %d rows (err %v), want 1", len(recs), err)
	}
	if recs[0].HostID != "h1" || recs[0].HostName != "name-h1" {
		t.Errorf("history row host = %q/%q, want h1/name-h1", recs[0].HostID, recs[0].HostName)
	}
}

// A host removed or revoked from the cluster will never list again: its
// queued updates go, while a host that is only offline keeps its own.
func TestScanPrunesQueueOfRemovedHost(t *testing.T) {
	u, _ := newTestUpdater(t, newMockDocker())
	u.SetClusterScanner(&fakeCluster{removed: map[string]bool{"gone": true}})
	u.queue.Add(PendingUpdate{HostID: "gone", ContainerName: "web", CurrentImage: "nginx:1"})
	u.queue.Add(PendingUpdate{HostID: "offline", ContainerName: "db", CurrentImage: "postgres:16"})

	u.Scan(context.Background(), ScanScheduled)

	got := queuedKeys(u)
	if got["gone::web"] {
		t.Error("queued update of a removed host was kept")
	}
	if !got["offline::db"] {
		t.Error("queued update of an offline host was pruned")
	}
}

// With cluster mode off there are no remote hosts: their queued updates go.
func TestScanPrunesRemoteQueueWhenClusterIsOff(t *testing.T) {
	u, _ := newTestUpdater(t, newMockDocker())
	u.queue.Add(PendingUpdate{HostID: "h1", ContainerName: "web", CurrentImage: "nginx:1"})

	u.Scan(context.Background(), ScanScheduled)

	if queuedKeys(u)["h1::web"] {
		t.Error("remote queued update kept with cluster mode off")
	}
}

// A remote update a pre-update hook skipped (exit 75) changed nothing: the
// scan counts it as skipped, not as an update or a failure, and writes no
// history row, as for a local container.
func TestScanRemoteHookSkipIsCountedAsSkipped(t *testing.T) {
	mock := newMockDocker()
	mock.distDigests["nginx:latest"] = "sha256:new"
	u, _ := newTestUpdater(t, mock)
	u.clock = &tickingClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	u.cfg.SetDefaultPolicy("auto")
	u.SetClusterScanner(&fakeCluster{
		connected: []string{"h1"},
		lists: map[string][]RemoteContainer{"h1": {
			{Name: "web", Image: "nginx:latest", ImageDigest: "sha256:old", State: "running"},
		}},
		outcome: "skipped",
	})

	res := u.Scan(context.Background(), ScanScheduled)

	if res.Updated != 0 || res.Failed != 0 || res.Skipped != 1 {
		t.Errorf("updated %d, failed %d, skipped %d; want the skip counted as skipped", res.Updated, res.Failed, res.Skipped)
	}
	if recs, _ := u.store.ListHistoryByContainer(store.ScopedKey("h1", "web"), 10); len(recs) != 0 {
		t.Errorf("history = %+v, want no row for a skipped update", recs)
	}
}

// fakeClusterInFlight is a fakeCluster that reports some updates in flight.
type fakeClusterInFlight struct {
	*fakeCluster
	inFlight map[string]bool // "hostID/name"
}

func (f *fakeClusterInFlight) UpdateInFlight(hostID, name string) bool {
	return f.inFlight[hostID+"/"+name]
}

// live-18: a scan that ran while an update of a remote container was in
// flight saw its old digest and queued (or dispatched) the update again,
// leaving a phantom queue row once it had finished. A container with an
// update in flight is left to that update.
func TestScanRemoteSkipsContainersWithAnUpdateInFlight(t *testing.T) {
	for _, policy := range []string{"manual", "auto"} {
		t.Run(policy, func(t *testing.T) {
			mock := newMockDocker()
			mock.distDigests["nginx:latest"] = "sha256:new"
			u, _ := newTestUpdater(t, mock)
			u.cfg.SetDefaultPolicy(policy)
			u.cfg.SetLatestAutoUpdate(false) // :latest follows the policy
			fc := &fakeCluster{
				connected: []string{"h1"},
				lists: map[string][]RemoteContainer{"h1": {
					{Name: "web", Image: "nginx:latest", ImageDigest: "sha256:old", State: "running"},
				}},
			}
			u.SetClusterScanner(&fakeClusterInFlight{fakeCluster: fc, inFlight: map[string]bool{"h1/web": true}})

			u.Scan(context.Background(), ScanScheduled)

			if len(fc.updates) != 0 {
				t.Errorf("updates sent = %q, want none while one is in flight", fc.updates)
			}
			if queuedKeys(u)["h1::web"] {
				t.Error("the container was queued again while its update was in flight")
			}
		})
	}
}

// live-22 and remote-orchestration-11: a remote queue row stayed after its
// container became up to date, or was filtered out or pinned since it was
// queued. The scan removes it, as it does for a local container that is up
// to date.
func TestScanRemoteRemovesQueueRowsThatNoLongerApply(t *testing.T) {
	mock := newMockDocker()
	mock.distDigests["nginx:latest"] = "sha256:same"
	mock.distDigests["redis:latest"] = "sha256:new"
	mock.distDigests["pg:latest"] = "sha256:new"
	u, _ := newTestUpdater(t, mock)
	u.cfg.SetDefaultPolicy("manual")
	if err := u.store.SaveSetting("filters", "cache"); err != nil {
		t.Fatal(err)
	}
	u.SetSettingsReader(u.store)
	u.SetClusterScanner(&fakeCluster{
		connected: []string{"h1"},
		lists: map[string][]RemoteContainer{"h1": {
			{Name: "web", Image: "nginx:latest", ImageDigest: "sha256:same", State: "running"},
			{Name: "cache", Image: "redis:latest", ImageDigest: "sha256:old", State: "running"},
			{Name: "db", Image: "pg:latest", ImageDigest: "sha256:old", State: "running", Labels: map[string]string{"sentinel.policy": "pinned"}},
		}},
	})
	for _, name := range []string{"web", "cache", "db"} {
		u.queue.Add(PendingUpdate{HostID: "h1", ContainerName: name, CurrentImage: "x:latest"})
	}

	res := u.Scan(context.Background(), ScanScheduled)

	got := queuedKeys(u)
	for _, key := range []string{"h1::web", "h1::cache", "h1::db"} {
		if got[key] {
			t.Errorf("queue row %s survived although it no longer applies", key)
		}
	}
	if res.UpToDate < 1 {
		t.Errorf("up to date = %d, want the remote container counted", res.UpToDate)
	}
}
