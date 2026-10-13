package store

import (
	"encoding/json"
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
// or undone, so a record left at startup means Sentinel stopped part way
// and tells recovery exactly which containers are involved.
type SwapRecord struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"` // update, finalise, rollback, consumer
	OldID      string `json:"old_id"`
	NewID      string `json:"new_id,omitempty"`
	OldRunning bool   `json:"old_running"`
	AutoRemove bool   `json:"auto_remove,omitempty"`
	// RestartPolicy and RestartRetries are the old container's restart
	// policy, set to "no" while it is stopped so a reboot cannot start it
	// next to its replacement. RestartChanged says it needs putting back.
	RestartPolicy  string `json:"restart_policy,omitempty"`
	RestartRetries int    `json:"restart_retries,omitempty"`
	RestartChanged bool   `json:"restart_changed,omitempty"`
	// Committed is set once the replacement is accepted; only the removal
	// of the old container is then left to do.
	Committed     bool `json:"committed,omitempty"`
	RemoveVolumes bool `json:"remove_volumes,omitempty"`
	// Snapshot is the old container's inspect JSON, to rebuild it when
	// both containers are gone.
	Snapshot []byte    `json:"snapshot,omitempty"`
	Started  time.Time `json:"started"`
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

// DeleteSwap removes the journal record for name, if any.
func (s *Store) DeleteSwap(name string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSwapJournal)
		if b == nil {
			return nil
		}
		return b.Delete([]byte(name))
	})
}

// ListSwaps returns the journal records, sorted by container name.
func (s *Store) ListSwaps() ([]SwapRecord, error) {
	var recs []SwapRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSwapJournal)
		if b == nil {
			return nil
		}
		return b.ForEach(func(_, v []byte) error {
			var rec SwapRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return nil // skip a record this version cannot read
			}
			recs = append(recs, rec)
			return nil
		})
	})
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })
	return recs, err
}

// SetFailedUpdate records that the update identified by key (a target
// version or a remote digest) failed for a container and was rolled back,
// so scans can leave that exact update alone.
func (s *Store) SetFailedUpdate(name, key string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketFailedUpdate)
		if err != nil {
			return err
		}
		return b.Put([]byte(name), []byte(key))
	})
}

// FailedUpdate returns the key recorded by SetFailedUpdate, or "".
func (s *Store) FailedUpdate(name string) (string, error) {
	var key string
	err := s.db.View(func(tx *bolt.Tx) error {
		if b := tx.Bucket(bucketFailedUpdate); b != nil {
			key = string(b.Get([]byte(name)))
		}
		return nil
	})
	return key, err
}

// ClearFailedUpdate removes the failed-update marker for a container.
func (s *Store) ClearFailedUpdate(name string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketFailedUpdate)
		if b == nil {
			return nil
		}
		return b.Delete([]byte(name))
	})
}
