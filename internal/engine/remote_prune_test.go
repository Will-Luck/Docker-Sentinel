package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeCluster is a ClusterScanner with a fixed set of connected hosts and
// per-host container lists or list errors.
type fakeCluster struct {
	mu        sync.Mutex
	connected []string
	lists     map[string][]RemoteContainer
	listErr   map[string]error
	updates   []string // "hostID/name -> target"
}

func (f *fakeCluster) ConnectedHosts() []string { return f.connected }

func (f *fakeCluster) HostInfo(hostID string) (HostContext, bool) {
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
	return RemoteUpdateResult{ContainerName: name, NewImage: target, Outcome: "success"}, nil
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
	u, _ := newTestUpdater(t, newMockDocker())
	fc := &fakeCluster{
		connected: []string{"listed", "failing"},
		lists:     map[string][]RemoteContainer{"listed": {{Name: "still-here", Image: "nginx:1"}}},
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
