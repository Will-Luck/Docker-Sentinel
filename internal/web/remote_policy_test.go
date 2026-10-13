package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// remote-orchestration-14: the dashboard showed remote containers under
// the local default policy, while the scan used the cluster's remote
// default and the :latest rule. It shows what the scan applies.
func TestRemotePolicy_IsTheOneTheScanApplies(t *testing.T) {
	ss := newMockSettingsStore()
	ps := newMockPolicyStore()
	srv := &Server{deps: Dependencies{
		SettingsStore: ss,
		Policy:        ps,
		Config:        &mockConfigReader{defaultPolicy: "manual", latestAutoUpdate: true},
	}}
	pinned := RemoteContainer{Name: "db", Image: "pg:16", HostID: "h1", Labels: map[string]string{"sentinel.policy": "pinned"}}
	latest := RemoteContainer{Name: "web", Image: "nginx:latest", HostID: "h1"}
	tagged := RemoteContainer{Name: "app", Image: "app:1.2", HostID: "h1"}

	if got := srv.remotePolicy(tagged); got != "manual" {
		t.Errorf("no remote default saved: %q, want the global default manual", got)
	}
	if got := srv.remotePolicy(latest); got != "auto" {
		t.Errorf(":latest with latest auto-update on: %q, want auto", got)
	}
	_ = ss.SaveSetting(store.SettingClusterRemotePolicy, "auto")
	if got := srv.remotePolicy(tagged); got != "auto" {
		t.Errorf("remote default auto: %q, want auto", got)
	}
	if got := srv.remotePolicy(pinned); got != "pinned" {
		t.Errorf("label: %q, want pinned", got)
	}
	ps.overrides["h1::app"] = "manual"
	if got := srv.remotePolicy(tagged); got != "manual" {
		t.Errorf("override: %q, want manual", got)
	}
}

// The cluster settings page showed "manual" as the remote default when
// none was saved; the scan then uses the global default policy.
func TestClusterSettings_RemoteDefaultFallsBackToTheGlobalPolicy(t *testing.T) {
	srv := &Server{deps: Dependencies{SettingsStore: newMockSettingsStore(), Config: &mockConfigReader{defaultPolicy: "auto"}}}
	w := httptest.NewRecorder()
	srv.apiClusterSettings(w, httptest.NewRequest("GET", "/api/settings/cluster", nil))
	if !strings.Contains(w.Body.String(), `"remote_policy":"auto"`) {
		t.Errorf("cluster settings = %s, want remote_policy auto (the global default)", w.Body.String())
	}
}

// remoteChecker is a RegistryChecker that also checks a remote container
// against the digest its agent reported.
type remoteCheckerFake struct {
	local          int
	remoteImage    string
	remoteDigest   string
	updateAvail    bool
	newer          []string
	registryDigest string
}

func (f *remoteCheckerFake) CheckForUpdate(context.Context, string) (bool, []string, string, string, error) {
	f.local++
	return false, nil, "", "", errors.New("No such image on the hub")
}

func (f *remoteCheckerFake) CheckRemoteForUpdate(_ context.Context, image, digest string, _ map[string]string) (RemoteCheck, error) {
	f.remoteImage, f.remoteDigest = image, digest
	return RemoteCheck{UpdateAvailable: f.updateAvail, NewerVersions: f.newer, RemoteDigest: f.registryDigest}, nil
}

// remote-orchestration-12 and -13: "Check for updates" on a remote
// container compared the hub's own copy of the image (often missing: a 502)
// instead of the digest its agent reported, and an update with no newer tag
// (a new image behind the same tag) was reported as up to date and not
// queued.
func TestApiCheck_RemoteUsesTheAgentsDigestAndQueuesSameTagUpdates(t *testing.T) {
	q := &mockQueue{}
	rc := RemoteContainer{Name: "web", Image: "nginx:latest", ImageDigest: "nginx@sha256:old", HostID: "h1", HostName: "Agent One"}
	cc := NewClusterController()
	cc.SetProvider(&mockClusterProviderWithContainers{hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"}, containers: []RemoteContainer{rc}})
	checker := &remoteCheckerFake{updateAvail: true, registryDigest: "sha256:new"}
	srv := &Server{deps: Dependencies{Cluster: cc, Queue: q, RegistryChecker: checker, EventBus: events.New(), Log: slog.Default()}}

	r := httptest.NewRequest(http.MethodPost, "/api/check/web?host=h1", nil)
	r.SetPathValue("name", "web")
	w := httptest.NewRecorder()
	srv.apiCheck(w, r)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "update_available") {
		t.Fatalf("check = %d %s, want an update available", w.Code, w.Body.String())
	}
	if checker.local != 0 || checker.remoteDigest != "nginx@sha256:old" {
		t.Errorf("hub-side checks %d, remote digest checked %q; want only the agent's digest checked", checker.local, checker.remoteDigest)
	}
	got, ok := q.Get("h1::web")
	if !ok || got.RemoteDigest != "sha256:new" || got.CurrentDigest != "nginx@sha256:old" {
		t.Errorf("queued %+v (%v), want the same-tag update with its digests", got, ok)
	}
}
