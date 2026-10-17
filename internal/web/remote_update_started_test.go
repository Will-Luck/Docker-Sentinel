package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// The hub announces every remote update it sends, with its result, whoever
// started it. The dashboard and the API only mark the row updating while
// the update runs: an event of their own as well would give every open
// browser two "update started" toasts for one update.
func TestRemoteUpdate_TheRowIsMarkedWhileItRunsAndOnlyTheHubAnnouncesIt(t *testing.T) {
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

	if !srv.isRemoteUpdating("h1", "web") {
		t.Error("the row is not marked updating while the update runs")
	}

	close(cp.release)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == events.EventContainerUpdate {
				t.Errorf("event %+v from the dashboard: the hub announces remote updates", e)
			}
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

// A remote row shows "Updating" while the hub waits for its agent to update
// or roll it back, whoever started it: a scan (auto policy), a rollback, or
// another hub client. The dashboard marks only the updates it starts.
func TestContainerRow_RemoteRowUpdatingWhileTheHubHasItInFlight(t *testing.T) {
	cp := &mockClusterProviderWithContainers{
		hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"},
		containers: []RemoteContainer{{Name: "web", Image: "nginx:1", State: "running", HostID: "h1", HostName: "Agent One"}},
	}
	srv := remoteUpdateServer(cp, &recordingHistory{})
	srv.deps.Policy = newMockPolicyStore()
	srv.parseTemplates()
	row := func() string {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/containers/web/row?host=h1", nil)
		r.SetPathValue("name", "web")
		w := httptest.NewRecorder()
		srv.handleContainerRow(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("row status = %d: %s", w.Code, w.Body.String())
		}
		var body struct{ HTML string }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.HTML
	}

	if strings.Contains(row(), "badge-updating") {
		t.Fatal("the row shows Updating with no update in flight")
	}
	cp.inFlight = map[string]bool{"h1::web": true}
	if !strings.Contains(row(), "badge-updating") {
		t.Error("the row does not show Updating while the hub has its update in flight")
	}
}
