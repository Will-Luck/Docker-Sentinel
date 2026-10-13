package registry

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestNewerVersionsScoped_BuildCounters(t *testing.T) {
	tests := []struct {
		name    string
		current string
		tags    []string
		want    []string
	}{
		{"linuxserver counter bump", "4.3.3-ls190", []string{"4.3.3-ls189", "4.3.3-ls191", "4.4.0-ls200"}, []string{"4.4.0-ls200", "4.3.3-ls191"}},
		{"bitnami revision bump", "7.2.4-debian-12-r9", []string{"7.2.4-debian-12-r10", "7.2.5-debian-12-r0", "7.2.5-debian-11-r3"}, []string{"7.2.5-debian-12-r0", "7.2.4-debian-12-r10"}},
		{"alpine revision then counter", "1.26.2-r1-ls300", []string{"1.26.2-r1-ls301", "1.26.3-r0-ls302", "1.26.3"}, []string{"1.26.3-r0-ls302", "1.26.2-r1-ls301"}},
		{"counter tags never see bare tags", "4.3.3-ls190", []string{"4.5.0", "4.5.0-rc1"}, nil},
		{"older counter is not newer", "4.3.3-ls190", []string{"4.3.3-ls9", "4.3.2-ls500"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rawVersions(NewerVersionsScoped(tt.current, tt.tags, docker.ScopeMajor, docker.ScopeDefault))
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// Pagination follows the Link header, so a registry whose pages hold fewer than
// 1000 tags is read to the end.
func TestListTags_FollowsLinkHeader(t *testing.T) {
	withFakeRegistry(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tags/list") {
			http.NotFound(w, r)
			return
		}
		var tags []string
		if r.URL.Query().Get("last") == "" {
			for i := 0; i < 100; i++ {
				tags = append(tags, fmt.Sprintf("0.0.%d", i))
			}
			w.Header().Set("Link", `</v2/org/app/tags/list?last=0.0.99&n=100>; rel="next"`)
		} else {
			tags = []string{"9.9.9"}
		}
		_ = json.NewEncoder(w).Encode(map[string][]string{"tags": tags})
	}))
	res, err := ListTags(context.Background(), "quay.example/org/app:0.0.1", "", "quay.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tags) != 101 {
		t.Errorf("ListTags returned %d tags, want 101 (second page behind the Link header)", len(res.Tags))
	}
}

// An image with more than the old 10,000-tag ceiling (linuxserver/* on GHCR)
// is listed in full, so its newest versions are not missed.
func TestListTags_ReadsMoreThanTenThousandTags(t *testing.T) {
	const pages, perPage = 13, 1000
	withFakeRegistry(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tags/list") {
			http.NotFound(w, r)
			return
		}
		page := 0
		if last := r.URL.Query().Get("last"); last != "" {
			_, _ = fmt.Sscanf(last, "p%d-", &page)
			page++
		}
		tags := make([]string, perPage)
		for i := range tags {
			tags[i] = fmt.Sprintf("p%02d-%04d", page, i)
		}
		if page < pages-1 {
			w.Header().Set("Link", fmt.Sprintf(`</v2/org/app/tags/list?last=%s>; rel="next"`, tags[perPage-1]))
		}
		_ = json.NewEncoder(w).Encode(map[string][]string{"tags": tags})
	}))
	res, err := ListTags(context.Background(), "quay.example/org/app:1", "", "quay.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tags) != pages*perPage {
		t.Errorf("ListTags returned %d tags, want %d", len(res.Tags), pages*perPage)
	}
}

// A container on a floating tag keeps floating: it is offered the newest tag of
// the same precision (1.29 for 1.27), not the most specific one (1.29.2), which
// would pin it and narrow what it is offered afterwards.
func TestNewerVersionsScoped_FloatingCurrentTargetsFloatingTag(t *testing.T) {
	tests := []struct {
		name    string
		current string
		tags    []string
		scope   docker.SemverScope
		want    []string
	}{
		{"two-part", "1.27", []string{"1.29", "1.29.2", "1.28"}, docker.ScopeDefault, []string{"1.29", "1.28", "1.29.2"}},
		{"one-part", "1", []string{"2", "2.1", "2.1.3"}, docker.ScopeMajor, []string{"2", "2.1.3", "2.1"}},
		{"a newer line without a floating tag keeps its place", "3.21", []string{"3.22", "3.23.1"}, docker.ScopeDefault, []string{"3.23.1", "3.22"}},
		{"no floating form published", "1.27", []string{"1.29.0", "1.29.2"}, docker.ScopeDefault, []string{"1.29.2", "1.29.0"}},
		{"pinned current still gets the newest patch", "1.27.1", []string{"1.27.2", "1.27"}, docker.ScopeDefault, []string{"1.27.2"}},
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
