package store

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// NotifyState tracks per-container notification deduplication state.
type NotifyState struct {
	LastDigest   string    `json:"last_digest"`
	LastNotified time.Time `json:"last_notified"`
	FirstSeen    time.Time `json:"first_seen"`
}

// NotifyPref holds per-container notification mode preferences.
type NotifyPref struct {
	Mode string `json:"mode"` // "default", "every_scan", "digest_only", "muted"
}

// GetNotifyState loads the notification state for a container.
// Returns nil, nil if no state exists.
func (s *Store) GetNotifyState(name string) (*NotifyState, error) {
	var state *NotifyState
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketNotifyState)
		v := b.Get([]byte(name))
		if v == nil {
			return nil
		}
		state = &NotifyState{}
		return json.Unmarshal(v, state)
	})
	return state, err
}

// SetNotifyState saves the notification state for a container.
func (s *Store) SetNotifyState(name string, state *NotifyState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal notify state: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketNotifyState)
		return b.Put([]byte(name), data)
	})
}

// ClearNotifyState removes the notification state for a container.
// Called after a successful update to reset the deduplication slate.
func (s *Store) ClearNotifyState(name string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketNotifyState)
		return b.Delete([]byte(name))
	})
}

// AllNotifyStates returns all stored notification states.
func (s *Store) AllNotifyStates() (map[string]*NotifyState, error) {
	result := make(map[string]*NotifyState)
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketNotifyState)
		return b.ForEach(func(k, v []byte) error {
			var state NotifyState
			if err := json.Unmarshal(v, &state); err != nil {
				return nil // skip malformed entries
			}
			result[string(k)] = &state
			return nil
		})
	})
	return result, err
}

// GetNotifyPref loads the notification preference for a container.
// Returns nil, nil if no preference is set (falls back to global default).
func (s *Store) GetNotifyPref(name string) (*NotifyPref, error) {
	var pref *NotifyPref
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketNotifyPrefs)
		v := b.Get([]byte(name))
		if v == nil {
			return nil
		}
		pref = &NotifyPref{}
		return json.Unmarshal(v, pref)
	})
	return pref, err
}

// SetNotifyPref saves the notification preference for a container.
func (s *Store) SetNotifyPref(name string, pref *NotifyPref) error {
	data, err := json.Marshal(pref)
	if err != nil {
		return fmt.Errorf("marshal notify pref: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketNotifyPrefs)
		return b.Put([]byte(name), data)
	})
}

// DeleteNotifyPref removes a per-container notification preference,
// causing it to fall back to the global default.
func (s *Store) DeleteNotifyPref(name string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketNotifyPrefs)
		return b.Delete([]byte(name))
	})
}

// AllNotifyPrefs returns all stored per-container notification preferences.
func (s *Store) AllNotifyPrefs() (map[string]*NotifyPref, error) {
	result := make(map[string]*NotifyPref)
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketNotifyPrefs)
		return b.ForEach(func(k, v []byte) error {
			var pref NotifyPref
			if err := json.Unmarshal(v, &pref); err != nil {
				return nil // skip malformed entries
			}
			result[string(k)] = &pref
			return nil
		})
	})
	return result, err
}
