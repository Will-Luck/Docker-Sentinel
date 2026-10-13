package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func selfTestServer() *Server {
	return newPolicyTestServer(
		&mockContainerLister{containers: []ContainerSummary{
			{Names: []string{"/sentinel"}, Labels: map[string]string{"sentinel.self": "true"}},
		}},
		&mockPolicyStore{},
		nil,
		[]RemoteContainer{
			{Name: "sentinel-agent", HostID: "h1", HostName: "remote-host", Labels: map[string]string{"sentinel.self": "true"}},
			{Name: "sentinel", HostID: "h1", HostName: "remote-host", Labels: map[string]string{"sentinel.self": "true"}},
		},
	)
}

// The hub protects only its own Sentinel container. A queued update for a
// Sentinel agent on another host is that agent's update and must be
// approvable (the agent replaces itself safely).
func TestApiApprove_RemoteSentinelAgentIsNotProtected(t *testing.T) {
	srv := selfTestServer()
	srv.deps.Queue = &mockQueue{}
	for _, key := range []string{"h1::sentinel-agent", "h1::sentinel"} {
		r := httptest.NewRequest(http.MethodPost, "/api/approve/"+key, nil)
		r.SetPathValue("key", key)
		w := httptest.NewRecorder()
		srv.apiApprove(w, r)
		if w.Code == http.StatusForbidden {
			t.Errorf("approving %s: 403 %s; a remote agent is not the hub", key, w.Body.String())
		}
	}

	r := httptest.NewRequest(http.MethodPost, "/api/approve/sentinel", nil)
	r.SetPathValue("key", "sentinel")
	w := httptest.NewRecorder()
	srv.apiApprove(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("approving the hub's own container: status %d, want 403", w.Code)
	}
}

// On the Queue page only the hub's own container gets the "Update
// Sentinel" (self-update) button; a remote agent's row gets the normal
// approve action, which reaches that agent.
func TestIsQueueItemSelf_OnlyTheLocalSentinel(t *testing.T) {
	srv := selfTestServer()
	ctx := context.Background()
	if srv.isQueueItemSelf(ctx, PendingUpdate{HostID: "h1", ContainerName: "sentinel-agent"}) {
		t.Error("remote agent row marked as the hub itself")
	}
	if srv.isQueueItemSelf(ctx, PendingUpdate{HostID: "h1", ContainerName: "sentinel"}) {
		t.Error("remote agent named like the hub's container marked as the hub itself")
	}
	if !srv.isQueueItemSelf(ctx, PendingUpdate{ContainerName: "sentinel"}) {
		t.Error("the hub's own container not marked as itself")
	}
}

// approvingQueue is an UpdateQueue whose Approve hands the item over.
type approvingQueue struct{ items []PendingUpdate }

func (q *approvingQueue) List() []PendingUpdate { return q.items }
func (q *approvingQueue) Get(key string) (PendingUpdate, bool) {
	for _, it := range q.items {
		if it.Key() == key {
			return it, true
		}
	}
	return PendingUpdate{}, false
}
func (q *approvingQueue) Add(u PendingUpdate) { q.items = append(q.items, u) }
func (q *approvingQueue) Approve(key string) (PendingUpdate, bool) {
	for i, it := range q.items {
		if it.Key() == key {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return it, true
		}
	}
	return PendingUpdate{}, false
}
func (q *approvingQueue) Remove(string) {}

// recordingUpdater is a local ContainerUpdater that records its calls.
type recordingUpdater struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingUpdater) UpdateContainer(_ context.Context, id, name, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, id+"/"+name)
	return nil
}
func (r *recordingUpdater) IsUpdating(string) bool { return false }
func (r *recordingUpdater) IsIdle() bool           { return true }
func (r *recordingUpdater) SelfUpdateQueued() bool { return false }
func (r *recordingUpdater) count() int             { r.mu.Lock(); defer r.mu.Unlock(); return len(r.calls) }

// A queued update for a remote host cannot be applied when cluster mode is
// off or the host has left the cluster. Approve must refuse it, keep it
// queued, and never hand the remote container to the local updater.
func TestApiApprove_RefusesRemoteRowWithoutItsHost(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cluster bool
	}{{"cluster off", false}, {"host not in cluster", true}} {
		t.Run(tc.name, func(t *testing.T) {
			var srv *Server
			if tc.cluster {
				srv = selfTestServer() // knows host h1 only
			} else {
				srv = newPolicyTestServer(&mockContainerLister{}, &mockPolicyStore{}, nil, nil)
			}
			q := &approvingQueue{items: []PendingUpdate{{HostID: "h9", ContainerID: "remote-id", ContainerName: "web", CurrentImage: "nginx:1"}}}
			up := &recordingUpdater{}
			srv.deps.Queue = q
			srv.deps.Updater = up
			srv.deps.Store = nopHistory{}

			r := httptest.NewRequest(http.MethodPost, "/api/approve/h9::web", nil)
			r.SetPathValue("key", "h9::web")
			w := httptest.NewRecorder()
			srv.apiApprove(w, r)

			if w.Code != http.StatusConflict {
				t.Errorf("status = %d (%s), want 409", w.Code, w.Body.String())
			}
			if _, ok := q.Get("h9::web"); !ok {
				t.Error("refused update was taken off the queue")
			}
			time.Sleep(100 * time.Millisecond)
			if n := up.count(); n != 0 {
				t.Errorf("remote container handed to the local updater %d times", n)
			}
		})
	}
}

// nopHistory is a HistoryStore that keeps nothing.
type nopHistory struct{}

func (nopHistory) ListHistory(int, string) ([]UpdateRecord, error)            { return nil, nil }
func (nopHistory) ListAllHistory() ([]UpdateRecord, error)                    { return nil, nil }
func (nopHistory) ListHistoryByContainer(string, int) ([]UpdateRecord, error) { return nil, nil }
func (nopHistory) GetMaintenance(string) (bool, error)                        { return false, nil }
func (nopHistory) RecordUpdate(UpdateRecord) error                            { return nil }

// Restart, start and GHCR switch protect only the hub's own container. The
// same name on a remote host (here a Sentinel agent) is an ordinary remote
// container, addressed with ?host=. (Stop is stricter for a remote agent:
// TestApiStop_RefusesARemoteSentinelAgent.)
func TestProtectedHandlers_OnlyTheHubsOwnContainer(t *testing.T) {
	handlers := map[string]func(*Server, http.ResponseWriter, *http.Request){
		"restart": (*Server).apiRestart,
		"start":   (*Server).apiStart,
		"ghcr":    (*Server).apiSwitchToGHCR,
	}
	for op, h := range handlers {
		t.Run(op, func(t *testing.T) {
			srv := selfTestServer()

			r := httptest.NewRequest(http.MethodPost, "/api/x/sentinel?host=h1", nil)
			r.SetPathValue("name", "sentinel")
			w := httptest.NewRecorder()
			h(srv, w, r)
			if w.Code == http.StatusForbidden {
				t.Errorf("%s on the remote agent named sentinel: 403 %s", op, w.Body.String())
			}

			r = httptest.NewRequest(http.MethodPost, "/api/x/sentinel", nil)
			r.SetPathValue("name", "sentinel")
			w = httptest.NewRecorder()
			h(srv, w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s on the hub's own container: status %d, want 403", op, w.Code)
			}
		})
	}
}

// Stopping a Sentinel agent's own container from the hub leaves that host
// with nothing to start it again: the hub refuses. An ordinary container
// on the same host can still be stopped, and the hub's own container stays
// protected.
func TestApiStop_RefusesARemoteSentinelAgent(t *testing.T) {
	srv := newPolicyTestServer(
		&mockContainerLister{containers: []ContainerSummary{
			{Names: []string{"/sentinel"}, Labels: map[string]string{"sentinel.self": "true"}},
		}},
		&mockPolicyStore{}, nil,
		[]RemoteContainer{
			{Name: "sentinel-agent", HostID: "h1", HostName: "remote-host", Labels: map[string]string{"sentinel.self": "true"}},
			{Name: "web", HostID: "h1", HostName: "remote-host"},
		},
	)
	stop := func(path, name string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.SetPathValue("name", name)
		w := httptest.NewRecorder()
		srv.apiStop(w, r)
		return w
	}

	w := stop("/api/stop/sentinel-agent?host=h1", "sentinel-agent")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "would leave that host unmanaged") {
		t.Errorf("stopping the remote agent: %d %s, want 403 saying it would leave the host unmanaged", w.Code, w.Body.String())
	}
	if w := stop("/api/stop/web?host=h1", "web"); w.Code == http.StatusForbidden {
		t.Errorf("stopping an ordinary remote container: 403 %s", w.Body.String())
	}
	if w := stop("/api/stop/sentinel", "sentinel"); w.Code != http.StatusForbidden {
		t.Errorf("stopping the hub's own container: status %d, want 403", w.Code)
	}
}
