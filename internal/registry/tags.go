package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// TagList holds the response from the Docker registry v2 tags/list endpoint.
type TagList struct {
	Tags []string `json:"tags"`
}

// SemVer represents a parsed semantic version.
type SemVer struct {
	Major int
	Minor int
	Patch int
	Pre   string // pre-release suffix (e.g. "rc1", "beta2")
	Raw   string // original tag string
}

// TagsResult holds the response from ListTags, including rate limit headers.
type TagsResult struct {
	Tags    []string
	Headers http.Header // response headers for rate limit extraction
}

// ListTags fetches all tags for the given image reference from a container registry.
// For Docker Hub, it uses registry-1.docker.io. For other registries, it uses
// the registry host extracted from the image reference.
// The token is a bearer token for Docker Hub; for other registries, pass empty
// token and provide credentials via the cred parameter for Basic auth.
func ListTags(ctx context.Context, imageRef string, token string, host string, cred *RegistryCredential) (TagsResult, error) {
	repo := RepoPath(imageRef)
	var url string
	if host != "" && host != "docker.io" {
		// Request up to 10000 tags per page — GHCR defaults to 100 which
		// misses newer tags on images with many variants (e.g. nginx has 300+).
		url = "https://" + host + "/v2/" + repo + "/tags/list?n=10000"
	} else {
		url = "https://registry-1.docker.io/v2/" + repo + "/tags/list"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return TagsResult{}, fmt.Errorf("create tags request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if cred != nil {
		// Non-Hub registries: use Basic auth directly
		req.SetBasicAuth(cred.Username, cred.Secret)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return TagsResult{}, fmt.Errorf("fetch tags: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return TagsResult{}, fmt.Errorf("tags endpoint returned %d", resp.StatusCode)
	}

	var tagList TagList
	if err := json.NewDecoder(resp.Body).Decode(&tagList); err != nil {
		return TagsResult{}, fmt.Errorf("decode tags response: %w", err)
	}

	return TagsResult{Tags: tagList.Tags, Headers: resp.Header}, nil
}

// ParseSemVer attempts to parse a tag string as a semantic version.
// It handles "x.y.z", "x.y", and optional "v" prefix. Pre-release
// suffixes like "-rc1" or "-beta2" are captured in the Pre field.
// Returns the parsed version and true if successful, or zero value and false
// if the tag is not a valid semver.
func ParseSemVer(tag string) (SemVer, bool) {
	raw := tag

	// Strip optional "v" or "V" prefix.
	tag = strings.TrimPrefix(tag, "v")
	tag = strings.TrimPrefix(tag, "V")

	if tag == "" {
		return SemVer{}, false
	}

	// Split off pre-release suffix at the first hyphen.
	var pre string
	if idx := strings.Index(tag, "-"); idx >= 0 {
		pre = tag[idx+1:]
		tag = tag[:idx]
	}

	parts := strings.Split(tag, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return SemVer{}, false
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return SemVer{}, false
	}

	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return SemVer{}, false
	}

	var patch int
	if len(parts) == 3 {
		patch, err = strconv.Atoi(parts[2])
		if err != nil {
			return SemVer{}, false
		}
	}

	return SemVer{
		Major: major,
		Minor: minor,
		Patch: patch,
		Pre:   pre,
		Raw:   raw,
	}, true
}

// LessThan returns true if v is strictly less than other.
// Pre-release versions are considered less than their release counterpart
// (e.g. 1.2.3-rc1 < 1.2.3). When both have pre-release strings, they are
// compared lexicographically.
func (v SemVer) LessThan(other SemVer) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	if v.Patch != other.Patch {
		return v.Patch < other.Patch
	}

	// Same numeric version: pre-release sorts before release.
	if v.Pre != other.Pre {
		if v.Pre == "" {
			return false // v is release, other is pre-release
		}
		if other.Pre == "" {
			return true // v is pre-release, other is release
		}
		return v.Pre < other.Pre
	}

	return false // equal
}

// NewerVersions filters tags to find semver versions newer than current,
// returning them sorted from newest to oldest. Non-semver tags are ignored.
func NewerVersions(current string, tags []string) []SemVer {
	cur, ok := ParseSemVer(current)
	if !ok {
		return nil
	}

	var newer []SemVer
	for _, tag := range tags {
		sv, ok := ParseSemVer(tag)
		if !ok {
			continue
		}
		if cur.LessThan(sv) {
			newer = append(newer, sv)
		}
	}

	// Sort newest first.
	sort.Slice(newer, func(i, j int) bool {
		return newer[j].LessThan(newer[i])
	})

	return newer
}

// NormaliseRepo converts an image reference to a Docker Hub repository path.
// "nginx" becomes "library/nginx", "gitea/gitea:1.21" becomes "gitea/gitea".
func NormaliseRepo(imageRef string) string {
	// Strip tag or digest.
	ref := imageRef
	if idx := strings.Index(ref, "@"); idx >= 0 {
		ref = ref[:idx]
	}
	if idx := strings.Index(ref, ":"); idx >= 0 {
		ref = ref[:idx]
	}

	// Official images have no slash — prefix with "library/".
	if !strings.Contains(ref, "/") {
		ref = "library/" + ref
	}

	return ref
}
