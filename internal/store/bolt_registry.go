package store

import (
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
)

// AddIgnoredVersion records that a specific version should be ignored for a container.
// The value stored under each container name is a JSON array of version strings.
func (s *Store) AddIgnoredVersion(containerName, version string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketIgnoredVersions)

		existing := b.Get([]byte(containerName))
		var versions []string
		if existing != nil {
			if err := json.Unmarshal(existing, &versions); err != nil {
				return fmt.Errorf("unmarshal ignored versions: %w", err)
			}
		}

		// Skip if already present.
		for _, v := range versions {
			if v == version {
				return nil
			}
		}

		versions = append(versions, version)
		data, err := json.Marshal(versions)
		if err != nil {
			return fmt.Errorf("marshal ignored versions: %w", err)
		}
		return b.Put([]byte(containerName), data)
	})
}

// GetIgnoredVersions returns all ignored versions for a container.
// Returns an empty slice if none are stored.
func (s *Store) GetIgnoredVersions(containerName string) ([]string, error) {
	var versions []string
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketIgnoredVersions)
		v := b.Get([]byte(containerName))
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &versions)
	})
	if versions == nil {
		versions = []string{}
	}
	return versions, err
}

// ClearIgnoredVersions removes all ignored versions for a container.
func (s *Store) ClearIgnoredVersions(containerName string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketIgnoredVersions)
		return b.Delete([]byte(containerName))
	})
}

// GetRegistryCredentials loads registry credentials from the registry_credentials bucket.
func (s *Store) GetRegistryCredentials() ([]registry.RegistryCredential, error) {
	var creds []registry.RegistryCredential
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRegistryCreds)
		v := b.Get([]byte("credentials"))
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &creds)
	})
	return creds, err
}

// SetRegistryCredentials saves registry credentials to the registry_credentials bucket.
func (s *Store) SetRegistryCredentials(creds []registry.RegistryCredential) error {
	data, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshal registry credentials: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRegistryCreds)
		return b.Put([]byte("credentials"), data)
	})
}

// SaveRateLimits persists rate limit state for all registries.
func (s *Store) SaveRateLimits(data []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRateLimits)
		return b.Put([]byte("state"), data)
	})
}

// LoadRateLimits loads persisted rate limit state.
// Returns nil, nil if nothing is stored.
func (s *Store) LoadRateLimits() ([]byte, error) {
	var data []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRateLimits)
		v := b.Get([]byte("state"))
		if v != nil {
			data = make([]byte, len(v))
			copy(data, v)
		}
		return nil
	})
	return data, err
}

// ---------------------------------------------------------------------------
// Release sources
// ---------------------------------------------------------------------------

var keyReleaseSources = []byte("sources")

// GetReleaseSources returns all configured release sources.
func (s *Store) GetReleaseSources() ([]registry.ReleaseSource, error) {
	var sources []registry.ReleaseSource
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketReleaseSources).Get(keyReleaseSources)
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &sources)
	})
	if sources == nil {
		sources = []registry.ReleaseSource{}
	}
	return sources, err
}

// SetReleaseSources persists the full list of release sources.
func (s *Store) SetReleaseSources(sources []registry.ReleaseSource) error {
	data, err := json.Marshal(sources)
	if err != nil {
		return fmt.Errorf("marshal release sources: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketReleaseSources).Put(keyReleaseSources, data)
	})
}

// SaveGHCRCache persists GHCR alternative detection cache.
func (s *Store) SaveGHCRCache(data []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketGHCRAlternatives)
		return b.Put([]byte("cache"), data)
	})
}

// LoadGHCRCache loads persisted GHCR alternative cache.
// Returns nil, nil if nothing is stored.
func (s *Store) LoadGHCRCache() ([]byte, error) {
	var data []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketGHCRAlternatives)
		v := b.Get([]byte("cache"))
		if v != nil {
			data = make([]byte, len(v))
			copy(data, v)
		}
		return nil
	})
	return data, err
}
