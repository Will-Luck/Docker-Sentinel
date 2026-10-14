package docker

import "testing"

// An image pushed to several repositories has one repository digest per
// repository, in no set order. The digest that identifies a container's
// image is the one of the repository the container names; taking the
// first one compared another repository's digest with the registry and
// reported an update that was not there (live-20).
func TestRepoDigestFor(t *testing.T) {
	multi := []string{
		"registry.example.com:5443/test/nsc@sha256:aaa",
		"registry.example.com:5443/test/stp@sha256:bbb",
	}
	hub := []string{"ghcr.io/will-luck/docker-sentinel@sha256:ccc", "nginx@sha256:ddd", "acme/app@sha256:eee"}
	for _, tc := range []struct {
		ref     string
		digests []string
		want    string
	}{
		{"registry.example.com:5443/test/stp:1.0.1", multi, "registry.example.com:5443/test/stp@sha256:bbb"},
		{"registry.example.com:5443/test/nsc:1.0.1", multi, "registry.example.com:5443/test/nsc@sha256:aaa"},
		{"nginx:1.27", hub, "nginx@sha256:ddd"},
		{"docker.io/library/nginx:1.27", hub, "nginx@sha256:ddd"},
		{"docker.io/acme/app", hub, "acme/app@sha256:eee"},
		{"acme/app:2", hub, "acme/app@sha256:eee"},
		{"registry.example.com:5443/test/other:1", multi, "registry.example.com:5443/test/nsc@sha256:aaa"}, // none matches: the first, as before
		{"nginx:1.27", nil, ""},
	} {
		if got := repoDigestFor(tc.ref, tc.digests); got != tc.want {
			t.Errorf("repoDigestFor(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}
