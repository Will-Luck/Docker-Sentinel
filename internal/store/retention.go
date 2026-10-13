package store

import (
	"encoding/json"
	"time"

	bolt "go.etcd.io/bbolt"
)

// SettingHistoryRetentionDays is how many days of history and activity log
// to keep; 0 keeps everything. It overrides SENTINEL_HISTORY_RETENTION_DAYS.
const SettingHistoryRetentionDays = "history_retention_days"

// DefaultHistoryRetentionDays is used when neither the setting nor the
// environment variable is set.
const DefaultHistoryRetentionDays = 90

// keptOutcome reports history records that describe a real change to a
// container (partial means the new image is running but finalising failed).
// They are small and rare, and rollback and image-history lookups rely on
// them, so retention never removes them.
func keptOutcome(outcome string) bool {
	return outcome == "success" || outcome == "rollback" || outcome == "partial"
}

// PruneHistory deletes history records older than cutoff, except records of
// applied updates and rollbacks (the scan summaries and per-scan noise are the
// bulk). It returns how many records were removed.
func (s *Store) PruneHistory(cutoff time.Time) (int, error) {
	return s.pruneBucket(bucketHistory, cutoff, func(v []byte) (time.Time, bool) {
		var rec UpdateRecord
		if json.Unmarshal(v, &rec) != nil {
			return time.Time{}, false
		}
		return rec.Timestamp, !keptOutcome(rec.Outcome)
	})
}

// PruneLogs deletes activity-log entries older than cutoff.
func (s *Store) PruneLogs(cutoff time.Time) (int, error) {
	return s.pruneBucket(bucketLogs, cutoff, func(v []byte) (time.Time, bool) {
		var e LogEntry
		if json.Unmarshal(v, &e) != nil {
			return time.Time{}, false
		}
		return e.Timestamp, true
	})
}

// pruneBucket removes entries whose timestamp is before cutoff. The age comes
// from the stored value, not the key, because log keys carry a local offset.
// Deletes run in batches so one huge transaction never holds the write lock.
func (s *Store) pruneBucket(name []byte, cutoff time.Time, inspect func([]byte) (time.Time, bool)) (int, error) {
	const batch = 2000
	total := 0
	for {
		var doomed [][]byte
		err := s.db.View(func(tx *bolt.Tx) error {
			b, err := bucket(tx, name)
			if err != nil {
				return err
			}
			return b.ForEach(func(k, v []byte) error {
				if len(doomed) >= batch {
					return nil
				}
				if ts, removable := inspect(v); removable && !ts.IsZero() && ts.Before(cutoff) {
					doomed = append(doomed, append([]byte(nil), k...))
				}
				return nil
			})
		})
		if err != nil {
			return total, err
		}
		if len(doomed) == 0 {
			return total, nil
		}
		err = s.db.Update(func(tx *bolt.Tx) error {
			b, err := bucket(tx, name)
			if err != nil {
				return err
			}
			for _, k := range doomed {
				if err := b.Delete(k); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		total += len(doomed)
		if len(doomed) < batch {
			return total, nil
		}
	}
}
