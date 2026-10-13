package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func (f *failingUpdateCluster) UpdateRemoteContainer(context.Context, string, string, string, string) (RemoteUpdated, error) {
	return RemoteUpdated{}, f.err
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

type failingUpdater struct{ err error }

func (f failingUpdater) UpdateContainer(context.Context, string, string, string) error { return f.err }
func (failingUpdater) IsUpdating(string) bool                                          { return false }
func (failingUpdater) IsIdle() bool                                                    { return true }
func (failingUpdater) SelfUpdateQueued() bool                                          { return false }

type fixedGHCR struct{}

func (fixedGHCR) Get(string, string) (*GHCRAlternative, bool) {
	return &GHCRAlternative{GHCRImage: "ghcr.io/acme/whoami", Tag: "v1", Available: true}, true
}
func (fixedGHCR) All() []GHCRAlternative { return nil }

// An update to a chosen version, and a switch to GHCR, on a remote container
// leave a history row like "Update now" does.
func TestRemoteVersionUpdateAndGHCRSwitchWriteHistory(t *testing.T) {
	remotes := []RemoteContainer{{Name: "whoami", Image: "traefik/whoami:v1", HostID: "h1", HostName: "Agent One"}}
	for _, op := range []struct {
		name string
		call func(*Server, http.ResponseWriter, *http.Request)
		body string
		path string
		img  string
	}{
		{"update-to-version", (*Server).apiUpdateToVersion, `{"tag":"v2"}`, "/api/containers/whoami/update-to-version?host=h1", "traefik/whoami:v2"},
		{"ghcr", (*Server).apiSwitchToGHCR, ``, "/api/containers/whoami/switch-ghcr?host=h1", "ghcr.io/acme/whoami:v1"},
	} {
		for _, fail := range []bool{false, true} {
			name := op.name + "/success"
			var upErr error
			if fail {
				name, upErr = op.name+"/failure", errors.New("pull failed")
			}
			t.Run(name, func(t *testing.T) {
				hist := &recordingHistory{}
				cp := &failingUpdateCluster{
					mockClusterProviderWithContainers: mockClusterProviderWithContainers{
						hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"}, containers: remotes,
					},
					err: upErr,
				}
				srv := remoteUpdateServer(cp, hist)
				srv.deps.GHCRCache = fixedGHCR{}
				r := httptest.NewRequest(http.MethodPost, op.path, strings.NewReader(op.body))
				r.SetPathValue("name", "whoami")
				w := httptest.NewRecorder()
				op.call(srv, w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("status = %d: %s", w.Code, w.Body.String())
				}
				rec := hist.wait(t)
				want := "success"
				if fail {
					want = "failed"
				}
				if rec.ContainerName != "h1::whoami" || rec.HostID != "h1" || rec.HostName != "Agent One" || rec.Outcome != want || rec.NewImage != op.img {
					t.Errorf("row = %+v, want h1::whoami on Agent One, outcome %s, new image %s", rec, want, op.img)
				}
			})
		}
	}
}

// Local updates record their own history inside the engine, for every failure
// (validation, start, verify refusal, hook abort, pull). The web layer records
// only remote updates, so a failed local update never gets a second row from
// any entry point, whatever the error.
func TestLocalUpdateEntryPoints_NeverAddAFailedRow(t *testing.T) {
	errs := []error{
		errors.New("new container failed validation"),
		errors.New("start failed"),
		errors.New("signature verification failed"),
		errors.New("pre-update hook failed"),
		errors.New("pull failed"),
	}
	local := &mockContainerLister{containers: []ContainerSummary{{ID: "c1", Names: []string{"/web"}, Image: "nginx:1", Labels: map[string]string{}}}}
	entries := []struct {
		name string
		run  func(srv *Server)
	}{
		{"approve", func(srv *Server) {
			srv.deps.Queue = &approvingQueue{items: []PendingUpdate{{ContainerID: "c1", ContainerName: "web", CurrentImage: "nginx:1"}}}
			r := httptest.NewRequest(http.MethodPost, "/api/approve/web", nil)
			r.SetPathValue("key", "web")
			srv.apiApprove(httptest.NewRecorder(), r)
		}},
		{"update now", func(srv *Server) {
			r := httptest.NewRequest(http.MethodPost, "/api/update/web", nil)
			r.SetPathValue("name", "web")
			srv.apiUpdate(httptest.NewRecorder(), r)
		}},
		{"update to version", func(srv *Server) {
			r := httptest.NewRequest(http.MethodPost, "/api/containers/web/update-to-version", strings.NewReader(`{"tag":"2"}`))
			r.SetPathValue("name", "web")
			srv.apiUpdateToVersion(httptest.NewRecorder(), r)
		}},
		{"ghcr switch", func(srv *Server) {
			srv.deps.GHCRCache = fixedGHCR{}
			r := httptest.NewRequest(http.MethodPost, "/api/containers/web/switch-ghcr", nil)
			r.SetPathValue("name", "web")
			srv.apiSwitchToGHCR(httptest.NewRecorder(), r)
		}},
	}
	for _, e := range entries {
		for _, upErr := range errs {
			t.Run(e.name+"/"+upErr.Error(), func(t *testing.T) {
				hist := &recordingHistory{}
				srv := &Server{deps: Dependencies{
					Store: hist, Updater: failingUpdater{err: upErr}, Docker: local,
					Queue: &mockQueue{}, EventBus: events.New(), Log: nopLogger(),
				}}
				e.run(srv)
				time.Sleep(150 * time.Millisecond)
				hist.mu.Lock()
				n := len(hist.recs)
				hist.mu.Unlock()
				if n != 0 {
					t.Errorf("the web layer added %d history rows for a failed local update", n)
				}
			})
		}
	}
}

type redirectingCluster struct {
	mockClusterProviderWithContainers
}

func (redirectingCluster) UpdateRemoteContainer(_ context.Context, _, _, target, _ string) (RemoteUpdated, error) {
	return RemoteUpdated{ContainerName: "sentinel-agent", OldImage: "sentinel:3.0.0", NewImage: target}, nil
}

// smoke NEW-1: "Update now" on another Sentinel container of an agent's
// host updates the agent's own container instead. The history row is for
// the container that changed, from the image it ran.
func TestApiUpdate_RemoteRecordsTheContainerThatChanged(t *testing.T) {
	hist := &recordingHistory{}
	cp := &redirectingCluster{mockClusterProviderWithContainers{
		hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"},
		containers: []RemoteContainer{{Name: "other-sentinel", Image: "sentinel:3.0.1", HostID: "h1", HostName: "Agent One"}},
	}}
	srv := remoteUpdateServer(cp, hist)
	r := httptest.NewRequest(http.MethodPost, "/api/containers/other-sentinel/update?host=h1", nil)
	r.SetPathValue("name", "other-sentinel")
	w := httptest.NewRecorder()
	srv.apiUpdate(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	rec := hist.wait(t)
	if rec.ContainerName != "h1::sentinel-agent" || rec.OldImage != "sentinel:3.0.0" || rec.NewImage != "sentinel:3.0.1" {
		t.Errorf("history row = %+v, want sentinel-agent from sentinel:3.0.0 to sentinel:3.0.1", rec)
	}
}

type pendingCluster struct {
	mockClusterProviderWithContainers
	done chan struct{}
}

func (p *pendingCluster) UpdateRemoteContainer(context.Context, string, string, string, string) (RemoteUpdated, error) {
	defer close(p.done)
	return RemoteUpdated{ContainerName: "sentinel-agent", Pending: true}, nil
}

// smoke F2: an agent's update of itself is under way when the agent
// answers (its helper does the swap); the hub records it when the outcome
// is known. The dashboard records no row for it.
func TestApiUpdate_RemoteSelfUpdateUnderWayRecordsNothing(t *testing.T) {
	hist := &recordingHistory{}
	cp := &pendingCluster{mockClusterProviderWithContainers: mockClusterProviderWithContainers{
		hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"},
		containers: []RemoteContainer{{Name: "sentinel-agent", Image: "sentinel:3.0.0", HostID: "h1", HostName: "Agent One"}},
	}, done: make(chan struct{})}
	srv := remoteUpdateServer(cp, hist)
	r := httptest.NewRequest(http.MethodPost, "/api/containers/sentinel-agent/update?host=h1", nil)
	r.SetPathValue("name", "sentinel-agent")
	srv.apiUpdate(httptest.NewRecorder(), r)
	<-cp.done
	time.Sleep(100 * time.Millisecond)
	hist.mu.Lock()
	defer hist.mu.Unlock()
	if len(hist.recs) != 0 {
		t.Errorf("history = %+v, want no row while the update is under way", hist.recs)
	}
}
