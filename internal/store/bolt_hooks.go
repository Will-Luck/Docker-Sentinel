package store

import (
	"bytes"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// HookEntry is the store representation of a lifecycle hook.
type HookEntry struct {
	ContainerName string   `json:"container_name"`
	Phase         string   `json:"phase"`
	Command       []string `json:"command"`
	Timeout       int      `json:"timeout"`
}

// ListHooks returns all hooks for a container.
func (s *Store) ListHooks(containerName string) ([]HookEntry, error) {
	var entries []HookEntry
	prefix := []byte(containerName + "::")

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHooks)
		c := b.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var entry HookEntry
			if err := json.Unmarshal(v, &entry); err != nil {
				continue
			}
			entries = append(entries, entry)
		}
		return nil
	})
	return entries, err
}

// SaveHook saves or updates a hook for a container.
func (s *Store) SaveHook(hook HookEntry) error {
	data, err := json.Marshal(hook)
	if err != nil {
		return fmt.Errorf("marshal hook: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHooks)
		key := []byte(hook.ContainerName + "::" + hook.Phase)
		return b.Put(key, data)
	})
}

// DeleteHook removes a hook for a container.
func (s *Store) DeleteHook(containerName, phase string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketHooks)
		key := []byte(containerName + "::" + phase)
		return b.Delete(key)
	})
}
