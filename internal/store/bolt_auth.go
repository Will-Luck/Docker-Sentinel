package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

// ---- index key helpers ----

func userIndexKey(username string) []byte {
	return []byte("idx::username::" + username)
}

func sessionUserIndexKey(userID, token string) []byte {
	return []byte("idx::user::" + userID + "::" + token)
}

func sessionUserIndexPrefix(userID string) []byte {
	return []byte("idx::user::" + userID + "::")
}

func apiTokenHashIndexKey(hash string) []byte {
	return []byte("idx::hash::" + hash)
}

func apiTokenUserIndexKey(userID, tokenID string) []byte {
	return []byte("idx::user::" + userID + "::" + tokenID)
}

func apiTokenUserIndexPrefix(userID string) []byte {
	return []byte("idx::user::" + userID + "::")
}

var indexPrefix = []byte("idx::")

func isIndexKey(k []byte) bool {
	return bytes.HasPrefix(k, indexPrefix)
}

// ============================================================
// User CRUD
// ============================================================

// CreateUser persists a new user and its username index atomically.
// Returns an error if the username is already taken.
func (s *Store) CreateUser(user auth.User) error {
	data, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("marshal user: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)

		// Ensure username is unique.
		if existing := b.Get(userIndexKey(user.Username)); existing != nil {
			return fmt.Errorf("username %q already exists", user.Username)
		}

		if err := b.Put([]byte(user.ID), data); err != nil {
			return err
		}
		return b.Put(userIndexKey(user.Username), []byte(user.ID))
	})
}

// CreateFirstUser atomically creates the initial user only if no users exist.
// Returns auth.ErrUsersExist if the users bucket already contains records.
func (s *Store) CreateFirstUser(user auth.User) error {
	data, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("marshal user: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)

		// Count non-index keys. If any exist, another user beat us.
		count := 0
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			if !isIndexKey(k) {
				count++
			}
		}
		if count > 0 {
			return auth.ErrUsersExist
		}

		if err := b.Put([]byte(user.ID), data); err != nil {
			return err
		}
		return b.Put(userIndexKey(user.Username), []byte(user.ID))
	})
}

// GetUser retrieves a user by ID.
func (s *Store) GetUser(id string) (*auth.User, error) {
	var user auth.User
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		v := b.Get([]byte(id))
		if v == nil {
			return fmt.Errorf("user %q not found", id)
		}
		return json.Unmarshal(v, &user)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUserByUsername retrieves a user by their unique username.
func (s *Store) GetUserByUsername(username string) (*auth.User, error) {
	var user auth.User
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)

		idBytes := b.Get(userIndexKey(username))
		if idBytes == nil {
			return fmt.Errorf("user with username %q not found", username)
		}

		v := b.Get(idBytes)
		if v == nil {
			return fmt.Errorf("user %q index orphan", username)
		}
		return json.Unmarshal(v, &user)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// UpdateUser updates an existing user record. If the username has changed,
// the secondary index is updated atomically.
func (s *Store) UpdateUser(user auth.User) error {
	data, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("marshal user: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)

		// Fetch existing to detect username change.
		existing := b.Get([]byte(user.ID))
		if existing == nil {
			return fmt.Errorf("user %q not found", user.ID)
		}

		var old auth.User
		if err := json.Unmarshal(existing, &old); err != nil {
			return fmt.Errorf("unmarshal existing user: %w", err)
		}

		// If username changed, rotate the index.
		if old.Username != user.Username {
			// Check the new username isn't taken by someone else.
			if v := b.Get(userIndexKey(user.Username)); v != nil {
				return fmt.Errorf("username %q already exists", user.Username)
			}
			if err := b.Delete(userIndexKey(old.Username)); err != nil {
				return err
			}
			if err := b.Put(userIndexKey(user.Username), []byte(user.ID)); err != nil {
				return err
			}
		}

		return b.Put([]byte(user.ID), data)
	})
}

// DeleteUser removes a user, its username index, and all associated sessions
// and API tokens in a single transaction.
func (s *Store) DeleteUser(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		ub := tx.Bucket(bucketUsers)

		// Fetch user to find the username.
		v := ub.Get([]byte(id))
		if v == nil {
			return fmt.Errorf("user %q not found", id)
		}
		var user auth.User
		if err := json.Unmarshal(v, &user); err != nil {
			return fmt.Errorf("unmarshal user: %w", err)
		}

		// Delete primary record and username index.
		if err := ub.Delete([]byte(id)); err != nil {
			return err
		}
		if err := ub.Delete(userIndexKey(user.Username)); err != nil {
			return err
		}

		// Cascade-delete sessions for this user.
		sb := tx.Bucket(bucketSessions)
		prefix := sessionUserIndexPrefix(id)
		sc := sb.Cursor()
		for k, _ := sc.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = sc.Next() {
			// Extract token from key: idx::user::{userID}::{token}
			token := string(k[len(prefix):])
			keyCopy := make([]byte, len(k))
			copy(keyCopy, k)

			if err := sb.Delete([]byte(token)); err != nil {
				return err
			}
			if err := sb.Delete(keyCopy); err != nil {
				return err
			}
		}

		// Cascade-delete API tokens for this user.
		ab := tx.Bucket(bucketAPITokens)
		aprefix := apiTokenUserIndexPrefix(id)
		ac := ab.Cursor()
		for k, _ := ac.Seek(aprefix); k != nil && bytes.HasPrefix(k, aprefix); k, _ = ac.Next() {
			// Extract token ID from key: idx::user::{userID}::{tokenID}
			tokenID := string(k[len(aprefix):])
			keyCopy := make([]byte, len(k))
			copy(keyCopy, k)

			// Get the API token to find its hash index.
			tv := ab.Get([]byte(tokenID))
			if tv != nil {
				var apiToken auth.APIToken
				if err := json.Unmarshal(tv, &apiToken); err == nil {
					_ = ab.Delete(apiTokenHashIndexKey(apiToken.TokenHash))
				}
			}

			if err := ab.Delete([]byte(tokenID)); err != nil {
				return err
			}
			if err := ab.Delete(keyCopy); err != nil {
				return err
			}
		}

		return nil
	})
}

// ListUsers returns all users (excluding index keys).
func (s *Store) ListUsers() ([]auth.User, error) {
	var users []auth.User
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		return b.ForEach(func(k, v []byte) error {
			if isIndexKey(k) {
				return nil
			}
			var user auth.User
			if err := json.Unmarshal(v, &user); err != nil {
				return nil // skip malformed records
			}
			users = append(users, user)
			return nil
		})
	})
	return users, err
}

// UserCount returns the number of user records (excluding index keys).
func (s *Store) UserCount() (int, error) {
	var count int
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			if !isIndexKey(k) {
				count++
			}
		}
		return nil
	})
	return count, err
}

// ============================================================
// Session CRUD
// ============================================================

// CreateSession persists a session and its user index atomically.
func (s *Store) CreateSession(session auth.Session) error {
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSessions)
		if err := b.Put([]byte(session.Token), data); err != nil {
			return err
		}
		return b.Put(sessionUserIndexKey(session.UserID, session.Token), []byte(""))
	})
}

// GetSession retrieves a session by its token.
func (s *Store) GetSession(token string) (*auth.Session, error) {
	var session auth.Session
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSessions)
		v := b.Get([]byte(token))
		if v == nil {
			return fmt.Errorf("session not found")
		}
		return json.Unmarshal(v, &session)
	})
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// DeleteSession removes a session and its user index entry.
func (s *Store) DeleteSession(token string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSessions)

		// Get session to find userID for index cleanup.
		v := b.Get([]byte(token))
		if v == nil {
			return nil // already gone — idempotent
		}

		var session auth.Session
		if err := json.Unmarshal(v, &session); err != nil {
			// Can't parse — still delete the primary key.
			return b.Delete([]byte(token))
		}

		if err := b.Delete([]byte(token)); err != nil {
			return err
		}
		return b.Delete(sessionUserIndexKey(session.UserID, token))
	})
}

// DeleteSessionsForUser removes all sessions belonging to the given user.
func (s *Store) DeleteSessionsForUser(userID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSessions)
		prefix := sessionUserIndexPrefix(userID)
		c := b.Cursor()

		// Collect keys first — mutating during iteration is unsafe.
		var tokens []string
		var indexKeys [][]byte
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			token := string(k[len(prefix):])
			tokens = append(tokens, token)
			keyCopy := make([]byte, len(k))
			copy(keyCopy, k)
			indexKeys = append(indexKeys, keyCopy)
		}

		for i, token := range tokens {
			if err := b.Delete([]byte(token)); err != nil {
				return err
			}
			if err := b.Delete(indexKeys[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListSessionsForUser returns all sessions belonging to the given user.
func (s *Store) ListSessionsForUser(userID string) ([]auth.Session, error) {
	var sessions []auth.Session
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSessions)
		prefix := sessionUserIndexPrefix(userID)
		c := b.Cursor()

		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			token := string(k[len(prefix):])
			v := b.Get([]byte(token))
			if v == nil {
				continue
			}
			var session auth.Session
			if err := json.Unmarshal(v, &session); err != nil {
				continue
			}
			sessions = append(sessions, session)
		}
		return nil
	})
	return sessions, err
}

// DeleteExpiredSessions removes all sessions whose ExpiresAt is in the past.
// Returns the number of sessions deleted.
func (s *Store) DeleteExpiredSessions() (int, error) {
	var deleted int
	now := time.Now().UTC()

	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSessions)
		c := b.Cursor()

		// Collect expired session keys and their user index keys.
		type expiredEntry struct {
			token    string
			indexKey []byte
		}
		var expired []expiredEntry

		for k, v := c.First(); k != nil; k, v = c.Next() {
			if isIndexKey(k) {
				continue
			}
			var session auth.Session
			if err := json.Unmarshal(v, &session); err != nil {
				continue
			}
			if !session.ExpiresAt.IsZero() && session.ExpiresAt.Before(now) {
				idxKey := sessionUserIndexKey(session.UserID, session.Token)
				expired = append(expired, expiredEntry{
					token:    string(k),
					indexKey: idxKey,
				})
			}
		}

		for _, e := range expired {
			if err := b.Delete([]byte(e.token)); err != nil {
				return err
			}
			if err := b.Delete(e.indexKey); err != nil {
				return err
			}
			deleted++
		}
		return nil
	})
	return deleted, err
}

// ============================================================
// Role CRUD
// ============================================================

// GetRole retrieves a role by ID.
func (s *Store) GetRole(id string) (*auth.Role, error) {
	var role auth.Role
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRoles)
		v := b.Get([]byte(id))
		if v == nil {
			return fmt.Errorf("role %q not found", id)
		}
		return json.Unmarshal(v, &role)
	})
	if err != nil {
		return nil, err
	}
	return &role, nil
}

// ListRoles returns all stored roles.
func (s *Store) ListRoles() ([]auth.Role, error) {
	var roles []auth.Role
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRoles)
		return b.ForEach(func(k, v []byte) error {
			var role auth.Role
			if err := json.Unmarshal(v, &role); err != nil {
				return nil // skip malformed
			}
			roles = append(roles, role)
			return nil
		})
	})
	return roles, err
}

// SeedBuiltinRoles inserts the built-in roles if they don't already exist.
func (s *Store) SeedBuiltinRoles() error {
	roles := auth.BuiltinRoles()
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRoles)
		for _, role := range roles {
			if existing := b.Get([]byte(role.ID)); existing != nil {
				continue // don't overwrite existing roles
			}
			data, err := json.Marshal(role)
			if err != nil {
				return fmt.Errorf("marshal role %q: %w", role.ID, err)
			}
			if err := b.Put([]byte(role.ID), data); err != nil {
				return err
			}
		}
		return nil
	})
}

// ============================================================
// API Token CRUD
// ============================================================

// CreateAPIToken persists an API token with hash and user indexes.
func (s *Store) CreateAPIToken(token auth.APIToken) error {
	data, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("marshal api token: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAPITokens)

		if err := b.Put([]byte(token.ID), data); err != nil {
			return err
		}
		if err := b.Put(apiTokenHashIndexKey(token.TokenHash), []byte(token.ID)); err != nil {
			return err
		}
		return b.Put(apiTokenUserIndexKey(token.UserID, token.ID), []byte(""))
	})
}

// GetAPITokenByHash retrieves an API token by its SHA-256 hash.
func (s *Store) GetAPITokenByHash(hash string) (*auth.APIToken, error) {
	var token auth.APIToken
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAPITokens)

		idBytes := b.Get(apiTokenHashIndexKey(hash))
		if idBytes == nil {
			return fmt.Errorf("api token not found")
		}

		v := b.Get(idBytes)
		if v == nil {
			return fmt.Errorf("api token index orphan for hash %q", hash)
		}
		return json.Unmarshal(v, &token)
	})
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// DeleteAPIToken removes an API token and all its indexes.
func (s *Store) DeleteAPIToken(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAPITokens)

		v := b.Get([]byte(id))
		if v == nil {
			return nil // already gone — idempotent
		}

		var token auth.APIToken
		if err := json.Unmarshal(v, &token); err != nil {
			// Can't parse — still delete the primary key.
			return b.Delete([]byte(id))
		}

		if err := b.Delete([]byte(id)); err != nil {
			return err
		}
		if err := b.Delete(apiTokenHashIndexKey(token.TokenHash)); err != nil {
			return err
		}
		return b.Delete(apiTokenUserIndexKey(token.UserID, token.ID))
	})
}

// ListAPITokensForUser returns all API tokens belonging to the given user.
func (s *Store) ListAPITokensForUser(userID string) ([]auth.APIToken, error) {
	var tokens []auth.APIToken
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAPITokens)
		prefix := apiTokenUserIndexPrefix(userID)
		c := b.Cursor()

		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			tokenID := string(k[len(prefix):])
			v := b.Get([]byte(tokenID))
			if v == nil {
				continue
			}
			var token auth.APIToken
			if err := json.Unmarshal(v, &token); err != nil {
				continue
			}
			tokens = append(tokens, token)
		}
		return nil
	})
	return tokens, err
}
