package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// remote-orchestration-20: after an approved or update-to-version remote
// update, the "Updating" flag was cleared without telling the browser, so
// the row stayed on "Updating". A state event for the row follows.
func TestRemoteUpdate_RowIsRefreshedAfterTheUpdate(t *testing.T) {
	newServer := func() (*Server, <-chan events.SSEEvent, func()) {
		cp := &failingUpdateCluster{mockClusterProviderWithContainers: mockClusterProviderWithContainers{
			hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"},
			containers: []RemoteContainer{{Name: "web", Image: "nginx:1", HostID: "h1", HostName: "Agent One"}},
		}}
		srv := remoteUpdateServer(cp, &recordingHistory{})
		srv.remoteHold = 10 * time.Millisecond
		ch, cancel := srv.deps.EventBus.Subscribe()
		return srv, ch, cancel
	}
	waitState := func(t *testing.T, srv *Server, ch <-chan events.SSEEvent) {
		t.Helper()
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
				t.Fatal("no state event for the row after the update")
			}
		}
	}

	t.Run("approve", func(t *testing.T) {
		srv, ch, cancel := newServer()
		defer cancel()
		srv.deps.Queue = &approvingQueue{items: []PendingUpdate{{HostID: "h1", HostName: "Agent One", ContainerName: "web", CurrentImage: "nginx:1"}}}
		r := httptest.NewRequest(http.MethodPost, "/api/approve/h1::web", nil)
		r.SetPathValue("key", "h1::web")
		srv.apiApprove(httptest.NewRecorder(), r)
		waitState(t, srv, ch)
	})
	t.Run("update to version", func(t *testing.T) {
		srv, ch, cancel := newServer()
		defer cancel()
		r := httptest.NewRequest(http.MethodPost, "/api/containers/web/update-to-version?host=h1", strings.NewReader(`{"tag":"2"}`))
		r.SetPathValue("name", "web")
		w := httptest.NewRecorder()
		srv.apiUpdateToVersion(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		waitState(t, srv, ch)
	})
	t.Run("ghcr switch", func(t *testing.T) {
		srv, ch, cancel := newServer()
		defer cancel()
		srv.deps.GHCRCache = fixedGHCR{}
		r := httptest.NewRequest(http.MethodPost, "/api/containers/web/switch-ghcr?host=h1", nil)
		r.SetPathValue("name", "web")
		w := httptest.NewRecorder()
		srv.apiSwitchToGHCR(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		waitState(t, srv, ch)
	})
}

// insertingHistory is a history store with a conditional insert, holding
// rows by time as the real one does.
type insertingHistory struct {
	nopHistory
	mu   sync.Mutex
	rows map[time.Time]UpdateRecord
}

func (h *insertingHistory) RecordUpdate(r UpdateRecord) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rows[r.Timestamp.UTC()] = r
	return nil
}

func (h *insertingHistory) RecordUpdateIfAbsent(r UpdateRecord) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.rows[r.Timestamp.UTC()]; ok {
		return false, nil
	}
	h.rows[r.Timestamp.UTC()] = r
	return true, nil
}

// The failure row of an unanswered remote update must not overwrite the
// agent's result when that arrived first at the same time key: on the
// approve path and on "Update now".
func TestRemoteUpdate_UnansweredFailureKeepsAnEarlierResult(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	unanswered := &cluster.UnansweredError{HostID: "h1", RequestID: "r1", RecordAt: at, Err: errors.New("agent disconnected")}
	for _, path := range []string{"approve", "update now"} {
		t.Run(path, func(t *testing.T) {
			hist := &insertingHistory{rows: map[time.Time]UpdateRecord{at: {Timestamp: at, ContainerName: "h1::web", Outcome: "success"}}}
			cp := &failingUpdateCluster{mockClusterProviderWithContainers: mockClusterProviderWithContainers{
				hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"},
				containers: []RemoteContainer{{Name: "web", Image: "nginx:1", HostID: "h1", HostName: "Agent One"}},
			}, err: unanswered}
			srv := remoteUpdateServer(cp, hist)
			if path == "approve" {
				srv.deps.Queue = &approvingQueue{items: []PendingUpdate{{HostID: "h1", HostName: "Agent One", ContainerName: "web", CurrentImage: "nginx:1"}}}
				r := httptest.NewRequest(http.MethodPost, "/api/approve/h1::web", nil)
				r.SetPathValue("key", "h1::web")
				srv.apiApprove(httptest.NewRecorder(), r)
			} else {
				r := httptest.NewRequest(http.MethodPost, "/api/containers/web/update?host=h1", nil)
				r.SetPathValue("name", "web")
				srv.apiUpdate(httptest.NewRecorder(), r)
			}
			time.Sleep(200 * time.Millisecond)
			hist.mu.Lock()
			defer hist.mu.Unlock()
			if len(hist.rows) != 1 || hist.rows[at].Outcome != "success" {
				t.Errorf("history = %+v, want the earlier success kept", hist.rows)
			}
		})
	}
}
