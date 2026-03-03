package store

import (
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// PortConfig holds per-port URL overrides for a container.
type PortConfig struct {
	Ports map[string]PortOverride `json:"ports"` // key: host port as string
}

// PortOverride configures a custom URL or path suffix for a single port.
type PortOverride struct {
	URL  string `json:"url,omitempty"`  // full URL override
	Path string `json:"path,omitempty"` // path suffix appended to resolved URL
}

// GetPortConfig returns the port configuration for a container.
// Returns nil, nil if no configuration exists.
func (s *Store) GetPortConfig(name string) (*PortConfig, error) {
	var cfg *PortConfig
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPortConfig)
		v := b.Get([]byte(name))
		if v == nil {
			return nil
		}
		data := make([]byte, len(v))
		copy(data, v)
		cfg = &PortConfig{}
		return json.Unmarshal(data, cfg)
	})
	if err != nil {
		return nil, fmt.Errorf("get port config for %s: %w", name, err)
	}
	return cfg, nil
}

// SetPortOverride sets a URL override for a specific port on a container.
// Creates a new PortConfig if one doesn't exist.
func (s *Store) SetPortOverride(name string, hostPort uint16, override PortOverride) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPortConfig)
		key := []byte(name)
		portKey := fmt.Sprintf("%d", hostPort)

		// Load existing config or create new.
		cfg := &PortConfig{Ports: make(map[string]PortOverride)}
		if v := b.Get(key); v != nil {
			data := make([]byte, len(v))
			copy(data, v)
			if err := json.Unmarshal(data, cfg); err != nil {
				return fmt.Errorf("unmarshal port config for %s: %w", name, err)
			}
			if cfg.Ports == nil {
				cfg.Ports = make(map[string]PortOverride)
			}
		}

		cfg.Ports[portKey] = override

		data, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("marshal port config for %s: %w", name, err)
		}
		return b.Put(key, data)
	})
}

// DeletePortOverride removes the URL override for a specific port on a container.
// If no ports remain, the entire entry is deleted.
func (s *Store) DeletePortOverride(name string, hostPort uint16) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPortConfig)
		key := []byte(name)
		portKey := fmt.Sprintf("%d", hostPort)

		v := b.Get(key)
		if v == nil {
			return nil
		}

		data := make([]byte, len(v))
		copy(data, v)

		var cfg PortConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("unmarshal port config for %s: %w", name, err)
		}

		delete(cfg.Ports, portKey)

		// If no ports left, remove the entire entry.
		if len(cfg.Ports) == 0 {
			return b.Delete(key)
		}

		out, err := json.Marshal(&cfg)
		if err != nil {
			return fmt.Errorf("marshal port config for %s: %w", name, err)
		}
		return b.Put(key, out)
	})
}

// AllPortConfigs returns all stored port configurations, keyed by container name.
func (s *Store) AllPortConfigs() (map[string]*PortConfig, error) {
	result := make(map[string]*PortConfig)
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPortConfig).ForEach(func(k, v []byte) error {
			data := make([]byte, len(v))
			copy(data, v)
			var cfg PortConfig
			if err := json.Unmarshal(data, &cfg); err != nil {
				return fmt.Errorf("unmarshal port config for %s: %w", string(k), err)
			}
			result[string(k)] = &cfg
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
