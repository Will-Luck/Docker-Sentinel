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

// maxTagPages is the maximum number of pagination requests when fetching tags.
// GHCR caps responses at 1000 tags per page regardless of the n= parameter,
// and some images (e.g. linuxserver/*) have 5000+ tags with arch-specific and
// build-variant suffixes. 10 pages × 1000 = 10000 tags max.
const maxTagPages = 10

// ListTags fetches all tags for the given image reference from a container registry.
// For Docker Hub, it uses registry-1.docker.io. For other registries, it uses
// the registry host extracted from the image reference.
// Automatically paginates using the ?last= parameter when the registry returns
// a full page (GHCR caps at 1000 per page).
func ListTags(ctx context.Context, imageRef string, token string, host string, cred *RegistryCredential) (TagsResult, error) {
	repo := RepoPath(imageRef)
	var baseURL string
	if host != "" && host != "docker.io" {
		baseURL = "https://" + host + "/v2/" + repo + "/tags/list?n=10000"
	} else {
		baseURL = "https://registry-1.docker.io/v2/" + repo + "/tags/list"
	}

	var allTags []string
	var lastHeaders http.Header
	pageURL := baseURL

	for page := 0; page < maxTagPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return TagsResult{}, fmt.Errorf("create tags request: %w", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		} else if cred != nil {
			req.SetBasicAuth(cred.Username, cred.Secret)
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			return TagsResult{}, fmt.Errorf("fetch tags: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return TagsResult{}, fmt.Errorf("tags endpoint returned %d", resp.StatusCode)
		}

		var tagList TagList
		if err := json.NewDecoder(resp.Body).Decode(&tagList); err != nil {
			resp.Body.Close()
			return TagsResult{}, fmt.Errorf("decode tags response: %w", err)
		}
		lastHeaders = resp.Header
		resp.Body.Close()

		allTags = append(allTags, tagList.Tags...)

		// If we got fewer than 1000 tags, there are no more pages.
		if len(tagList.Tags) < 1000 {
			break
		}

		// Build next page URL using last= parameter.
		last := tagList.Tags[len(tagList.Tags)-1]
		if strings.Contains(baseURL, "?") {
			pageURL = baseURL + "&last=" + last
		} else {
			pageURL = baseURL + "?last=" + last
		}
	}

	return TagsResult{Tags: allTags, Headers: lastHeaders}, nil
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
		// Skip candidates from a different versioning scheme. Calendar
		// versioning tags like "2021.12.14" parse as semver with major >= 1900,
		// which would falsely compare as "newer" than real semver like "3.21".
		if versionSchemeMismatch(cur, sv) {
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

// versionSchemeMismatch returns true when two versions appear to use different
// versioning schemes (e.g. semver "3.21" vs calver "2021.12.14"). Comparing
// across schemes produces nonsensical results.
func versionSchemeMismatch(a, b SemVer) bool {
	aCalver := a.Major >= 1900
	bCalver := b.Major >= 1900
	return aCalver != bCalver
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
