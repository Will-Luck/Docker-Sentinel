package store

import bolt "go.etcd.io/bbolt"

var (
	bucketUsers         = []byte("users")
	bucketSessions      = []byte("sessions")
	bucketRoles         = []byte("roles")
	bucketAPITokens     = []byte("api_tokens")
	bucketWebAuthnCreds = []byte("webauthn_credentials")
)

// EnsureAuthBuckets creates the four auth-related BoltDB buckets if they
// do not already exist. Call this after Open() to initialise auth storage.
func (s *Store) EnsureAuthBuckets() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketUsers, bucketSessions, bucketRoles, bucketAPITokens, bucketWebAuthnCreds} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
}
