package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

type fixedChecker struct {
	update   bool
	versions []string
	target   string
}

func (f fixedChecker) CheckForUpdate(context.Context, string) (bool, []string, string, string, error) {
	return f.update, f.versions, "", f.target, nil
}

func localCheck(t *testing.T, chk fixedChecker, ig IgnoredVersionStore) (map[string]any, *mockQueue) {
	t.Helper()
	q := &mockQueue{}
	srv := &Server{deps: Dependencies{
		RegistryChecker: chk, Queue: q, IgnoredVersions: ig,
		Docker:   &mockContainerLister{containers: []ContainerSummary{{ID: "c1", Names: []string{"/web"}, Image: "nginx:latest"}}},
		EventBus: events.New(), Log: nopLogger(),
	}}
	r := httptest.NewRequest(http.MethodPost, "/api/check/web", nil)
	r.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	srv.apiCheck(w, r)
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	return got, q
}

// A new image behind the same tag has no newer version to list. The manual
// check queues it, as the scan does, instead of calling it ignored.
func TestApiCheck_LocalDigestOnlyUpdateIsQueued(t *testing.T) {
	got, q := localCheck(t, fixedChecker{update: true}, nil)
	if got["status"] != "update_available" || len(q.items) != 1 {
		t.Errorf("status %v, queued %d: a same-tag update was not queued", got["status"], len(q.items))
	}
}

func TestApiCheck_LocalIgnoredKeysStillHold(t *testing.T) {
	ig := &recordingIgnores{added: map[string][]string{"web": {"1.29.2", "1.30.0"}}}
	ig.get = true
	// A resolved same-tag target that was ignored is up to date.
	if got, q := localCheck(t, fixedChecker{update: true, target: "1.29.2"}, ig); got["status"] != "up_to_date" || len(q.items) != 0 {
		t.Errorf("ignored resolved target: %v, queued %d", got["status"], len(q.items))
	}
	// Listed versions are filtered; one left is queued.
	if got, q := localCheck(t, fixedChecker{update: true, versions: []string{"1.30.0", "1.31.0"}}, ig); got["status"] != "update_available" || len(q.items) != 1 || len(q.items[0].NewerVersions) != 1 {
		t.Errorf("filtered versions: %v, queue %+v", got["status"], q.items)
	}
	// All listed versions ignored: up to date.
	if got, _ := localCheck(t, fixedChecker{update: true, versions: []string{"1.30.0"}}, ig); got["status"] != "up_to_date" {
		t.Errorf("all ignored: %v", got["status"])
	}
}
