package docker

import (
	"strconv"
	"strings"
	"time"
)

// Policy represents a container's update policy.
type Policy string

const (
	PolicyAuto   Policy = "auto"
	PolicyManual Policy = "manual"
	PolicyPinned Policy = "pinned"
)

// ContainerPolicy reads the sentinel.policy label from container labels
// and returns the update policy. Falls back to defaultPolicy if not set.
// The fromLabel return value indicates whether the policy came from an
// explicit Docker label (true) or the default fallback (false).
func ContainerPolicy(labels map[string]string, defaultPolicy string) (policy Policy, fromLabel bool) {
	if v, ok := labels["sentinel.policy"]; ok {
		switch Policy(strings.ToLower(v)) {
		case PolicyAuto:
			return PolicyAuto, true
		case PolicyManual:
			return PolicyManual, true
		case PolicyPinned:
			return PolicyPinned, true
		}
	}
	return Policy(defaultPolicy), false
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

// ContainerNotifySnooze reads the sentinel.notify-snooze label and returns
// the suppression duration to apply after a notification is sent.
// Returns 0 if the label is absent or invalid.
func ContainerNotifySnooze(labels map[string]string) time.Duration {
	v, ok := labels["sentinel.notify-snooze"]
	if !ok || v == "" {
		return 0
	}
	d, err := parseDurationWithDays(v)
	if err != nil {
		return 0
	}
	return d
}

// SemverScope controls the version range considered when finding newer versions.
type SemverScope string

const (
	ScopeDefault SemverScope = ""      // infer from tag precision
	ScopePatch   SemverScope = "patch" // same major.minor only
	ScopeMinor   SemverScope = "minor" // same major only
	ScopeMajor   SemverScope = "major" // any newer version
)

// ContainerTagFilters reads sentinel.include-tags and sentinel.exclude-tags labels.
func ContainerTagFilters(labels map[string]string) (include, exclude string) {
	return labels["sentinel.include-tags"], labels["sentinel.exclude-tags"]
}

// ContainerSemverScope reads the sentinel.semver label and returns the
// explicit version scope. Returns ScopeDefault if the label is absent or invalid.
func ContainerSemverScope(labels map[string]string) SemverScope {
	switch strings.ToLower(labels["sentinel.semver"]) {
	case "patch":
		return ScopePatch
	case "minor":
		return ScopeMinor
	case "major", "all":
		return ScopeMajor
	default:
		return ScopeDefault
	}
}

// parseDurationWithDays extends time.ParseDuration with a "d" suffix for days.
func parseDurationWithDays(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		days := strings.TrimSuffix(s, "d")
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
