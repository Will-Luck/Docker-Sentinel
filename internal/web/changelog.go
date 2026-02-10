package web

import "strings"

// ChangelogURL generates a best-effort link to the changelog or release page
// for a Docker image. Returns an empty string if no URL can be determined.
func ChangelogURL(imageRef string) string {
	// Strip tag or digest (everything after : or @).
	ref := imageRef
	if idx := strings.LastIndex(ref, ":"); idx > 0 {
		// Avoid stripping the port from a registry host (e.g. "registry:5000/repo").
		// If the part after ":" contains a "/" it is a path, not a tag.
		candidate := ref[idx+1:]
		if !strings.Contains(candidate, "/") {
			ref = ref[:idx]
		}
	}
	if idx := strings.Index(ref, "@"); idx > 0 {
		ref = ref[:idx]
	}

	parts := strings.Split(ref, "/")

	switch {
	// ghcr.io/owner/repo
	case len(parts) >= 3 && parts[0] == "ghcr.io":
		owner := parts[1]
		repo := strings.Join(parts[2:], "/")
		return "https://github.com/" + owner + "/" + repo + "/releases"

	// lscr.io/linuxserver/name
	case len(parts) >= 3 && parts[0] == "lscr.io" && parts[1] == "linuxserver":
		name := parts[2]
		return "https://github.com/linuxserver/docker-" + name + "/releases"

	// Docker Hub official image (no slash, e.g. "nginx")
	case len(parts) == 1:
		return "https://hub.docker.com/_/" + parts[0]

	// Docker Hub org image (one slash, e.g. "gitea/gitea")
	case len(parts) == 2 && !strings.Contains(parts[0], "."):
		return "https://hub.docker.com/r/" + parts[0] + "/" + parts[1] + "/tags"

	default:
		return ""
	}
}
