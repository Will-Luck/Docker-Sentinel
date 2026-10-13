package store

import (
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketSwapJournal  = []byte("swap_journal")
	bucketFailedUpdate = []byte("failed_updates")
)

// SwapRecord is the journal entry of a container swap in progress: the old
// container renamed aside while its replacement is created and started. It
// is written before the first change and removed once the swap is committed
// or undone, so a record left behind means the swap did not finish and
// tells recovery exactly which containers are involved.
type SwapRecord struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"` // update, finalise, rollback, consumer
	OldID string `json:"old_id"`
	NewID string `json:"new_id,omitempty"`
	// Token is written before the replacement is created and set on it as
	// the sentinel.swap label, so recovery can recognise the replacement
	// even when its ID never reached the journal.
	Token      string `json:"token,omitempty"`
	OldRunning bool   `json:"old_running"`
	AutoRemove bool   `json:"auto_remove,omitempty"`
	// RestartPolicy and RestartRetries are the old container's restart
	// policy, set to "no" while it is stopped so a reboot cannot start it
	// next to its replacement. RestartChanged says it needs putting back.
	RestartPolicy  string `json:"restart_policy,omitempty"`
	RestartRetries int    `json:"restart_retries,omitempty"`
	RestartChanged bool   `json:"restart_changed,omitempty"`
	// StopTimeout is the timeout, in seconds, given to the old container's
	// stop. Stopping is set while that stop is in flight: the daemon carries
	// on with a stop when Sentinel goes away, and can stop the old container
	// after recovery has put it back. WatchUntil is set when recovery
	// restores an old container that may be stopped that way; the record
	// stays until then, and the container is started again if it stops.
	StopTimeout int       `json:"stop_timeout,omitempty"`
	Stopping    bool      `json:"stopping,omitempty"`
	WatchUntil  time.Time `json:"watch_until,omitzero"`
	// FailedName is the name a failed replacement that could not be removed
	// was renamed to, so the old container could have its name back; the
	// record stays until recovery removes it.
	FailedName string `json:"failed_name,omitempty"`
	// Committed is set once the replacement is accepted; only the removal
	// of the old container is then left to do.
	Committed     bool `json:"committed,omitempty"`
	RemoveVolumes bool `json:"remove_volumes,omitempty"`
	// Snapshot is the old container's inspect JSON, to rebuild it when it
	// is gone.
	Snapshot []byte    `json:"snapshot,omitempty"`
	Started  time.Time `json:"started"`
	// Attempts counts failed recovery attempts; Notified is set once the
	// user has been told the swap needs a hand.
	Attempts int  `json:"attempts,omitempty"`
	Notified bool `json:"notified,omitempty"`
}

// SaveSwap writes or replaces the journal record for rec.Name.
func (s *Store) SaveSwap(rec SwapRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketSwapJournal)
		if err != nil {
			return err
		}
		return b.Put([]byte(rec.Name), data)
	})
}

// DeleteSwap removes the journal record for name, if any, readable or not.
func (s *Store) DeleteSwap(name string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSwapJournal)
		if b == nil {
			return nil
		}
		return b.Delete([]byte(name))
	})
}

// GetSwap returns the journal record for name, if there is one. A record
// that cannot be read still counts as present: found is true, with the
// decoding error.
func (s *Store) GetSwap(name string) (rec SwapRecord, found bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSwapJournal)
		if b == nil {
			return nil
		}
		v := b.Get([]byte(name))
		if v == nil {
			return nil
		}
		found = true
		return json.Unmarshal(v, &rec)
	})
	return rec, found, err
}

// HasSwap reports whether a swap of the container called name is
// journalled.
func (s *Store) HasSwap(name string) bool {
	_, found, _ := s.GetSwap(name)
	return found
}

// ListSwaps returns the readable journal records, sorted by container name.
// A record that cannot be read is logged and left in place, so it can be
// looked at and discarded with DeleteSwap.
func (s *Store) ListSwaps() ([]SwapRecord, error) {
	var recs []SwapRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSwapJournal)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var rec SwapRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				slog.Warn("unreadable container swap record left in the journal", "name", string(k), "error", err)
				return nil
			}
			recs = append(recs, rec)
			return nil
		})
	})
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })
	return recs, err
}

// FailedUpdate identifies an update that was tried and rolled back: the
// target version (empty for a mutable tag) and the registry digest of the
// image that failed.
type FailedUpdate struct {
	Version string `json:"version,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

// SetFailedUpdate records that update f failed for a container and was
// rolled back, so scans can leave that exact update alone.
func (s *Store) SetFailedUpdate(name string, f FailedUpdate) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketFailedUpdate)
		if err != nil {
			return err
		}
		return b.Put([]byte(name), data)
	})
}

// GetFailedUpdate returns the update recorded by SetFailedUpdate, if any.
func (s *Store) GetFailedUpdate(name string) (FailedUpdate, bool, error) {
	var f FailedUpdate
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketFailedUpdate)
		if b == nil {
			return nil
		}
		v := b.Get([]byte(name))
		if v == nil {
			return nil
		}
		found = true
		return json.Unmarshal(v, &f)
	})
	return f, found, err
}

// ListFailedUpdates returns the names of the containers with a recorded
// failed update.
func (s *Store) ListFailedUpdates() ([]string, error) {
	var names []string
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketFailedUpdate)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, _ []byte) error {
			names = append(names, string(k))
			return nil
		})
	})
	return names, err
}

// ClearFailedUpdate removes the failed-update record of a container.
func (s *Store) ClearFailedUpdate(name string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketFailedUpdate)
		if b == nil {
			return nil
		}
		return b.Delete([]byte(name))
	})
}
