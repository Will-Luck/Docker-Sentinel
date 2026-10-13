package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

type recordingHistory struct {
	nopHistory
	mu   sync.Mutex
	recs []UpdateRecord
}

func (h *recordingHistory) RecordUpdate(r UpdateRecord) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, r)
	return nil
}

func (h *recordingHistory) wait(t *testing.T) UpdateRecord {
	t.Helper()
	for i := 0; i < 100; i++ {
		h.mu.Lock()
		if len(h.recs) > 0 {
			r := h.recs[0]
			h.mu.Unlock()
			return r
		}
		h.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no history row was recorded")
	return UpdateRecord{}
}

type failingUpdateCluster struct {
	mockClusterProviderWithContainers
	err error
}

func (f *failingUpdateCluster) UpdateRemoteContainer(context.Context, string, string, string, string) error {
	return f.err
}

func remoteUpdateServer(cp ClusterProvider, hist HistoryStore) *Server {
	cc := NewClusterController()
	cc.SetProvider(cp)
	return &Server{deps: Dependencies{
		Cluster:  cc,
		Queue:    &mockQueue{},
		Store:    hist,
		Docker:   &mockContainerLister{},
		EventBus: events.New(),
		Log:      slog.Default(),
	}}
}

// "Update now" on a remote container leaves a history row under the scoped
// key, like an approved remote update.
func TestApiUpdate_RemoteWritesHistory(t *testing.T) {
	remotes := []RemoteContainer{{Name: "whoami", Image: "traefik/whoami:v1", HostID: "h1", HostName: "Agent One"}}
	for _, tc := range []struct {
		name        string
		err         error
		wantOutcome string
	}{
		{"success", nil, "success"},
		{"failure", errors.New("pull failed"), "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hist := &recordingHistory{}
			cp := &failingUpdateCluster{
				mockClusterProviderWithContainers: mockClusterProviderWithContainers{
					hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"}, containers: remotes,
				},
				err: tc.err,
			}
			srv := remoteUpdateServer(cp, hist)
			r := httptest.NewRequest(http.MethodPost, "/api/containers/whoami/update?host=h1", nil)
			r.SetPathValue("name", "whoami")
			w := httptest.NewRecorder()
			srv.apiUpdate(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			rec := hist.wait(t)
			if rec.ContainerName != "h1::whoami" || rec.HostID != "h1" || rec.HostName != "Agent One" || rec.Outcome != tc.wantOutcome {
				t.Errorf("history row = %+v, want h1::whoami on Agent One with outcome %s", rec, tc.wantOutcome)
			}
			if tc.err != nil && rec.Error == "" {
				t.Errorf("failed row has no error text")
			}
		})
	}
}
