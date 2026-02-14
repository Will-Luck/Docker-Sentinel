package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// GHCRAlternative holds the result of checking whether a Docker Hub image
// has a corresponding image on GitHub Container Registry.
type GHCRAlternative struct {
	DockerHubImage string    `json:"docker_hub_image"`
	GHCRImage      string    `json:"ghcr_image"`
	Tag            string    `json:"tag"`
	Available      bool      `json:"available"`
	DigestMatch    bool      `json:"digest_match"`
	HubDigest      string    `json:"hub_digest,omitempty"`
	GHCRDigest     string    `json:"ghcr_digest,omitempty"`
	CheckedAt      time.Time `json:"checked_at"`
}

// ghcrKnownMappings maps Docker Hub repositories to their GHCR equivalents
// where the organisation name differs between registries.
var ghcrKnownMappings = map[string]string{
	"gitea/gitea": "go-gitea/gitea",
}

// ghcrCacheEntry wraps a GHCRAlternative with the time it was cached.
type ghcrCacheEntry struct {
	Alt      GHCRAlternative `json:"alt"`
	CachedAt time.Time       `json:"cached_at"`
}

// GHCRCache is a thread-safe cache of GHCR alternative checks, keyed by
// "repo::tag". Entries expire after the configured TTL.
type GHCRCache struct {
	mu      sync.RWMutex
	entries map[string]ghcrCacheEntry
	ttl     time.Duration
}

// NewGHCRCache creates a new cache with the given TTL for entries.
func NewGHCRCache(ttl time.Duration) *GHCRCache {
	return &GHCRCache{
		entries: make(map[string]ghcrCacheEntry),
		ttl:     ttl,
	}
}

// cacheKey builds the map key from a repo and tag.
func cacheKey(repo, tag string) string {
	return repo + "::" + tag
}

// Get returns the cached alternative for the given repo and tag.
// Returns nil and false if the entry is missing or expired.
func (c *GHCRCache) Get(repo, tag string) (*GHCRAlternative, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[cacheKey(repo, tag)]
	if !ok {
		return nil, false
	}
	if time.Since(entry.CachedAt) > c.ttl {
		return nil, false
	}

	alt := entry.Alt
	return &alt, true
}

// Set stores a GHCRAlternative in the cache with the current time.
func (c *GHCRCache) Set(repo, tag string, alt GHCRAlternative) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[cacheKey(repo, tag)] = ghcrCacheEntry{
		Alt:      alt,
		CachedAt: time.Now(),
	}
}

// All returns all non-expired entries in the cache.
func (c *GHCRCache) All() []GHCRAlternative {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	var result []GHCRAlternative
	for _, entry := range c.entries {
		if now.Sub(entry.CachedAt) <= c.ttl {
			result = append(result, entry.Alt)
		}
	}
	return result
}

// Export serialises the cache to JSON for BoltDB persistence.
func (c *GHCRCache) Export() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return json.Marshal(c.entries)
}

// Import restores cache state from persisted JSON. Loaded entries overwrite
// any existing entries with the same key.
func (c *GHCRCache) Import(data []byte) error {
	var loaded map[string]ghcrCacheEntry
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range loaded {
		c.entries[key] = entry
	}
	return nil
}

// CheckGHCRAlternative checks whether a Docker Hub image has a corresponding
// image on GHCR. Returns nil for non-Docker-Hub images and official library
// images (e.g. nginx, redis) which rarely have GHCR equivalents.
func CheckGHCRAlternative(ctx context.Context, imageRef string, hubCred, ghcrCred *RegistryCredential) (*GHCRAlternative, error) {
	host := RegistryHost(imageRef)
	if host != "docker.io" {
		return nil, nil
	}

	repo := RepoPath(imageRef)
	if strings.HasPrefix(repo, "library/") {
		return nil, nil
	}

	tag := ExtractTag(imageRef)
	if tag == "" {
		tag = "latest"
	}

	// Determine the GHCR repository path.
	ghcrRepo := repo
	if mapped, ok := ghcrKnownMappings[repo]; ok {
		ghcrRepo = mapped
	}

	alt := &GHCRAlternative{
		DockerHubImage: repo,
		GHCRImage:      "ghcr.io/" + ghcrRepo,
		Tag:            tag,
		CheckedAt:      time.Now(),
	}

	// Fetch Docker Hub digest.
	hubToken, err := FetchToken(ctx, repo, hubCred, "docker.io")
	if err != nil {
		return nil, fmt.Errorf("fetch Docker Hub token: %w", err)
	}

	hubDigest, _, err := ManifestDigest(ctx, repo, tag, hubToken, "docker.io", hubCred)
	if err != nil {
		return nil, fmt.Errorf("fetch Docker Hub digest: %w", err)
	}
	alt.HubDigest = hubDigest

	// Fetch GHCR digest.
	ghcrToken, err := FetchGHCRToken(ctx, ghcrRepo)
	if err != nil {
		alt.Available = false
		return alt, nil
	}

	ghcrDigest, _, err := ManifestDigest(ctx, ghcrRepo, tag, ghcrToken, "ghcr.io", ghcrCred)
	if err != nil {
		alt.Available = false
		return alt, nil
	}
	alt.GHCRDigest = ghcrDigest
	alt.Available = true
	alt.DigestMatch = extractHash(hubDigest) == extractHash(ghcrDigest)

	return alt, nil
}
