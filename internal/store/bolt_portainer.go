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

// MigratePortainerSettings converts old flat portainer_url/portainer_token
// settings into a PortainerInstance record. Returns true if migration occurred.
// Safe to call multiple times: skips if instances already exist.
func (s *Store) MigratePortainerSettings() (bool, error) {
	// Check if already migrated (instances exist).
	existing, err := s.ListPortainerInstances()
	if err != nil {
		return false, fmt.Errorf("list instances: %w", err)
	}
	if len(existing) > 0 {
		return false, nil
	}

	// Check for old settings.
	url, _ := s.LoadSetting(SettingPortainerURL)
	token, _ := s.LoadSetting(SettingPortainerToken)
	if url == "" && token == "" {
		return false, nil
	}

	enabled, _ := s.LoadSetting(SettingPortainerEnabled)

	inst := PortainerInstance{
		ID:      "p1",
		Name:    "Portainer",
		URL:     url,
		Token:   token,
		Enabled: enabled == "true",
	}
	if err := s.SavePortainerInstance(inst); err != nil {
		return false, fmt.Errorf("save migrated instance: %w", err)
	}

	// Clear old settings.
	_ = s.DeleteSetting(SettingPortainerURL)
	_ = s.DeleteSetting(SettingPortainerToken)
	_ = s.DeleteSetting(SettingPortainerEnabled)

	return true, nil
}

// MigratePortainerKeys rewrites HostID fields in queue and history entries
// from the old format "portainer:{epID}" to "portainer:{instanceID}:{epID}".
// Queue: single JSON array under key "pending" — unmarshal, rewrite HostIDs, re-save.
// History: each value is a JSON UpdateRecord — rewrite HostID in each.
func (s *Store) MigratePortainerKeys(instanceID string) error {
	prefix := "portainer:"
	newPrefix := "portainer:" + instanceID + ":"

	// --- Queue migration ---
	data, err := s.LoadPendingQueue()
	if err != nil {
		return fmt.Errorf("load queue: %w", err)
	}
	if data != nil {
		var items []json.RawMessage
		if json.Unmarshal(data, &items) == nil {
			changed := false
			for i, raw := range items {
				var m map[string]interface{}
				if json.Unmarshal(raw, &m) != nil {
					continue
				}
				hostID, _ := m["host_id"].(string)
				if !strings.HasPrefix(hostID, prefix) {
					continue
				}
				rest := hostID[len(prefix):]
				// Skip already-migrated entries (new format has instanceID:epID, i.e. contains a colon).
				if strings.Contains(rest, ":") {
					continue
				}
				m["host_id"] = newPrefix + rest
				rewritten, err := json.Marshal(m)
				if err != nil {
					continue
				}
				items[i] = rewritten
				changed = true
			}
			if changed {
				newData, err := json.Marshal(items)
				if err != nil {
					return fmt.Errorf("marshal queue: %w", err)
				}
				if err := s.SavePendingQueue(newData); err != nil {
					return fmt.Errorf("save queue: %w", err)
				}
			}
		}
	}

	// --- History migration ---
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := bucket(tx, bucketHistory)
		if err != nil {
			return err
		}
		type kv struct{ key, val []byte }
		var rewrites []kv
		if err := b.ForEach(func(k, v []byte) error {
			var rec UpdateRecord
			if json.Unmarshal(v, &rec) != nil {
				return nil
			}
			if !strings.HasPrefix(rec.HostID, prefix) {
				return nil
			}
			rest := rec.HostID[len(prefix):]
			if strings.Contains(rest, ":") {
				return nil // already migrated
			}
			rec.HostID = newPrefix + rest
			newVal, err := json.Marshal(rec)
			if err != nil {
				return nil
			}
			keyCopy := make([]byte, len(k))
			copy(keyCopy, k)
			rewrites = append(rewrites, kv{key: keyCopy, val: newVal})
			return nil
		}); err != nil {
			return err
		}
		for _, rw := range rewrites {
			if err := b.Put(rw.key, rw.val); err != nil {
				return err
			}
		}
		return nil
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
