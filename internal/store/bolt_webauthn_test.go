package store

import (
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

func testStoreWithAuth(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	if err := s.EnsureAuthBuckets(); err != nil {
		t.Fatalf("EnsureAuthBuckets: %v", err)
	}
	return s
}

func TestWebAuthnCreateAndGet(t *testing.T) {
	s := testStoreWithAuth(t)

	cred := auth.WebAuthnCredential{
		ID:              []byte("cred-abc-123"),
		PublicKey:       []byte("pubkey-data"),
		AttestationType: "none",
		Transport:       []string{"internal"},
		Flags: auth.WebAuthnFlags{
			UserPresent:  true,
			UserVerified: true,
		},
		Authenticator: auth.WebAuthnAuthenticator{
			AAGUID:    []byte("aaguid-data"),
			SignCount: 1,
		},
		UserID:    "user1",
		Name:      "MacBook Touch ID",
		CreatedAt: time.Now().UTC(),
	}

	if err := s.CreateWebAuthnCredential(cred); err != nil {
		t.Fatalf("CreateWebAuthnCredential: %v", err)
	}

	got, err := s.GetWebAuthnCredential([]byte("cred-abc-123"))
	if err != nil {
		t.Fatalf("GetWebAuthnCredential: %v", err)
	}
	if got.Name != "MacBook Touch ID" {
		t.Errorf("expected name %q, got %q", "MacBook Touch ID", got.Name)
	}
	if got.UserID != "user1" {
		t.Errorf("expected userID %q, got %q", "user1", got.UserID)
	}
	if got.AttestationType != "none" {
		t.Errorf("expected attestation type %q, got %q", "none", got.AttestationType)
	}
	if !got.Flags.UserPresent {
		t.Error("expected UserPresent flag to be true")
	}
}

func TestWebAuthnGetNotFound(t *testing.T) {
	s := testStoreWithAuth(t)

	_, err := s.GetWebAuthnCredential([]byte("nonexistent"))
	if err != auth.ErrCredentialNotFound {
		t.Errorf("expected ErrCredentialNotFound, got %v", err)
	}
}

func TestWebAuthnListForUser(t *testing.T) {
	s := testStoreWithAuth(t)

	cred1 := auth.WebAuthnCredential{
		ID:        []byte("cred-001"),
		UserID:    "user1",
		Name:      "Key 1",
		CreatedAt: time.Now().UTC(),
	}
	cred2 := auth.WebAuthnCredential{
		ID:        []byte("cred-002"),
		UserID:    "user1",
		Name:      "Key 2",
		CreatedAt: time.Now().UTC(),
	}
	cred3 := auth.WebAuthnCredential{
		ID:        []byte("cred-003"),
		UserID:    "user2",
		Name:      "Other User Key",
		CreatedAt: time.Now().UTC(),
	}

	for _, c := range []auth.WebAuthnCredential{cred1, cred2, cred3} {
		if err := s.CreateWebAuthnCredential(c); err != nil {
			t.Fatalf("CreateWebAuthnCredential(%s): %v", c.Name, err)
		}
	}

	// User1 should have 2 credentials.
	creds, err := s.ListWebAuthnCredentialsForUser("user1")
	if err != nil {
		t.Fatalf("ListWebAuthnCredentialsForUser: %v", err)
	}
	if len(creds) != 2 {
		t.Fatalf("expected 2 credentials for user1, got %d", len(creds))
	}

	// User2 should have 1 credential.
	creds, err = s.ListWebAuthnCredentialsForUser("user2")
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 {
		t.Fatalf("expected 1 credential for user2, got %d", len(creds))
	}

	// User3 should have 0 credentials.
	creds, err = s.ListWebAuthnCredentialsForUser("user3")
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 0 {
		t.Errorf("expected 0 credentials for user3, got %d", len(creds))
	}
}

func TestWebAuthnDelete(t *testing.T) {
	s := testStoreWithAuth(t)

	cred := auth.WebAuthnCredential{
		ID:        []byte("cred-to-delete"),
		UserID:    "user1",
		Name:      "Ephemeral Key",
		CreatedAt: time.Now().UTC(),
	}

	if err := s.CreateWebAuthnCredential(cred); err != nil {
		t.Fatal(err)
	}

	// Verify it exists.
	got, err := s.GetWebAuthnCredential([]byte("cred-to-delete"))
	if err != nil {
		t.Fatalf("GetWebAuthnCredential: %v", err)
	}
	if got == nil {
		t.Fatal("expected credential to exist before delete")
	}

	// Delete it.
	if err := s.DeleteWebAuthnCredential([]byte("cred-to-delete")); err != nil {
		t.Fatalf("DeleteWebAuthnCredential: %v", err)
	}

	// Verify it's gone.
	_, err = s.GetWebAuthnCredential([]byte("cred-to-delete"))
	if err != auth.ErrCredentialNotFound {
		t.Errorf("expected ErrCredentialNotFound after delete, got %v", err)
	}

	// Should not appear in list.
	creds, err := s.ListWebAuthnCredentialsForUser("user1")
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 0 {
		t.Errorf("expected 0 credentials after delete, got %d", len(creds))
	}
}

func TestWebAuthnDeleteIdempotent(t *testing.T) {
	s := testStoreWithAuth(t)

	// Deleting a non-existent credential should not error.
	if err := s.DeleteWebAuthnCredential([]byte("nonexistent")); err != nil {
		t.Errorf("expected nil error for idempotent delete, got %v", err)
	}
}

func TestWebAuthnGetUserByHandle(t *testing.T) {
	s := testStoreWithAuth(t)

	// Create a user with a WebAuthn user ID.
	user := auth.User{
		ID:             "user1",
		Username:       "admin",
		PasswordHash:   "hash",
		RoleID:         "admin",
		WebAuthnUserID: []byte("handle-64-bytes-padded-for-testing-purposes-01234567890123456789"),
		CreatedAt:      time.Now().UTC(),
	}
	if err := s.CreateUser(user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create a credential for this user (triggers handle index creation).
	cred := auth.WebAuthnCredential{
		ID:        []byte("cred-handle-test"),
		UserID:    "user1",
		Name:      "Handle Test Key",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateWebAuthnCredential(cred); err != nil {
		t.Fatalf("CreateWebAuthnCredential: %v", err)
	}

	// Look up by handle.
	got, err := s.GetUserByWebAuthnHandle(user.WebAuthnUserID)
	if err != nil {
		t.Fatalf("GetUserByWebAuthnHandle: %v", err)
	}
	if got.ID != "user1" {
		t.Errorf("expected user ID %q, got %q", "user1", got.ID)
	}
	if got.Username != "admin" {
		t.Errorf("expected username %q, got %q", "admin", got.Username)
	}
}

func TestWebAuthnGetUserByHandleNotFound(t *testing.T) {
	s := testStoreWithAuth(t)

	_, err := s.GetUserByWebAuthnHandle([]byte("no-such-handle"))
	if err != auth.ErrCredentialNotFound {
		t.Errorf("expected ErrCredentialNotFound, got %v", err)
	}
}

func TestWebAuthnAnyCredentialsExist(t *testing.T) {
	s := testStoreWithAuth(t)

	// Empty bucket.
	exists, err := s.AnyWebAuthnCredentialsExist()
	if err != nil {
		t.Fatalf("AnyWebAuthnCredentialsExist: %v", err)
	}
	if exists {
		t.Error("expected false for empty bucket")
	}

	// Add a credential.
	cred := auth.WebAuthnCredential{
		ID:        []byte("any-test-cred"),
		UserID:    "user1",
		Name:      "Existence Test",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateWebAuthnCredential(cred); err != nil {
		t.Fatal(err)
	}

	exists, err = s.AnyWebAuthnCredentialsExist()
	if err != nil {
		t.Fatalf("AnyWebAuthnCredentialsExist: %v", err)
	}
	if !exists {
		t.Error("expected true after adding a credential")
	}
}
