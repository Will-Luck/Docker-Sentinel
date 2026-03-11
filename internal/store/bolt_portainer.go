package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// PortainerInstance represents a configured Portainer server.
// Each instance has its own URL, API token, and per-endpoint settings.
type PortainerInstance struct {
	ID        string                    `json:"id"`
	Name      string                    `json:"name"`
	URL       string                    `json:"url"`
	Token     string                    `json:"token"`
	Enabled   bool                      `json:"enabled"`
	Endpoints map[string]EndpointConfig `json:"endpoints,omitempty"`
}

// EndpointConfig stores per-endpoint user/auto settings.
// An endpoint can be individually enabled/disabled or blocked
// (e.g. if the Portainer API reports it as unreachable).
type EndpointConfig struct {
	Enabled bool   `json:"enabled"`
	Blocked bool   `json:"blocked,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// SavePortainerInstance upserts a Portainer instance record.
// The instance ID is used as the BoltDB key.
func (s *Store) SavePortainerInstance(inst PortainerInstance) error {
	data, err := json.Marshal(inst)
	if err != nil {
		return fmt.Errorf("marshal portainer instance: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketPortainerInstances)
		if err != nil {
			return err
		}
		return b.Put([]byte(inst.ID), data)
	})
}

// GetPortainerInstance loads a single instance by ID.
// Returns an error if the instance does not exist.
func (s *Store) GetPortainerInstance(id string) (PortainerInstance, error) {
	var inst PortainerInstance
	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketPortainerInstances)
		if err != nil {
			return err
		}
		v := b.Get([]byte(id))
		if v == nil {
			return fmt.Errorf("portainer instance %q not found", id)
		}
		return json.Unmarshal(v, &inst)
	})
	return inst, err
}

// ListPortainerInstances returns all configured instances, sorted by ID.
func (s *Store) ListPortainerInstances() ([]PortainerInstance, error) {
	var instances []PortainerInstance
	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketPortainerInstances)
		if err != nil {
			return err
		}
		return b.ForEach(func(k, v []byte) error {
			var inst PortainerInstance
			if err := json.Unmarshal(v, &inst); err != nil {
				return err
			}
			instances = append(instances, inst)
			return nil
		})
	})
	// BoltDB iterates in key order which is already lexicographic,
	// but sort explicitly to be safe if IDs aren't purely sequential.
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].ID < instances[j].ID
	})
	return instances, err
}

// DeletePortainerInstance removes an instance by ID.
// Deleting a non-existent ID is a silent no-op (BoltDB behaviour).
func (s *Store) DeletePortainerInstance(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketPortainerInstances)
		if err != nil {
			return err
		}
		return b.Delete([]byte(id))
	})
}

// NextPortainerID returns the next available instance ID (p1, p2, ...).
// Scans existing keys to find the highest numeric suffix and increments.
func (s *Store) NextPortainerID() (string, error) {
	var maxNum int
	err := s.db.View(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketPortainerInstances)
		if err != nil {
			return err
		}
		return b.ForEach(func(k, _ []byte) error {
			key := string(k)
			if strings.HasPrefix(key, "p") {
				if n, err := strconv.Atoi(key[1:]); err == nil && n > maxNum {
					maxNum = n
				}
			}
			return nil
		})
	})
	return fmt.Sprintf("p%d", maxNum+1), err
}
