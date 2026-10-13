package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// A remote update a pre-update hook skipped (exit 75) changed nothing. As
// for a local container, it leaves no history row, neither a success nor a
// failure, whether it was approved from the queue or started with "Update
// now".
func TestRemoteUpdate_HookSkipLeavesNoHistory(t *testing.T) {
	skipped := fmt.Errorf("%w (exit 75)", cluster.ErrUpdateSkipped)
	newCluster := func() *failingUpdateCluster {
		return &failingUpdateCluster{
			mockClusterProviderWithContainers: mockClusterProviderWithContainers{
				hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"},
				containers: []RemoteContainer{{Name: "web", Image: "nginx:1", HostID: "h1", HostName: "Agent One"}},
			},
			err: skipped,
		}
	}

	t.Run("approve", func(t *testing.T) {
		hist := &recordingHistory{}
		srv := remoteUpdateServer(newCluster(), hist)
		srv.deps.Queue = &approvingQueue{items: []PendingUpdate{{HostID: "h1", HostName: "Agent One", ContainerName: "web", CurrentImage: "nginx:1"}}}
		r := httptest.NewRequest(http.MethodPost, "/api/approve/h1::web", nil)
		r.SetPathValue("key", "h1::web")
		w := httptest.NewRecorder()
		srv.apiApprove(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		time.Sleep(200 * time.Millisecond)
		hist.mu.Lock()
		defer hist.mu.Unlock()
		if len(hist.recs) != 0 {
			t.Errorf("history = %+v, want no row for a skipped update", hist.recs)
		}
	})

	t.Run("update now", func(t *testing.T) {
		hist := &recordingHistory{}
		srv := remoteUpdateServer(newCluster(), hist)
		r := httptest.NewRequest(http.MethodPost, "/api/containers/web/update?host=h1", nil)
		r.SetPathValue("name", "web")
		w := httptest.NewRecorder()
		srv.apiUpdate(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		time.Sleep(200 * time.Millisecond)
		hist.mu.Lock()
		defer hist.mu.Unlock()
		if len(hist.recs) != 0 {
			t.Errorf("history = %+v, want no row for a skipped update", hist.recs)
		}
	})
}

// remote-orchestration-8: "Ignore version" on a remote queue row was saved
// under the plain container name, so it applied to a local container of
// that name and never to the remote one. It is saved under the row's
// host-scoped key, which the remote scan reads.
func TestApiIgnoreVersion_RemoteRowUsesItsScopedKey(t *testing.T) {
	ig := &recordingIgnores{}
	item := PendingUpdate{HostID: "h1", ContainerName: "web", CurrentImage: "nginx:1", NewerVersions: []string{"2"}}
	srv := &Server{deps: Dependencies{Queue: &mockQueue{items: []PendingUpdate{item}}, IgnoredVersions: ig, EventBus: events.New(), Log: slog.Default()}}
	r := httptest.NewRequest(http.MethodPost, "/api/ignore/h1::web", nil)
	r.SetPathValue("key", "h1::web")
	w := httptest.NewRecorder()
	srv.apiIgnoreVersion(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if len(ig.added["web"]) != 0 || len(ig.added["h1::web"]) != 1 {
		t.Errorf("ignored versions saved under %v, want only h1::web", ig.added)
	}
}
