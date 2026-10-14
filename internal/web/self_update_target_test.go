package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/selfupdate"
)

// queueOf is an update queue holding pending updates by container name.
type queueOf struct {
	mockUpdateQueue
	pending map[string]PendingUpdate
}

func (q *queueOf) Get(name string) (PendingUpdate, bool) {
	p, ok := q.pending[name]
	return p, ok
}

type recordingSelfUpdater struct{ targets chan string }

func (r *recordingSelfUpdater) Update(_ context.Context, target string) error {
	r.targets <- target
	return nil
}

func twoSentinelServer(t *testing.T, ownID string) (*Server, *recordingSelfUpdater) {
	t.Helper()
	old := selfContainerID
	selfContainerID = func() string { return ownID }
	t.Cleanup(func() { selfContainerID = old })

	self := map[string]string{"sentinel.self": "true"}
	su := &recordingSelfUpdater{targets: make(chan string, 2)}
	return &Server{deps: Dependencies{
		Docker: &mockContainerLister{containers: []ContainerSummary{
			{ID: "aaaaaaaaaaaa1111", Names: []string{"/sentinel-hub-2"}, Image: "img/sentinel:1.0", Labels: self},
			{ID: "bbbbbbbbbbbb2222", Names: []string{"/sentinel-hub"}, Image: "img/sentinel:1.0", Labels: self},
		}},
		Queue: &queueOf{pending: map[string]PendingUpdate{
			"sentinel-hub-2": {NewerVersions: []string{"9.9.9"}},
			"sentinel-hub":   {NewerVersions: []string{"3.0.0"}},
		}},
		SelfUpdater: su,
		EventBus:    events.New(),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}, su
}

// The dashboard's self-update targets the version queued for this Sentinel's
// own container, not for the first Sentinel in the list.
func TestApiSelfUpdateUsesTheOwnContainerNotTheFirstSentinel(t *testing.T) {
	srv, su := twoSentinelServer(t, "bbbbbbbbbbbb")
	w := httptest.NewRecorder()
	srv.apiSelfUpdate(w, httptest.NewRequest(http.MethodPost, "/api/self-update", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	select {
	case target := <-su.targets:
		if target != "img/sentinel:3.0.0" {
			t.Errorf("target = %q, want img/sentinel:3.0.0 (the queued version of sentinel-hub)", target)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no self-update started")
	}
}

func TestApiSelfUpdateRefusesWhenItCannotTellWhichContainerItIs(t *testing.T) {
	for _, id := range []string{"", "ffffffffffff"} {
		srv, su := twoSentinelServer(t, id)
		w := httptest.NewRecorder()
		srv.apiSelfUpdate(w, httptest.NewRequest(http.MethodPost, "/api/self-update", nil))
		if w.Code == http.StatusOK {
			t.Errorf("id %q: started a self-update by guessing", id)
		}
		select {
		case target := <-su.targets:
			t.Errorf("id %q: self-update started for %q", id, target)
		case <-time.After(50 * time.Millisecond):
		}
		if !strings.Contains(w.Body.String(), "could not determine") {
			t.Errorf("id %q: body = %s", id, w.Body)
		}
	}
}

func updateToVersion(srv *Server, name string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/containers/"+name+"/update-to-version", strings.NewReader(`{"tag":"3.0.0"}`))
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	srv.apiUpdateToVersion(w, r)
	return w
}

// Choosing "update to version" on another Sentinel container must not
// self-update this one in its place; choosing this one does.
func TestApiUpdateToVersionSelfUpdatesOnlyTheOwnContainer(t *testing.T) {
	srv, su := twoSentinelServer(t, "bbbbbbbbbbbb")

	w := updateToVersion(srv, "sentinel-hub-2")
	if w.Code == http.StatusOK {
		t.Errorf("another Sentinel was accepted: %s", w.Body)
	}
	select {
	case target := <-su.targets:
		t.Fatalf("self-update started for %q on a request for another Sentinel", target)
	case <-time.After(50 * time.Millisecond):
	}

	w = updateToVersion(srv, "sentinel-hub")
	if w.Code != http.StatusOK {
		t.Fatalf("own container: status %d: %s", w.Code, w.Body)
	}
	select {
	case target := <-su.targets:
		if target != "img/sentinel:3.0.0" {
			t.Errorf("target = %q", target)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no self-update started for the own container")
	}
}

// SENTINEL_SELF_CONTAINER tells the dashboard which container it is when the
// container ID cannot be read; it must be a Sentinel.
func TestApiSelfUpdateHonoursSelfContainerOverride(t *testing.T) {
	t.Setenv(selfupdate.SelfContainerEnv, "sentinel-hub")
	srv, su := twoSentinelServer(t, "sentinel-hub")
	w := httptest.NewRecorder()
	srv.apiSelfUpdate(w, httptest.NewRequest(http.MethodPost, "/api/self-update", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	if target := <-su.targets; target != "img/sentinel:3.0.0" {
		t.Errorf("target = %q", target)
	}

	t.Setenv(selfupdate.SelfContainerEnv, "no-such")
	srv, su = twoSentinelServer(t, "no-such")
	w = httptest.NewRecorder()
	srv.apiSelfUpdate(w, httptest.NewRequest(http.MethodPost, "/api/self-update", nil))
	if w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "SENTINEL_SELF_CONTAINER") {
		t.Errorf("a bad override was accepted or the error does not name the variable: %d %s", w.Code, w.Body)
	}
	select {
	case target := <-su.targets:
		t.Errorf("self-update started for %q", target)
	case <-time.After(50 * time.Millisecond):
	}
}
