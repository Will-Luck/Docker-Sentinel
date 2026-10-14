package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// blockingUpdateCluster holds a remote update open until release is closed.
type blockingUpdateCluster struct {
	mockClusterProviderWithContainers
	release chan struct{}
}

func (b *blockingUpdateCluster) UpdateRemoteContainer(context.Context, string, string, string, string) (RemoteUpdated, error) {
	<-b.release
	return RemoteUpdated{}, nil
}

// A remote update announces its start like a local one, and the row is marked
// updating while the update runs, so a dashboard fetching it shows "Updating".
func TestRemoteUpdate_StartIsPublishedAndTheRowIsMarkedWhileItRuns(t *testing.T) {
	cp := &blockingUpdateCluster{
		mockClusterProviderWithContainers: mockClusterProviderWithContainers{
			hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"},
			containers: []RemoteContainer{{Name: "web", Image: "nginx:1", HostID: "h1", HostName: "Agent One"}},
		},
		release: make(chan struct{}),
	}
	srv := remoteUpdateServer(cp, &recordingHistory{})
	srv.remoteHold = 10 * time.Millisecond
	ch, cancel := srv.deps.EventBus.Subscribe()
	defer cancel()

	r := httptest.NewRequest(http.MethodPost, "/api/containers/web/update?host=h1", nil)
	r.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	srv.apiUpdate(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	select {
	case e := <-ch:
		if e.Type != events.EventContainerUpdate || e.ContainerName != "web" || e.HostID != "h1" || e.Message != "update started" {
			t.Fatalf("first event = %+v, want container_update \"update started\" for h1/web", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no start event")
	}

	// The mark is set while the update runs (the order of mark and event is
	// not observable from here).
	if !srv.isRemoteUpdating("h1", "web") {
		t.Error("the row is not marked updating while the update runs")
	}

	close(cp.release)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == events.EventContainerState && e.ContainerName == "web" && e.HostID == "h1" {
				if srv.isRemoteUpdating("h1", "web") {
					t.Error("the row was refreshed while still marked updating")
				}
				return
			}
		case <-deadline:
			t.Fatal("no state event after the update")
		}
	}
}
