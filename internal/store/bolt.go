package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
)

var (
	bucketSnapshots       = []byte("snapshots")
	bucketHistory         = []byte("history")
	bucketState           = []byte("state")
	bucketQueue           = []byte("queue")
	bucketPolicies        = []byte("policies")
	bucketLogs            = []byte("logs")
	bucketSettings        = []byte("settings")
	bucketNotifyState     = []byte("notify_state")
	bucketNotifyPrefs     = []byte("notify_prefs")
	bucketIgnoredVersions = []byte("ignored_versions")
	bucketRegistryCreds   = []byte("registry_credentials")
	bucketRateLimits        = []byte("rate_limits")
	bucketGHCRAlternatives  = []byte("ghcr_alternatives")
)

// UpdateRecord represents a completed (or failed) container update.
type UpdateRecord struct {
	Timestamp     time.Time     `json:"timestamp"`
	ContainerName string        `json:"container_name"`
	OldImage      string        `json:"old_image"`
	OldDigest     string        `json:"old_digest"`
	NewImage      string        `json:"new_image"`
	NewDigest     string        `json:"new_digest"`
	Outcome       string        `json:"outcome"` // "success", "rollback", "failed", or "finalise_warning"
	Duration      time.Duration `json:"duration"`
	Error         string        `json:"error,omitempty"`
}

// Store wraps a BoltDB database for Sentinel persistence.
type Store struct {
	db *bolt.DB
}

// Open creates or opens a BoltDB database at the given path and ensures
// all required buckets exist.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bolt db: %w", err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketSnapshots, bucketHistory, bucketState, bucketQueue, bucketPolicies, bucketLogs, bucketSettings, bucketNotifyState, bucketNotifyPrefs, bucketIgnoredVersions, bucketRegistryCreds, bucketRateLimits, bucketGHCRAlternatives} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create buckets: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying BoltDB.
func (s *Store) Close() error {
	return s.db.Close()
}

// SaveSnapshot stores a container inspect JSON snapshot.
// Key format: "{name}::{RFC3339Nano}" for chronological ordering.
func (s *Store) SaveSnapshot(name string, data []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSnapshots)
		key := []byte(fmt.Sprintf("%s::%s", name, time.Now().UTC().Format(time.RFC3339Nano)))
		return b.Put(key, data)
	})
}

// GetLatestSnapshot returns the most recent snapshot for the given container name.
// Returns nil, nil if no snapshot exists.
func (s *Store) GetLatestSnapshot(name string) ([]byte, error) {
	var data []byte
	prefix := []byte(name + "::")

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSnapshots)
		c := b.Cursor()

		// Seek to the end of this container's keys by seeking past the prefix.
		// The prefix range ends at name + ":;" (';' is one byte after ':' in ASCII).
		endPrefix := []byte(name + "::;")
		k, _ := c.Seek(endPrefix)
		var v []byte
		if k == nil {
			// Past the end of the bucket — go to last key.
			k, v = c.Last()
		} else {
			// We overshot — go back one.
			k, v = c.Prev()
		}

		if k == nil {
			return nil
		}
		// Verify the key actually belongs to this container.
		if len(k) < len(prefix) || string(k[:len(prefix)]) != string(prefix) {
			return nil
		}
		data = make([]byte, len(v))
		copy(data, v)
		return nil
	})
	return data, err
}

// RecordUpdate appends an update record to the history bucket.
func (s *Store) RecordUpdate(rec UpdateRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal update record: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHistory)
		key := []byte(rec.Timestamp.UTC().Format(time.RFC3339Nano))
		return b.Put(key, data)
	})
}

// ListHistory returns the most recent update records, up to limit.
func (s *Store) ListHistory(limit int) ([]UpdateRecord, error) {
	var records []UpdateRecord

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHistory)
		c := b.Cursor()

		// Walk backwards from the end (newest first).
		for k, v := c.Last(); k != nil && len(records) < limit; k, v = c.Prev() {
			var rec UpdateRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			records = append(records, rec)
		}
		return nil
	})
	return records, err
}

// SetMaintenance marks a container as in or out of a maintenance window.
func (s *Store) SetMaintenance(name string, active bool) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketState)
		key := []byte("maintenance::" + name)
		if active {
			return b.Put(key, []byte("true"))
		}
		return b.Delete(key)
	})
}

// GetMaintenance returns whether a container is currently in maintenance.
func (s *Store) GetMaintenance(name string) (bool, error) {
	var active bool
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketState)
		v := b.Get([]byte("maintenance::" + name))
		active = v != nil && string(v) == "true"
		return nil
	})
	return active, err
}

// SavePendingQueue persists the pending update queue as JSON.
func (s *Store) SavePendingQueue(data []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketQueue)
		return b.Put([]byte("pending"), data)
	})
}

// LoadPendingQueue loads the persisted pending update queue.
// Returns nil, nil if no queue is saved.
func (s *Store) LoadPendingQueue() ([]byte, error) {
	var data []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketQueue)
		v := b.Get([]byte("pending"))
		if v != nil {
			data = make([]byte, len(v))
			copy(data, v)
		}
		return nil
	})
	return data, err
}

// SnapshotEntry represents a stored snapshot with its timestamp.
type SnapshotEntry struct {
	Timestamp time.Time
	Data      []byte
}

// ListSnapshots returns all snapshots for a container, newest first.
func (s *Store) ListSnapshots(name string) ([]SnapshotEntry, error) {
	var entries []SnapshotEntry
	prefix := []byte(name + "::")

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSnapshots)
		c := b.Cursor()

		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			// Parse timestamp from key suffix (after "::").
			tsStr := string(k[len(prefix):])
			ts, err := time.Parse(time.RFC3339Nano, tsStr)
			if err != nil {
				continue // skip malformed keys
			}

			data := make([]byte, len(v))
			copy(data, v)

			entries = append(entries, SnapshotEntry{
				Timestamp: ts,
				Data:      data,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sort newest first (reverse chronological).
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})

	return entries, nil
}

// ListHistoryByContainer returns update records filtered by container name,
// newest first, up to limit.
func (s *Store) ListHistoryByContainer(name string, limit int) ([]UpdateRecord, error) {
	var records []UpdateRecord

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHistory)
		c := b.Cursor()

		// Reverse cursor scan (start at end, move backwards).
		for k, v := c.Last(); k != nil && len(records) < limit; k, v = c.Prev() {
			var rec UpdateRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				continue
			}
			if rec.ContainerName == name {
				records = append(records, rec)
			}
		}
		return nil
	})
	return records, err
}

// GetPolicyOverride returns the DB-stored policy override for a container.
// Returns ("", false) if no override exists.
func (s *Store) GetPolicyOverride(name string) (string, bool) {
	var policy string
	_ = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPolicies)
		v := b.Get([]byte(name))
		if v != nil {
			policy = string(v)
		}
		return nil
	})
	return policy, policy != ""
}

// SetPolicyOverride stores a policy override for a container in BoltDB.
func (s *Store) SetPolicyOverride(name, policy string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPolicies)
		return b.Put([]byte(name), []byte(policy))
	})
}

// DeletePolicyOverride removes the policy override for a container.
func (s *Store) DeletePolicyOverride(name string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPolicies)
		return b.Delete([]byte(name))
	})
}

// AllPolicyOverrides returns all stored policy overrides.
func (s *Store) AllPolicyOverrides() map[string]string {
	result := make(map[string]string)
	_ = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPolicies)
		return b.ForEach(func(k, v []byte) error {
			result[string(k)] = string(v)
			return nil
		})
	})
	return result
}

// LogEntry represents a timestamped event in the activity log.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"` // policy_set, policy_delete, bulk_policy, update, rollback, approve, reject
	Message   string    `json:"message"`
	Container string    `json:"container,omitempty"`
}

// AppendLog writes a log entry to the logs bucket.
func (s *Store) AppendLog(entry LogEntry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal log entry: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketLogs)
		key := []byte(entry.Timestamp.Format(time.RFC3339Nano))
		return b.Put(key, data)
	})
}

// ListLogs returns the most recent log entries, newest first, up to limit.
func (s *Store) ListLogs(limit int) ([]LogEntry, error) {
	var entries []LogEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketLogs)
		c := b.Cursor()
		for k, v := c.Last(); k != nil && len(entries) < limit; k, v = c.Prev() {
			var entry LogEntry
			if err := json.Unmarshal(v, &entry); err != nil {
				continue
			}
			entries = append(entries, entry)
		}
		return nil
	})
	return entries, err
}

// DeleteOldSnapshots removes all but the N most recent snapshots for a container.
func (s *Store) DeleteOldSnapshots(name string, keep int) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSnapshots)
		c := b.Cursor()
		prefix := []byte(name + "::")

		// Collect all matching keys.
		var keys [][]byte
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			keyCopy := make([]byte, len(k))
			copy(keyCopy, k)
			keys = append(keys, keyCopy)
		}

		// Keys are in lexicographic (chronological) order — newest are at the end.
		// Delete all except the last `keep` keys.
		if len(keys) <= keep {
			return nil
		}

		toDelete := keys[:len(keys)-keep]
		for _, k := range toDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// SaveSetting stores a setting key-value pair in the settings bucket.
func (s *Store) SaveSetting(key, value string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSettings)
		return b.Put([]byte(key), []byte(value))
	})
}

// LoadSetting loads a setting by key from the settings bucket.
// Returns empty string if the key doesn't exist.
func (s *Store) LoadSetting(key string) (string, error) {
	var val string
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSettings)
		v := b.Get([]byte(key))
		if v != nil {
			val = string(v)
		}
		return nil
	})
	return val, err
}

// GetAllSettings returns all key-value pairs from the settings bucket.
// Keys used internally (notification_config, notification_channels) are excluded
// to avoid leaking large JSON blobs — only simple string settings are returned.
func (s *Store) GetAllSettings() (map[string]string, error) {
	result := make(map[string]string)
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSettings)
		return b.ForEach(func(k, v []byte) error {
			key := string(k)
			// Skip internal compound keys that store JSON blobs.
			if key == "notification_config" || key == "notification_channels" {
				return nil
			}
			result[key] = string(v)
			return nil
		})
	})
	return result, err
}

// NotificationConfig represents persisted notification settings.
type NotificationConfig struct {
	GotifyURL      string            `json:"gotify_url"`
	GotifyToken    string            `json:"gotify_token"`
	WebhookURL     string            `json:"webhook_url"`
	WebhookHeaders map[string]string `json:"webhook_headers"`
}

// GetNotificationConfig loads the notification configuration from the settings bucket.
func (s *Store) GetNotificationConfig() (NotificationConfig, error) {
	var cfg NotificationConfig
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSettings)
		v := b.Get([]byte("notification_config"))
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &cfg)
	})
	return cfg, err
}

// SetNotificationConfig saves the notification configuration to the settings bucket.
func (s *Store) SetNotificationConfig(cfg NotificationConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal notification config: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSettings)
		return b.Put([]byte("notification_config"), data)
	})
}

// GetNotificationChannels loads notification channels from the settings bucket.
// Handles legacy format migration: if stored data is a JSON object (old NotificationConfig),
// it converts it to the new channel array format.
func (s *Store) GetNotificationChannels() ([]notify.Channel, error) {
	var channels []notify.Channel
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSettings)
		v := b.Get([]byte("notification_channels"))
		if v == nil {
			// Try legacy key migration.
			legacy := b.Get([]byte("notification_config"))
			if legacy != nil {
				channels = migrateFromLegacy(legacy)
			}
			return nil
		}
		return json.Unmarshal(v, &channels)
	})
	return channels, err
}

// SetNotificationChannels saves notification channels to the settings bucket.
func (s *Store) SetNotificationChannels(channels []notify.Channel) error {
	data, err := json.Marshal(channels)
	if err != nil {
		return fmt.Errorf("marshal notification channels: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSettings)
		return b.Put([]byte("notification_channels"), data)
	})
}

// migrateFromLegacy converts old NotificationConfig JSON into Channel entries.
func migrateFromLegacy(data []byte) []notify.Channel {
	var legacy NotificationConfig
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil
	}

	var channels []notify.Channel

	if legacy.GotifyURL != "" {
		settings, _ := json.Marshal(notify.GotifySettings{
			URL:   legacy.GotifyURL,
			Token: legacy.GotifyToken,
		})
		channels = append(channels, notify.Channel{
			ID:       notify.GenerateID(),
			Type:     notify.ProviderGotify,
			Name:     "Gotify",
			Enabled:  true,
			Settings: settings,
		})
	}

	if legacy.WebhookURL != "" {
		settings, _ := json.Marshal(notify.WebhookSettings{
			URL:     legacy.WebhookURL,
			Headers: legacy.WebhookHeaders,
		})
		channels = append(channels, notify.Channel{
			ID:       notify.GenerateID(),
			Type:     notify.ProviderWebhook,
			Name:     "Webhook",
			Enabled:  true,
			Settings: settings,
		})
	}

	return channels
}

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
