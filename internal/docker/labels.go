package docker

import "strings"

// Policy represents a container's update policy.
type Policy string

const (
	PolicyAuto   Policy = "auto"
	PolicyManual Policy = "manual"
	PolicyPinned Policy = "pinned"
)

// ContainerPolicy reads the sentinel.policy label from container labels
// and returns the update policy. Falls back to defaultPolicy if not set.
func ContainerPolicy(labels map[string]string, defaultPolicy string) Policy {
	if v, ok := labels["sentinel.policy"]; ok {
		switch Policy(strings.ToLower(v)) {
		case PolicyAuto:
			return PolicyAuto
		case PolicyManual:
			return PolicyManual
		case PolicyPinned:
			return PolicyPinned
		}
	}
	return Policy(defaultPolicy)
}

// IsLocalImage returns true if the image reference looks like a locally built
// image that has no registry to check against. Only returns true for images
// with no dots AND no slashes — these are bare names like "myapp:v1" that
// can't be resolved via a registry. Docker Hub images like "nginx:latest"
// or "library/nginx" are NOT considered local — they should go through
// the registry check (DistributionInspect handles auth failures gracefully).
func IsLocalImage(imageRef string) bool {
	// Strip tag/digest for analysis.
	ref := imageRef
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.Index(ref, ":"); i >= 0 {
		ref = ref[:i]
	}

	// If there's a slash, it could be a Docker Hub org image (gitea/gitea)
	// or a registry (ghcr.io/owner/image). Either way, not local.
	if strings.Contains(ref, "/") {
		return false
	}

	// If there's a dot, it's a registry hostname. Not local.
	if strings.Contains(ref, ".") {
		return false
	}

	// Bare single-segment names: official Docker Hub images like "nginx",
	// "postgres", "redis" are real registry images. But locally built images
	// like "myapp" also look like this. We can't distinguish them reliably,
	// so we DON'T mark them as local — let the registry check try and fail
	// gracefully for truly local images.
	return false
}
