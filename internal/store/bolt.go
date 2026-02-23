package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketSnapshots        = []byte("snapshots")
	bucketHistory          = []byte("history")
	bucketState            = []byte("state")
	bucketQueue            = []byte("queue")
	bucketPolicies         = []byte("policies")
	bucketLogs             = []byte("logs")
	bucketSettings         = []byte("settings")
	bucketNotifyState      = []byte("notify_state")
	bucketNotifyPrefs      = []byte("notify_prefs")
	bucketIgnoredVersions  = []byte("ignored_versions")
	bucketRegistryCreds    = []byte("registry_credentials")
	bucketRateLimits       = []byte("rate_limits")
	bucketGHCRAlternatives = []byte("ghcr_alternatives")
	bucketHooks            = []byte("hooks")
	bucketReleaseSources   = []byte("release_sources")

	// Cluster / multi-host
	bucketClusterHosts       = []byte("cluster_hosts")
	bucketClusterTokens      = []byte("cluster_tokens")
	bucketClusterJournal     = []byte("cluster_journal")
	bucketClusterConfigCache = []byte("cluster_config_cache")
	bucketClusterRevoked     = []byte("cluster_revoked")
)

// Cluster settings keys (stored in bucketSettings).
const (
	SettingClusterEnabled      = "cluster_enabled"       // "true" / "false"
	SettingClusterPort         = "cluster_port"          // e.g. "9443"
	SettingClusterGracePeriod  = "cluster_grace_period"  // e.g. "30m"
	SettingClusterRemotePolicy = "cluster_remote_policy" // "auto" / "manual" / "pinned"
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
	Type          string        `json:"type,omitempty"`      // "container" (default) or "service"
	HostID        string        `json:"host_id,omitempty"`   // cluster host (empty = local)
	HostName      string        `json:"host_name,omitempty"` // cluster host name (empty = local)
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
		for _, b := range [][]byte{bucketSnapshots, bucketHistory, bucketState, bucketQueue, bucketPolicies, bucketLogs, bucketSettings, bucketNotifyState, bucketNotifyPrefs, bucketIgnoredVersions, bucketRegistryCreds, bucketRateLimits, bucketGHCRAlternatives, bucketHooks, bucketReleaseSources, bucketClusterHosts, bucketClusterTokens, bucketClusterJournal, bucketClusterConfigCache, bucketClusterRevoked} {
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
// If before is non-empty it is treated as a cursor (RFC3339Nano key) and only
// records older than that key are returned.
func (s *Store) ListHistory(limit int, before string) ([]UpdateRecord, error) {
	var records []UpdateRecord

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHistory)
		c := b.Cursor()

		var k, v []byte
		if before != "" {
			c.Seek([]byte(before))
			k, v = c.Prev()
		} else {
			k, v = c.Last()
		}

		for ; k != nil && len(records) < limit; k, v = c.Prev() {
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

// ListAllHistory returns all update records, newest first.
func (s *Store) ListAllHistory() ([]UpdateRecord, error) {
	var records []UpdateRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHistory)
		c := b.Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
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
	User      string    `json:"user,omitempty"`
	Kind      string    `json:"kind,omitempty"` // "service" or "" (default = container)
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

// CountHistory returns the number of entries in the history bucket.
// Uses bucket stats for O(1) counting.
func (s *Store) CountHistory() (int, error) {
	var count int
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHistory)
		count = b.Stats().KeyN
		return nil
	})
	return count, err
}

// CountSnapshots returns the number of entries in the snapshots bucket.
// Uses bucket stats for O(1) counting.
func (s *Store) CountSnapshots() (int, error) {
	var count int
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSnapshots)
		count = b.Stats().KeyN
		return nil
	})
	return count, err
}

// GetLastContainerScan returns the time a container was last scanned.
// Returns zero time and nil error if never scanned.
func (s *Store) GetLastContainerScan(name string) (time.Time, error) {
	var t time.Time
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSettings)
		v := b.Get([]byte("last_scan_" + name))
		if v == nil {
			return nil
		}
		return t.UnmarshalText(v)
	})
	return t, err
}

// SetLastContainerScan records the time a container was last scanned.
func (s *Store) SetLastContainerScan(name string, t time.Time) error {
	data, err := t.MarshalText()
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSettings)
		return b.Put([]byte("last_scan_"+name), data)
	})
}

// ScopedKey returns a host-scoped key for multi-host store operations.
// If hostID is empty (local containers), returns the bare name unchanged
// for backwards compatibility. Remote containers use "hostID::name".
func ScopedKey(hostID, name string) string {
	if hostID == "" {
		return name
	}
	return hostID + "::" + name
}

// ---------------------------------------------------------------------------
// Cluster host registration
// ---------------------------------------------------------------------------

// SaveClusterHost persists a host registration to the cluster_hosts bucket.
func (s *Store) SaveClusterHost(id string, data []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketClusterHosts).Put([]byte(id), data)
	})
}

// GetClusterHost retrieves a host registration by ID.
func (s *Store) GetClusterHost(id string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketClusterHosts).Get([]byte(id))
		if v != nil {
			data = make([]byte, len(v))
			copy(data, v)
		}
		return nil
	})
	return data, err
}

// ListClusterHosts returns all registered hosts.
func (s *Store) ListClusterHosts() (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketClusterHosts).ForEach(func(k, v []byte) error {
			data := make([]byte, len(v))
			copy(data, v)
			result[string(k)] = data
			return nil
		})
	})
	return result, err
}

// DeleteClusterHost removes a host registration.
func (s *Store) DeleteClusterHost(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketClusterHosts).Delete([]byte(id))
	})
}

// ---------------------------------------------------------------------------
// Enrollment tokens
// ---------------------------------------------------------------------------

// SaveEnrollToken stores an enrollment token (hashed).
func (s *Store) SaveEnrollToken(id string, data []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketClusterTokens).Put([]byte(id), data)
	})
}

// GetEnrollToken retrieves an enrollment token by ID.
func (s *Store) GetEnrollToken(id string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketClusterTokens).Get([]byte(id))
		if v != nil {
			data = make([]byte, len(v))
			copy(data, v)
		}
		return nil
	})
	return data, err
}

// DeleteEnrollToken removes a used or expired token.
func (s *Store) DeleteEnrollToken(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketClusterTokens).Delete([]byte(id))
	})
}

// ---------------------------------------------------------------------------
// Certificate revocation
// ---------------------------------------------------------------------------

// AddRevokedCert adds a certificate serial to the revocation list.
func (s *Store) AddRevokedCert(serial string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketClusterRevoked).Put([]byte(serial), []byte(time.Now().UTC().Format(time.RFC3339)))
	})
}

// IsRevokedCert checks if a certificate serial is revoked.
func (s *Store) IsRevokedCert(serial string) (bool, error) {
	var revoked bool
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketClusterRevoked).Get([]byte(serial))
		revoked = v != nil
		return nil
	})
	return revoked, err
}

// ListRevokedCerts returns all revoked certificate serials with their revocation timestamps.
func (s *Store) ListRevokedCerts() (map[string]string, error) {
	result := make(map[string]string)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketClusterRevoked).ForEach(func(k, v []byte) error {
			result[string(k)] = string(v)
			return nil
		})
	})
	return result, err
}

// ---------------------------------------------------------------------------
// Offline action journal (agent-side)
// ---------------------------------------------------------------------------

// SaveClusterJournal stores an offline action journal entry.
func (s *Store) SaveClusterJournal(id string, data []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketClusterJournal).Put([]byte(id), data)
	})
}

// ListClusterJournal returns all journal entries.
func (s *Store) ListClusterJournal() (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketClusterJournal).ForEach(func(k, v []byte) error {
			data := make([]byte, len(v))
			copy(data, v)
			result[string(k)] = data
			return nil
		})
	})
	return result, err
}

// ClearClusterJournal removes all journal entries (after successful sync).
func (s *Store) ClearClusterJournal() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketClusterJournal)
		return b.ForEach(func(k, _ []byte) error {
			return b.Delete(k)
		})
	})
}

// ---------------------------------------------------------------------------
// Cluster config cache (autonomous mode fallback)
// ---------------------------------------------------------------------------

// SaveClusterConfigCache stores cached settings/policies for autonomous mode.
func (s *Store) SaveClusterConfigCache(key string, data []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketClusterConfigCache).Put([]byte(key), data)
	})
}

// GetClusterConfigCache retrieves a cached config value.
func (s *Store) GetClusterConfigCache(key string) ([]byte, error) {
	var data []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketClusterConfigCache).Get([]byte(key))
		if v != nil {
			data = make([]byte, len(v))
			copy(data, v)
		}
		return nil
	})
	return data, err
}
