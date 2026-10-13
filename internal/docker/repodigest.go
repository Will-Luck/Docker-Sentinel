package docker

import "strings"

// repoDigestFor returns the repository digest, among an image's
// RepoDigests, of the repository ref names. An image pushed to several
// repositories has one per repository, in no set order, and the first may
// belong to another repository, whose digest the registry of ref does not
// know. When none matches it returns the first, as before; "" when there
// are none.
func repoDigestFor(ref string, digests []string) string {
	if len(digests) == 0 {
		return ""
	}
	want := normalisedRepo(ref)
	for _, d := range digests {
		if repo, _, ok := strings.Cut(d, "@"); ok && normalisedRepo(repo) == want {
			return d
		}
	}
	return digests[0]
}

// normalisedRepo returns the repository of an image reference in full
// form (registry host and path, without tag or digest), so that "nginx",
// "library/nginx" and "docker.io/library/nginx:1" compare equal.
func normalisedRepo(ref string) string {
	name, _, _ := strings.Cut(ref, "@")
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	host, path, found := strings.Cut(name, "/")
	if !found || (!strings.ContainsAny(host, ".:") && host != "localhost") {
		host, path = "docker.io", name
	}
	if host == "index.docker.io" || host == "registry-1.docker.io" {
		host = "docker.io"
	}
	if host == "docker.io" && !strings.Contains(path, "/") {
		path = "library/" + path
	}
	return host + "/" + path
}
