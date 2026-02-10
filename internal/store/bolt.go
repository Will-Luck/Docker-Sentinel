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
	bucketSnapshots = []byte("snapshots")
	bucketHistory   = []byte("history")
	bucketState     = []byte("state")
	bucketQueue     = []byte("queue")
)

// UpdateRecord represents a completed (or failed) container update.
type UpdateRecord struct {
	Timestamp     time.Time     `json:"timestamp"`
	ContainerName string        `json:"container_name"`
	OldImage      string        `json:"old_image"`
	OldDigest     string        `json:"old_digest"`
	NewImage      string        `json:"new_image"`
	NewDigest     string        `json:"new_digest"`
	Outcome       string        `json:"outcome"` // "success" or "rollback"
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
		for _, b := range [][]byte{bucketSnapshots, bucketHistory, bucketState, bucketQueue} {
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
