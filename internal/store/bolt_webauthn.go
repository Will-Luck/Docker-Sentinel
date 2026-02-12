package store

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

// ---- WebAuthn index key helpers ----

func webauthnCredKey(credID []byte) []byte {
	return []byte(base64.RawURLEncoding.EncodeToString(credID))
}

func webauthnUserIndexKey(userID string, credID []byte) []byte {
	return []byte("idx::user::" + userID + "::" + base64.RawURLEncoding.EncodeToString(credID))
}

func webauthnUserIndexPrefix(userID string) []byte {
	return []byte("idx::user::" + userID + "::")
}

func webauthnHandleIndexKey(handle []byte) []byte {
	return []byte("idx::handle::" + base64.RawURLEncoding.EncodeToString(handle))
}

// CreateWebAuthnCredential stores a credential and its indexes.
func (s *Store) CreateWebAuthnCredential(cred auth.WebAuthnCredential) error {
	data, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("marshal webauthn credential: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWebAuthnCreds)
		if err := b.Put(webauthnCredKey(cred.ID), data); err != nil {
			return err
		}
		if err := b.Put(webauthnUserIndexKey(cred.UserID, cred.ID), []byte("")); err != nil {
			return err
		}
		// Store the handle->userID index for discoverable login.
		// Look up the user to get their WebAuthnUserID.
		ub := tx.Bucket(bucketUsers)
		uv := ub.Get([]byte(cred.UserID))
		if uv != nil {
			var user auth.User
			if err := json.Unmarshal(uv, &user); err == nil && len(user.WebAuthnUserID) > 0 {
				if err := b.Put(webauthnHandleIndexKey(user.WebAuthnUserID), []byte(cred.UserID)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// GetWebAuthnCredential retrieves a credential by its ID.
func (s *Store) GetWebAuthnCredential(credID []byte) (*auth.WebAuthnCredential, error) {
	var cred auth.WebAuthnCredential
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWebAuthnCreds)
		v := b.Get(webauthnCredKey(credID))
		if v == nil {
			return auth.ErrCredentialNotFound
		}
		return json.Unmarshal(v, &cred)
	})
	if err != nil {
		return nil, err
	}
	return &cred, nil
}

// ListWebAuthnCredentialsForUser returns all credentials for a user.
func (s *Store) ListWebAuthnCredentialsForUser(userID string) ([]auth.WebAuthnCredential, error) {
	var creds []auth.WebAuthnCredential
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWebAuthnCreds)
		prefix := webauthnUserIndexPrefix(userID)
		c := b.Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			// Extract credID from the index key.
			credB64 := string(k[len(prefix):])
			credIDBytes, err := base64.RawURLEncoding.DecodeString(credB64)
			if err != nil {
				continue
			}
			v := b.Get(webauthnCredKey(credIDBytes))
			if v == nil {
				continue
			}
			var cred auth.WebAuthnCredential
			if err := json.Unmarshal(v, &cred); err != nil {
				continue
			}
			creds = append(creds, cred)
		}
		return nil
	})
	return creds, err
}

// DeleteWebAuthnCredential removes a credential and its indexes.
func (s *Store) DeleteWebAuthnCredential(credID []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWebAuthnCreds)
		key := webauthnCredKey(credID)
		v := b.Get(key)
		if v == nil {
			return nil // idempotent
		}
		var cred auth.WebAuthnCredential
		if err := json.Unmarshal(v, &cred); err != nil {
			return b.Delete(key)
		}
		if err := b.Delete(key); err != nil {
			return err
		}
		_ = b.Delete(webauthnUserIndexKey(cred.UserID, cred.ID))
		// Note: we don't delete the handle index here because the user may have other credentials.
		// The handle index is re-checked at login time.
		return nil
	})
}

// GetUserByWebAuthnHandle looks up a user by WebAuthn user handle (for discoverable login).
func (s *Store) GetUserByWebAuthnHandle(handle []byte) (*auth.User, error) {
	var user auth.User
	err := s.db.View(func(tx *bolt.Tx) error {
		wb := tx.Bucket(bucketWebAuthnCreds)
		userIDBytes := wb.Get(webauthnHandleIndexKey(handle))
		if userIDBytes == nil {
			return auth.ErrCredentialNotFound
		}
		ub := tx.Bucket(bucketUsers)
		v := ub.Get(userIDBytes)
		if v == nil {
			return auth.ErrCredentialNotFound
		}
		return json.Unmarshal(v, &user)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// AnyWebAuthnCredentialsExist checks if any passkeys are registered system-wide.
func (s *Store) AnyWebAuthnCredentialsExist() (bool, error) {
	var exists bool
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketWebAuthnCreds)
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			if !isIndexKey(k) {
				exists = true
				return nil
			}
		}
		return nil
	})
	return exists, err
}
