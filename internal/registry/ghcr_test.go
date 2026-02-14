package registry

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestGHCRCache_SetGetExpiry(t *testing.T) {
	cache := NewGHCRCache(1 * time.Hour)

	alt := GHCRAlternative{
		DockerHubImage: "gitea/gitea",
		GHCRImage:      "ghcr.io/go-gitea/gitea",
		Tag:            "latest",
		Available:      true,
		DigestMatch:    true,
		CheckedAt:      time.Now(),
	}

	cache.Set("gitea/gitea", "latest", alt)

	// Should return the entry immediately.
	got, ok := cache.Get("gitea/gitea", "latest")
	if !ok {
		t.Fatal("expected cache hit, got miss")
	}
	if got.DockerHubImage != "gitea/gitea" {
		t.Errorf("expected DockerHubImage %q, got %q", "gitea/gitea", got.DockerHubImage)
	}
	if !got.Available {
		t.Error("expected Available=true")
	}

	// Simulate expiry by setting CachedAt far in the past.
	cache.mu.Lock()
	key := cacheKey("gitea/gitea", "latest")
	entry := cache.entries[key]
	entry.CachedAt = time.Now().Add(-2 * time.Hour)
	cache.entries[key] = entry
	cache.mu.Unlock()

	// Should return nil after expiry.
	got, ok = cache.Get("gitea/gitea", "latest")
	if ok {
		t.Fatal("expected cache miss after expiry, got hit")
	}
	if got != nil {
		t.Fatal("expected nil after expiry")
	}
}

func TestGHCRCache_All(t *testing.T) {
	cache := NewGHCRCache(1 * time.Hour)

	cache.Set("gitea/gitea", "latest", GHCRAlternative{
		DockerHubImage: "gitea/gitea",
		Available:      true,
	})
	cache.Set("portainer/portainer-ce", "latest", GHCRAlternative{
		DockerHubImage: "portainer/portainer-ce",
		Available:      true,
	})
	cache.Set("linuxserver/sonarr", "latest", GHCRAlternative{
		DockerHubImage: "linuxserver/sonarr",
		Available:      false,
	})

	// Expire one entry.
	cache.mu.Lock()
	key := cacheKey("linuxserver/sonarr", "latest")
	entry := cache.entries[key]
	entry.CachedAt = time.Now().Add(-2 * time.Hour)
	cache.entries[key] = entry
	cache.mu.Unlock()

	all := cache.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 non-expired entries, got %d", len(all))
	}
}

func TestGHCRCache_ExportImport(t *testing.T) {
	cache := NewGHCRCache(1 * time.Hour)

	cache.Set("gitea/gitea", "latest", GHCRAlternative{
		DockerHubImage: "gitea/gitea",
		GHCRImage:      "ghcr.io/go-gitea/gitea",
		Tag:            "latest",
		Available:      true,
		DigestMatch:    true,
		HubDigest:      "sha256:aaa",
		GHCRDigest:     "sha256:aaa",
		CheckedAt:      time.Now(),
	})
	cache.Set("portainer/portainer-ce", "2.19", GHCRAlternative{
		DockerHubImage: "portainer/portainer-ce",
		GHCRImage:      "ghcr.io/portainer/portainer-ce",
		Tag:            "2.19",
		Available:      false,
		CheckedAt:      time.Now(),
	})

	data, err := cache.Export()
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Verify it's valid JSON.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("exported data is not valid JSON: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("expected 2 entries in exported JSON, got %d", len(raw))
	}

	// Import into a fresh cache and verify.
	cache2 := NewGHCRCache(1 * time.Hour)
	if err := cache2.Import(data); err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	got, ok := cache2.Get("gitea/gitea", "latest")
	if !ok {
		t.Fatal("expected cache hit after import")
	}
	if got.GHCRImage != "ghcr.io/go-gitea/gitea" {
		t.Errorf("expected GHCRImage %q, got %q", "ghcr.io/go-gitea/gitea", got.GHCRImage)
	}
	if !got.DigestMatch {
		t.Error("expected DigestMatch=true after import")
	}

	got2, ok := cache2.Get("portainer/portainer-ce", "2.19")
	if !ok {
		t.Fatal("expected cache hit for portainer after import")
	}
	if got2.Available {
		t.Error("expected Available=false for portainer after import")
	}
}

func TestGHCRKnownMappings(t *testing.T) {
	got, ok := ghcrKnownMappings["gitea/gitea"]
	if !ok {
		t.Fatal("expected gitea/gitea to be in known mappings")
	}
	if got != "go-gitea/gitea" {
		t.Errorf("expected mapping %q, got %q", "go-gitea/gitea", got)
	}
}

func TestCheckGHCRAlternative_SkipNonDockerHub(t *testing.T) {
	ctx := context.Background()

	// GHCR image should be skipped.
	alt, err := CheckGHCRAlternative(ctx, "ghcr.io/user/repo:latest", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alt != nil {
		t.Errorf("expected nil for GHCR image, got %+v", alt)
	}

	// lscr.io image should be skipped.
	alt, err = CheckGHCRAlternative(ctx, "lscr.io/linuxserver/sonarr:latest", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alt != nil {
		t.Errorf("expected nil for lscr.io image, got %+v", alt)
	}
}

func TestCheckGHCRAlternative_SkipLibraryImages(t *testing.T) {
	ctx := context.Background()

	// Official library image "nginx" resolves to "library/nginx".
	alt, err := CheckGHCRAlternative(ctx, "nginx:latest", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alt != nil {
		t.Errorf("expected nil for library image, got %+v", alt)
	}

	// Explicit library/ prefix.
	alt, err = CheckGHCRAlternative(ctx, "library/redis:7", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alt != nil {
		t.Errorf("expected nil for library/redis image, got %+v", alt)
	}
}
