package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
)

// NotifyState tracks per-container notification deduplication state.
type NotifyState struct {
	LastDigest   string    `json:"last_digest"`
	LastNotified time.Time `json:"last_notified"`
	FirstSeen    time.Time `json:"first_seen"`
	SnoozedUntil time.Time `json:"snoozed_until,omitempty"`
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
				slog.Warn("corrupt entry in notify_state bucket, skipping", "key", string(k), "error", err)
				return nil
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
				slog.Warn("corrupt entry in notify_prefs bucket, skipping", "key", string(k), "error", err)
				return nil
			}
			result[string(k)] = &pref
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
