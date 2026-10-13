package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
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
