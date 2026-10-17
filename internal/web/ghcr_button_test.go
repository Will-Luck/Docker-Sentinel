package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

type fakeGHCRCache struct{ alts map[string]GHCRAlternative }

func (f fakeGHCRCache) Get(repo, tag string) (*GHCRAlternative, bool) {
	a, ok := f.alts[repo+":"+tag]
	return &a, ok
}
func (f fakeGHCRCache) All() []GHCRAlternative { return nil }

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestGHCRTarget(t *testing.T) {
	srv := &Server{deps: Dependencies{GHCRCache: fakeGHCRCache{alts: map[string]GHCRAlternative{
		"acme/app:1.0":       {GHCRImage: "ghcr.io/acme/app", Tag: "1.0", Available: true},
		"acme/gone:latest":   {GHCRImage: "ghcr.io/acme/gone", Tag: "latest", Available: false},
		"acme/pinned:latest": {GHCRImage: "ghcr.io/acme/pinned", Tag: "latest", Available: true},
	}}}}
	tests := []struct{ image, want string }{
		{"acme/app:1.0", "ghcr.io/acme/app:1.0"},
		{"docker.io/acme/app:1.0", "ghcr.io/acme/app:1.0"},
		{"acme/gone", ""},                         // probed, no alternative
		{"acme/unchecked:2", ""},                  // not probed yet
		{"ghcr.io/acme/app:1.0", ""},              // already on GHCR: no switch to itself
		{"registry.example.com/acme/app:1.0", ""}, // another registry
		{"acme/pinned", "ghcr.io/acme/pinned:latest"},
		// Pinned by digest: :latest on GHCR would be another version.
		{"acme/pinned@" + testDigest, ""},
		{"acme/pinned:1.0@" + testDigest, ""},
	}
	for _, tt := range tests {
		if got := srv.ghcrTarget(tt.image); got != tt.want {
			t.Errorf("ghcrTarget(%q) = %q, want %q", tt.image, got, tt.want)
		}
	}
	if got := (&Server{}).ghcrTarget("acme/app:1.0"); got != "" {
		t.Errorf("without a cache ghcrTarget = %q, want empty", got)
	}
}

// The Switch to GHCR button is on the container page only where an
// alternative exists, and it names the image and the host it acts on.
func TestContainerPage_SwitchToGHCRButton(t *testing.T) {
	render := func(ghcr string) string {
		s := &Server{}
		s.parseTemplates()
		var sb strings.Builder
		data := containerDetailData{Container: containerView{Name: "app", Image: "acme/app:1.0", State: "running", HostID: "h1"}, GHCRImage: ghcr}
		if err := s.tmpl.ExecuteTemplate(&sb, "container.html", data); err != nil {
			t.Fatal(err)
		}
		return sb.String()
	}
	with := render("ghcr.io/acme/app:1.0")
	// html/template escapes the slashes of a JS string argument; the browser
	// reads them back as plain slashes.
	if !strings.Contains(with, `switchToGHCR('app', 'ghcr.io\/acme\/app:1.0', 'h1')`) {
		if i := strings.Index(with, "switchToGHCR("); i >= 0 {
			t.Errorf("button has the wrong arguments: %q", with[i:min(i+80, len(with))])
		} else {
			t.Errorf("button missing")
		}
	}
	if strings.Contains(render(""), "switchToGHCR") {
		t.Errorf("button shown without an alternative")
	}
}

// containerPage renders the container page at path through its handler.
func containerPage(t *testing.T, srv *Server, name, path string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	srv.handleContainerDetail(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, w.Code)
	}
	return w.Body.String()
}

// The button follows the switch endpoint's rule for Sentinel itself
// (isProtectedTarget): only the hub's own container is refused, while a
// Sentinel agent on another host is switched like any remote container.
func TestContainerPage_GHCRButtonFollowsTheEndpointsSelfRule(t *testing.T) {
	self := map[string]string{"sentinel.self": "true"}
	cc := NewClusterController()
	cc.SetProvider(&mockClusterProviderWithContainers{
		hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"},
		containers: []RemoteContainer{{Name: "sentinel", Image: "acme/sentinel:3.0", HostID: "h1", HostName: "Agent One", State: "running", Labels: self}},
	})
	srv := &Server{deps: Dependencies{
		Cluster: cc, Queue: &mockQueue{}, Store: newMockHistoryStore(), Log: nopLogger(),
		Docker: &mockContainerLister{containers: []ContainerSummary{
			{ID: "own", Names: []string{"/sentinel"}, Image: "acme/sentinel:3.0", State: "running", Labels: self},
		}},
		GHCRCache: fakeGHCRCache{alts: map[string]GHCRAlternative{
			"acme/sentinel:3.0": {GHCRImage: "ghcr.io/acme/sentinel", Tag: "3.0", Available: true},
		}},
	}}
	srv.parseTemplates()

	if out := containerPage(t, srv, "sentinel", "/container/sentinel?host=h1"); !strings.Contains(out, "switchToGHCR(") {
		t.Errorf("a Sentinel agent has no Switch to GHCR button, though the endpoint switches it")
	}
	if out := containerPage(t, srv, "sentinel", "/container/sentinel"); strings.Contains(out, "switchToGHCR(") {
		t.Errorf("the hub's own container has a Switch to GHCR button, though the endpoint refuses it")
	}
}

// switch-ghcr refuses a container pinned by digest, local or remote, even when
// its repository's :latest has a GHCR copy: switching would change its version.
func TestSwitchToGHCR_RefusesDigestPinned(t *testing.T) {
	cache := fakeGHCRCache{alts: map[string]GHCRAlternative{
		"acme/web:latest": {GHCRImage: "ghcr.io/acme/web", Tag: "latest", Available: true},
	}}
	for _, image := range []string{"acme/web@" + testDigest, "acme/web:1.0@" + testDigest} {
		t.Run("remote "+image[:12], func(t *testing.T) {
			srv := remoteUpdateServer(&mockClusterProviderWithContainers{
				hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"},
				containers: []RemoteContainer{{Name: "web", Image: image, HostID: "h1", HostName: "Agent One"}},
			}, &recordingHistory{})
			srv.deps.GHCRCache = cache
			r := httptest.NewRequest(http.MethodPost, "/api/containers/web/switch-ghcr?host=h1", nil)
			r.SetPathValue("name", "web")
			w := httptest.NewRecorder()
			srv.apiSwitchToGHCR(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status %d (%s), want 400", w.Code, w.Body.String())
			}
		})
		t.Run("local "+image[:12], func(t *testing.T) {
			srv := &Server{deps: Dependencies{
				GHCRCache: cache, Updater: failingUpdater{}, Store: &recordingHistory{},
				Docker:   &mockContainerLister{containers: []ContainerSummary{{ID: "c1", Names: []string{"/web"}, Image: image}}},
				Queue:    &mockQueue{},
				EventBus: events.New(),
				Log:      nopLogger(),
			}}
			r := httptest.NewRequest(http.MethodPost, "/api/containers/web/switch-ghcr", nil)
			r.SetPathValue("name", "web")
			w := httptest.NewRecorder()
			srv.apiSwitchToGHCR(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status %d (%s), want 400", w.Code, w.Body.String())
			}
		})
	}
}
