package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// scopeRegistry serves a fixed tag list through the checker's own scope rule,
// as the production adapter does. ListVersions is the unscoped list the
// versions API used to return.
type scopeRegistry struct{ tags []string }

func (r scopeRegistry) newer(image string, scope, def docker.SemverScope) ([]string, int) {
	newer, beyond := registry.NewerVersionsScopedWithBeyond(registry.ExtractTag(image), r.tags, scope, def)
	out := make([]string, len(newer))
	for i, sv := range newer {
		out[i] = sv.Raw
	}
	return out, beyond
}

func (r scopeRegistry) ListVersions(_ context.Context, image string) ([]string, error) {
	v, _ := r.newer(image, docker.ScopeDefault, docker.ScopeDefault)
	return v, nil
}

func (r scopeRegistry) VersionsWithScopeHint(_ context.Context, image, scope, def string) ([]string, int, error) {
	parse := func(s string) docker.SemverScope {
		for _, c := range []docker.SemverScope{docker.ScopeStrict, docker.ScopePatch, docker.ScopeMinor, docker.ScopeMajor} {
			if s == string(c) {
				return c
			}
		}
		return docker.ScopeDefault
	}
	v, beyond := r.newer(image, parse(scope), parse(def))
	return v, beyond, nil
}

// versionsServer serves one container, example.com/app:1.2.3 with labels,
// under the global Version Scope global.
func versionsServer(global string, labels map[string]string) *Server {
	ss := newMockSettingsStore()
	if global != "" {
		ss.data[store.SettingVersionScope] = global
	}
	return &Server{deps: Dependencies{
		SettingsStore: ss,
		Registry:      scopeRegistry{tags: []string{"1.2.3", "1.2.4", "1.2.5", "1.3.0", "2.0.0"}},
		Docker: &mockContainerLister{containers: []ContainerSummary{
			{ID: "abc", Names: []string{"/app"}, Image: "example.com/app:1.2.3", State: "running", Labels: labels},
		}},
		Store: newMockHistoryStore(),
		Queue: &mockQueue{},
		Log:   nopLogger(),
	}}
}

func versionsAPI(t *testing.T, global string, labels map[string]string) []string {
	t.Helper()
	srv := versionsServer(global, labels)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/app/versions", nil)
	r.SetPathValue("name", "app")
	srv.apiContainerVersions(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got []string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// versionsPage renders the container page for the same container and returns
// the versions its version picker offers.
func versionsPage(t *testing.T, global string, labels map[string]string) []string {
	t.Helper()
	srv := versionsServer(global, labels)
	srv.parseTemplates()
	out := containerPage(t, srv, "app", "/container/app")
	picker := regexp.MustCompile(`(?s)<select id="version-select"[^>]*>(.*?)</select>`).FindStringSubmatch(out)
	if picker == nil {
		return []string{}
	}
	got := []string{}
	for _, m := range regexp.MustCompile(`<option value="([^"]*)">`).FindAllStringSubmatch(picker[1], -1) {
		got = append(got, m[1])
	}
	return got
}

// The versions API applies the Version Scope the container page and the
// update checker apply, instead of listing every newer tag.
func TestApiContainerVersions_AppliesVersionScope(t *testing.T) {
	strict := versionsAPI(t, "strict", nil)
	relaxed := versionsAPI(t, "default", nil)
	unscoped, _ := scopeRegistry{tags: []string{"1.2.3", "1.2.4", "1.2.5", "1.3.0", "2.0.0"}}.ListVersions(context.Background(), "example.com/app:1.2.3")

	// The fixture must be able to tell the scopes apart: a 3-part current tag
	// sees nothing under Strict and only patch releases under Relaxed, while
	// the old unscoped list was never empty.
	if len(unscoped) == 0 || len(relaxed) == 0 {
		t.Fatalf("fixture offers nothing unscoped %v or relaxed %v", unscoped, relaxed)
	}
	if len(strict) != 0 {
		t.Errorf("strict scope offered %v for a 3-part tag, want none", strict)
	}
	if want := []string{"1.2.5", "1.2.4"}; !reflect.DeepEqual(relaxed, want) {
		t.Errorf("relaxed scope offered %v, want %v (patch releases only)", relaxed, want)
	}

}

// The versions API offers what the container page's version picker offers for
// the same container, under each scope. The major label case shows the page
// can offer versions at all.
func TestApiContainerVersions_EqualsContainerPage(t *testing.T) {
	for _, tc := range []struct {
		name, global string
		labels       map[string]string
	}{
		{"strict", "strict", nil},
		{"relaxed", "default", nil},
		{"major label", "strict", map[string]string{"sentinel.semver": "major"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := versionsAPI(t, tc.global, tc.labels)
			page := versionsPage(t, tc.global, tc.labels)
			if !reflect.DeepEqual(page, api) {
				t.Errorf("container page offers %v, versions API %v", page, api)
			}
			if tc.name == "major label" && len(page) != 4 {
				t.Errorf("container page offers %v under the major label, want four versions", page)
			}
		})
	}
}

// A per-container sentinel.semver label wins over the global scope.
func TestApiContainerVersions_ContainerLabelOverridesGlobal(t *testing.T) {
	got := versionsAPI(t, "strict", map[string]string{"sentinel.semver": "major"})
	if want := []string{"2.0.0", "1.3.0", "1.2.5", "1.2.4"}; !reflect.DeepEqual(got, want) {
		t.Errorf("container scope major under a strict global offered %v, want %v", got, want)
	}
}
