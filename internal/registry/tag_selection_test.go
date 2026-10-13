package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
)

// redirectTransport sends every request to the test server, keeping the path.
type redirectTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (rt redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme = rt.target.Scheme
	r.URL.Host = rt.target.Host
	r.Host = rt.target.Host
	return rt.inner.RoundTrip(r)
}

func withFakeRegistry(t *testing.T, h http.Handler) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	orig := httpClient
	c := srv.Client()
	c.Transport = redirectTransport{target: u, inner: c.Transport}
	httpClient = c
	t.Cleanup(func() { httpClient = orig })
}

// A container on a mutable tag must keep following that tag: the checker may
// report which version the tag now points at, but must not offer it as a
// replacement tag, because callers feed NewerVersions[0] into the image ref.
func TestCheckVersioned_MutableTagKeepsFollowingTag(t *testing.T) {
	for _, tag := range []string{"latest", "alpine"} {
		t.Run(tag, func(t *testing.T) {
			withFakeRegistry(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/token":
					_ = json.NewEncoder(w).Encode(map[string]string{"token": "t"})
				case strings.HasSuffix(r.URL.Path, "/tags/list"):
					_ = json.NewEncoder(w).Encode(map[string][]string{"tags": {"latest", "alpine", "1.29.1", "1.29.2"}})
				case strings.HasSuffix(r.URL.Path, "/manifests/1.29.2"):
					w.Header().Set("Docker-Content-Digest", "sha256:remote")
				case strings.HasSuffix(r.URL.Path, "/manifests/1.29.1"):
					w.Header().Set("Docker-Content-Digest", "sha256:local")
				default:
					http.NotFound(w, r)
				}
			}))

			ref := "nginx:" + tag
			mock := newMockRegistry()
			mock.imageDigests[ref] = "docker.io/library/nginx@sha256:local"
			mock.distributionDigests[ref] = "sha256:remote"
			c := NewChecker(mock, logging.New(false))

			res := c.CheckVersioned(context.Background(), ref, docker.ScopeDefault, "", "")
			if !res.UpdateAvailable {
				t.Fatal("precondition: expected a digest update")
			}
			if len(res.NewerVersions) > 0 {
				t.Errorf("%s got NewerVersions=%v; the engine would recreate it as a pinned tag", ref, res.NewerVersions)
			}
			if res.ResolvedTargetVersion != "1.29.2" {
				t.Errorf("ResolvedTargetVersion = %q, want 1.29.2 (display only)", res.ResolvedTargetVersion)
			}
		})
	}
}

func TestSemVerLessThan_PrereleaseNumbersCompareNumerically(t *testing.T) {
	tests := []struct {
		lo, hi string
	}{
		{"2.0.0-rc9", "2.0.0-rc10"},
		{"2.0.0-beta.2", "2.0.0-beta.10"},
		{"2.0.0-alpha", "2.0.0-beta"},
		{"2.0.0-rc1", "2.0.0-rc.2"},
		{"2.0.0-rc", "2.0.0-rc1"},
		{"2.0.0-rc10", "2.0.0"},
	}
	for _, tt := range tests {
		lo, ok1 := ParseSemVer(tt.lo)
		hi, ok2 := ParseSemVer(tt.hi)
		if !ok1 || !ok2 {
			t.Fatalf("parse failed for %q / %q", tt.lo, tt.hi)
		}
		if !lo.LessThan(hi) {
			t.Errorf("%s < %s reported false", tt.lo, tt.hi)
		}
		if hi.LessThan(lo) {
			t.Errorf("%s < %s reported true", tt.hi, tt.lo)
		}
	}

	got := rawVersions(NewerVersionsScoped("2.0.0-rc8", []string{"2.0.0-rc9", "2.0.0-rc10"}, docker.ScopeMajor, docker.ScopeDefault))
	if strings.Join(got, ",") != "2.0.0-rc10,2.0.0-rc9" {
		t.Errorf("newest-first order = %v, want rc10 before rc9", got)
	}
}

func TestNewerVersionsScoped_StableCurrentSkipsPrereleases(t *testing.T) {
	tests := []struct {
		name    string
		current string
		tags    []string
		scope   docker.SemverScope
		want    []string
	}{
		{"3-part stable", "1.2.3", []string{"1.2.4-rc1", "1.2.4"}, docker.ScopeDefault, []string{"1.2.4"}},
		{"3-part stable, only a pre-release", "1.2.3", []string{"1.2.4-rc1"}, docker.ScopeDefault, nil},
		{"2-part stable", "1.24", []string{"1.25.0-rc0", "1.25.0-beta2"}, docker.ScopeDefault, nil},
		{"every pre-release word", "1.0.0", []string{"2.0.0-alpha", "2.0.0-beta", "2.0.0-pre1", "2.0.0-dev", "2.0.0-snapshot", "2.0.0-nightly", "2.0.0-preview1"}, docker.ScopeMajor, nil},
		{"pre-release current still sees later pre-releases and the release", "2.0.0-rc1", []string{"2.0.0-rc2", "2.0.0"}, docker.ScopePatch, []string{"2.0.0", "2.0.0-rc2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rawVersions(NewerVersionsScoped(tt.current, tt.tags, tt.scope, docker.ScopeDefault))
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
