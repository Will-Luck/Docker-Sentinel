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
