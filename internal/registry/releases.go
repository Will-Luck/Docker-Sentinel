package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ReleaseInfo holds a GitHub release URL and a truncated body.
type ReleaseInfo struct {
	URL  string
	Body string // first 500 chars
}

var releaseCache struct {
	sync.Mutex
	entries map[string]releaseCacheEntry
}

type releaseCacheEntry struct {
	info      *ReleaseInfo
	fetchedAt time.Time
}

func init() {
	releaseCache.entries = make(map[string]releaseCacheEntry)
}

// FetchReleaseNotes fetches GitHub release notes for the given image + version.
// Returns nil if not found or unsupported. Results are cached for 1 hour.
func FetchReleaseNotes(ctx context.Context, imageRef, version string) *ReleaseInfo {
	repo := imageToGitHubRepo(imageRef)
	if repo == "" {
		return nil
	}

	cacheKey := repo + ":" + version
	releaseCache.Lock()
	if entry, ok := releaseCache.entries[cacheKey]; ok && time.Since(entry.fetchedAt) < time.Hour {
		releaseCache.Unlock()
		return entry.info
	}
	releaseCache.Unlock()

	info := fetchGitHubRelease(ctx, repo, version)

	releaseCache.Lock()
	releaseCache.entries[cacheKey] = releaseCacheEntry{info: info, fetchedAt: time.Now()}
	releaseCache.Unlock()

	return info
}

// imageToGitHubRepo maps an image ref to a "owner/repo" GitHub path.
// Returns "" if the image can't be mapped.
func imageToGitHubRepo(imageRef string) string {
	ref := imageRef
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		candidate := ref[i+1:]
		if !strings.Contains(candidate, "/") {
			ref = ref[:i]
		}
	}

	if strings.HasPrefix(ref, "ghcr.io/") {
		return strings.TrimPrefix(ref, "ghcr.io/")
	}
	if strings.HasPrefix(ref, "lscr.io/linuxserver/") {
		name := strings.TrimPrefix(ref, "lscr.io/linuxserver/")
		return "linuxserver/docker-" + name
	}
	if strings.HasPrefix(ref, "linuxserver/") {
		name := strings.TrimPrefix(ref, "linuxserver/")
		return "linuxserver/docker-" + name
	}

	return ""
}

func fetchGitHubRelease(ctx context.Context, repo, version string) *ReleaseInfo {
	tags := []string{version}
	if !strings.HasPrefix(version, "v") {
		tags = append(tags, "v"+version)
	}

	for _, tag := range tags {
		url := fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", repo, tag)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/vnd.github.v3+json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var release struct {
			HTMLURL string `json:"html_url"`
			Body    string `json:"body"`
		}
		json.NewDecoder(resp.Body).Decode(&release) //nolint:errcheck
		resp.Body.Close()

		if release.HTMLURL == "" {
			continue
		}

		body := release.Body
		if len(body) > 500 {
			body = body[:500] + "..."
		}

		return &ReleaseInfo{URL: release.HTMLURL, Body: body}
	}

	return nil
}
