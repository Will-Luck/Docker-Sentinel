package registry

import (
	"context"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
)

// CheckResult holds the outcome of a registry digest check.
type CheckResult struct {
	ImageRef        string
	LocalDigest     string
	RemoteDigest    string
	UpdateAvailable bool
	IsLocal         bool
	Error           error
	NewerVersions   []string // Newer semver versions available (newest first)
}

// Checker queries the Docker daemon and remote registry to determine
// whether an image has an update available.
type Checker struct {
	docker docker.API
	log    *logging.Logger
}

// NewChecker creates a registry checker.
func NewChecker(d docker.API, log *logging.Logger) *Checker {
	return &Checker{docker: d, log: log}
}

// Check compares the local digest of an image to the remote registry digest.
func (c *Checker) Check(ctx context.Context, imageRef string) CheckResult {
	result := CheckResult{ImageRef: imageRef}

	// Local/untagged images can't be checked against a registry.
	if docker.IsLocalImage(imageRef) {
		result.IsLocal = true
		return result
	}

	// Strip the tag if present to get just repo:tag for digest lookup.
	// If the ref already contains @sha256:, it's pinned by digest — skip.
	if strings.Contains(imageRef, "@sha256:") {
		result.IsLocal = true // treat pinned-by-digest as not updatable
		return result
	}

	localDigest, err := c.docker.ImageDigest(ctx, imageRef)
	if err != nil {
		c.log.Warn("failed to get local digest", "image", imageRef, "error", err)
		result.Error = err
		return result
	}
	result.LocalDigest = localDigest

	remoteDigest, err := c.docker.DistributionDigest(ctx, imageRef)
	if err != nil {
		// Auth failures or 404s mean we can't check — treat as no update.
		c.log.Debug("failed to get remote digest, treating as local", "image", imageRef, "error", err)
		result.IsLocal = true
		return result
	}
	result.RemoteDigest = remoteDigest

	result.UpdateAvailable = !digestsMatch(localDigest, remoteDigest)
	return result
}

// digestsMatch compares two digests, normalising away the repo@ prefix.
// Local digests look like "docker.io/library/nginx@sha256:abc123..."
// Remote digests look like "sha256:abc123..."
func digestsMatch(local, remote string) bool {
	return extractHash(local) == extractHash(remote)
}

// extractHash returns the sha256:... portion of a digest string.
func extractHash(digest string) string {
	if i := strings.LastIndex(digest, "sha256:"); i >= 0 {
		return digest[i:]
	}
	return digest
}

// CheckVersioned performs a digest check and, for versioned tags, also looks
// for newer semver releases by listing remote tags.
func (c *Checker) CheckVersioned(ctx context.Context, imageRef string) CheckResult {
	result := c.Check(ctx, imageRef)

	// Only attempt version lookup if the base check succeeded and the image
	// has a semver-like tag.
	tag := ExtractTag(imageRef)
	if tag == "" || result.Error != nil || result.IsLocal {
		return result
	}

	_, ok := ParseSemVer(tag)
	if !ok {
		return result
	}

	repo := NormaliseRepo(imageRef)
	token, err := FetchAnonymousToken(ctx, repo)
	if err != nil {
		c.log.Debug("failed to fetch anonymous token for version check", "repo", repo, "error", err)
		return result
	}

	tags, err := ListTags(ctx, imageRef, token)
	if err != nil {
		c.log.Debug("failed to list tags for version check", "repo", repo, "error", err)
		return result
	}

	newer := NewerVersions(tag, tags)
	for _, sv := range newer {
		result.NewerVersions = append(result.NewerVersions, sv.Raw)
	}

	return result
}

// ExtractTag returns the tag portion of an image reference, or empty string
// if there is no tag (e.g. digest-pinned or bare image name).
func ExtractTag(imageRef string) string {
	// Remove digest portion if present.
	if idx := strings.Index(imageRef, "@"); idx >= 0 {
		return ""
	}

	if idx := strings.LastIndex(imageRef, ":"); idx >= 0 {
		// Ensure the colon is after any slash (not part of a registry hostname
		// with a port like ghcr.io:443/owner/repo).
		tag := imageRef[idx+1:]
		if !strings.Contains(tag, "/") {
			return tag
		}
	}

	return ""
}
