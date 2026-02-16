package web

import (
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
)

// ChangelogURL generates a best-effort link to the changelog or release page
// for a Docker image. Returns an empty string if no URL can be determined.
func ChangelogURL(imageRef string) string {
	ref := stripTagDigest(imageRef)
	parts := strings.Split(ref, "/")

	switch {
	// ghcr.io/owner/repo — link to the GitHub Packages page, not the repo's
	// releases page, because the GitHub repo name often doesn't match the
	// GHCR image name (e.g. ghcr.io/valkey-io/valkey → valkey-container repo).
	case len(parts) >= 3 && parts[0] == "ghcr.io":
		owner := parts[1]
		repo := parts[len(parts)-1] // last segment is the package name
		return "https://github.com/orgs/" + owner + "/packages/container/package/" + repo

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

// VersionURL generates a link to a specific version/tag page for a Docker image.
// For GitHub-hosted images, links to the specific release tag.
// For Docker Hub, links to the tag listing with the version as a filter hint.
func VersionURL(imageRef, version string) string {
	if version == "" {
		return ChangelogURL(imageRef)
	}

	ref := stripTagDigest(imageRef)
	parts := strings.Split(ref, "/")

	switch {
	case len(parts) >= 3 && parts[0] == "ghcr.io":
		owner := parts[1]
		repo := parts[len(parts)-1]
		return "https://github.com/orgs/" + owner + "/packages/container/package/" + repo

	case len(parts) >= 3 && parts[0] == "lscr.io" && parts[1] == "linuxserver":
		name := parts[2]
		return "https://github.com/linuxserver/docker-" + name + "/releases/tag/" + version

	case len(parts) == 1:
		return "https://hub.docker.com/_/" + parts[0] + "/tags?name=" + version

	case len(parts) == 2 && !strings.Contains(parts[0], "."):
		return "https://hub.docker.com/r/" + parts[0] + "/" + parts[1] + "/tags?name=" + version

	default:
		return ""
	}
}

// ImageTag extracts the tag portion from a full image reference.
// Template helper wrapping registry.ExtractTag.
func ImageTag(imageRef string) string {
	tag := registry.ExtractTag(imageRef)
	if tag == "" {
		if idx := strings.LastIndex(imageRef, "/"); idx >= 0 {
			return imageRef[idx+1:]
		}
		return imageRef
	}
	return tag
}

// stripTagDigest removes the tag or digest suffix from an image reference.
func stripTagDigest(imageRef string) string {
	ref := imageRef
	if idx := strings.LastIndex(ref, ":"); idx > 0 {
		candidate := ref[idx+1:]
		if !strings.Contains(candidate, "/") {
			ref = ref[:idx]
		}
	}
	if idx := strings.Index(ref, "@"); idx > 0 {
		ref = ref[:idx]
	}
	return ref
}
