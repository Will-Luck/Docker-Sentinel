package registry

import "strings"

// RegistryHost extracts the registry host from an image reference.
//
// Examples:
//
//	"nginx:1.24"                     -> "docker.io"
//	"library/nginx:latest"           -> "docker.io"
//	"ghcr.io/user/repo:tag"          -> "ghcr.io"
//	"hotio.dev/hotio/sonarr:latest"  -> "hotio.dev"
//	"registry-1.docker.io/lib/nginx" -> "docker.io"
//	"lscr.io/linuxserver/sonarr"     -> "lscr.io"
//	"docker.gitea.com/gitea-mcp"     -> "docker.gitea.com"
func RegistryHost(imageRef string) string {
	// Strip digest (@sha256:...).
	ref := imageRef
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}

	// Find the first path segment.
	firstSlash := strings.Index(ref, "/")
	if firstSlash < 0 {
		// Single-segment image like "nginx" -> Docker Hub.
		return "docker.io"
	}

	firstSegment := ref[:firstSlash]

	// If first segment contains a dot or colon, it's a registry hostname.
	if strings.ContainsAny(firstSegment, ".:") {
		return NormaliseRegistryHost(firstSegment)
	}

	// Otherwise it's a Docker Hub org/image like "gitea/gitea".
	return "docker.io"
}
