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

type approveCluster struct {
	mockClusterProviderWithContainers
}

// For a version bump the queue's RemoteDigest belongs to the old tag, so the
// history must not record it as the new digest. A digest-only update's
// RemoteDigest is the new one.
func TestApiApprove_RemoteHistoryNewDigest(t *testing.T) {
	for _, tc := range []struct {
		name       string
		versions   []string
		wantDigest string
	}{
		{"version bump", []string{"1.0.1"}, ""},
		{"same tag, new digest", nil, "sha256:new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hist := &recordingHistory{}
			cp := &approveCluster{mockClusterProviderWithContainers{
				hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"},
			}}
			srv := remoteUpdateServer(cp, hist)
			srv.deps.Queue = &approvingQueue{items: []PendingUpdate{{
				HostID: "h1", HostName: "Agent One", ContainerName: "web", CurrentImage: "nginx:1.0.0",
				RemoteDigest: "sha256:new", NewerVersions: tc.versions,
			}}}
			r := httptest.NewRequest(http.MethodPost, "/api/approve/h1::web", nil)
			r.SetPathValue("key", "h1::web")
			w := httptest.NewRecorder()
			srv.apiApprove(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			rec := hist.wait(t)
			if rec.Outcome != "success" || rec.NewDigest != tc.wantDigest {
				t.Errorf("history row = outcome %s, new digest %q; want success and %q", rec.Outcome, rec.NewDigest, tc.wantDigest)
			}
		})
	}
}

type listingHistory struct {
	recordingHistory
	existing []UpdateRecord
}

func (h *listingHistory) ListHistoryByContainer(string, int) ([]UpdateRecord, error) {
	return h.existing, nil
}

type failingUpdater struct{ err error }

func (f failingUpdater) UpdateContainer(context.Context, string, string, string) error { return f.err }
func (failingUpdater) IsUpdating(string) bool                                          { return false }
func (failingUpdater) IsIdle() bool                                                    { return true }
func (failingUpdater) SelfUpdateQueued() bool                                          { return false }

// A local update the engine refused (signature check, pre-update hook) is
// already in the history. Approve must not add a second failed row.
func TestApiApprove_LocalFailureNotRecordedTwice(t *testing.T) {
	for _, tc := range []struct {
		name         string
		engineLogged bool
		wantRows     int
	}{{"engine already recorded it", true, 0}, {"engine recorded nothing", false, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			hist := &listingHistory{}
			if tc.engineLogged {
				hist.existing = []UpdateRecord{{ContainerName: "web", Outcome: "failed", Timestamp: time.Now().Add(time.Hour)}}
			}
			srv := &Server{deps: Dependencies{
				Queue:    &approvingQueue{items: []PendingUpdate{{ContainerID: "id", ContainerName: "web", CurrentImage: "nginx:1"}}},
				Store:    hist,
				Updater:  failingUpdater{err: errors.New("signature verification failed")},
				Docker:   &mockContainerLister{},
				EventBus: events.New(),
				Log:      slog.Default(),
			}}
			r := httptest.NewRequest(http.MethodPost, "/api/approve/web", nil)
			r.SetPathValue("key", "web")
			w := httptest.NewRecorder()
			srv.apiApprove(w, r)
			time.Sleep(300 * time.Millisecond)
			hist.mu.Lock()
			n := len(hist.recs)
			hist.mu.Unlock()
			if n != tc.wantRows {
				t.Errorf("web added %d failed rows, want %d", n, tc.wantRows)
			}
		})
	}
}
