package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
