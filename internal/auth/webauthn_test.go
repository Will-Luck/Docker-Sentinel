package auth

import (
	"context"
	"testing"
	"time"
)

func TestCeremonyStore_PutGet(t *testing.T) {
	cs := NewCeremonyStore()

	cs.Put("reg::user1", "session-data", "user1")

	got := cs.Get("reg::user1")
	if got == nil {
		t.Fatal("expected ceremony data, got nil")
	}
	if got.Data != "session-data" {
		t.Errorf("expected data %q, got %q", "session-data", got.Data)
	}
	if got.UserID != "user1" {
		t.Errorf("expected userID %q, got %q", "user1", got.UserID)
	}
}

func TestCeremonyStore_GetRemoves(t *testing.T) {
	cs := NewCeremonyStore()

	cs.Put("key1", "data1", "user1")

	// First Get should succeed.
	got := cs.Get("key1")
	if got == nil {
		t.Fatal("expected ceremony data on first Get, got nil")
	}

	// Second Get should return nil (already consumed).
	got = cs.Get("key1")
	if got != nil {
		t.Error("expected nil on second Get, got data")
	}
}

func TestCeremonyStore_NotFound(t *testing.T) {
	cs := NewCeremonyStore()

	got := cs.Get("nonexistent")
	if got != nil {
		t.Error("expected nil for nonexistent key, got data")
	}
}

func TestCeremonyStore_Expiry(t *testing.T) {
	cs := &CeremonyStore{items: make(map[string]CeremonyData)}

	// Insert with an already-expired time.
	cs.mu.Lock()
	cs.items["expired"] = CeremonyData{
		Data:      "old-data",
		UserID:    "user1",
		ExpiresAt: time.Now().Add(-1 * time.Second),
	}
	cs.mu.Unlock()

	got := cs.Get("expired")
	if got != nil {
		t.Error("expected nil for expired ceremony, got data")
	}
}

func TestUser_EnsureWebAuthnUserID(t *testing.T) {
	t.Run("generates when empty", func(t *testing.T) {
		u := &User{ID: "test-user"}
		generated, err := u.EnsureWebAuthnUserID()
		if err != nil {
			t.Fatalf("EnsureWebAuthnUserID failed: %v", err)
		}
		if !generated {
			t.Error("expected generated=true for empty WebAuthnUserID")
		}
		if len(u.WebAuthnUserID) != 64 {
			t.Errorf("expected 64-byte ID, got %d bytes", len(u.WebAuthnUserID))
		}
	})

	t.Run("no-op when already set", func(t *testing.T) {
		existing := make([]byte, 64)
		existing[0] = 0xAB
		u := &User{ID: "test-user", WebAuthnUserID: existing}

		generated, err := u.EnsureWebAuthnUserID()
		if err != nil {
			t.Fatalf("EnsureWebAuthnUserID failed: %v", err)
		}
		if generated {
			t.Error("expected generated=false when ID already set")
		}
		if u.WebAuthnUserID[0] != 0xAB {
			t.Error("existing WebAuthnUserID should not have been changed")
		}
	})

	t.Run("generates unique IDs", func(t *testing.T) {
		u1 := &User{ID: "user1"}
		u2 := &User{ID: "user2"}

		if _, err := u1.EnsureWebAuthnUserID(); err != nil {
			t.Fatal(err)
		}
		if _, err := u2.EnsureWebAuthnUserID(); err != nil {
			t.Fatal(err)
		}

		if string(u1.WebAuthnUserID) == string(u2.WebAuthnUserID) {
			t.Error("two generated WebAuthn user IDs should not be identical")
		}
	})
}

func TestService_HasPasskeys(t *testing.T) {
	t.Run("false when WebAuthnCreds is nil", func(t *testing.T) {
		svc := newTestService(true)
		if svc.HasPasskeys("user1") {
			t.Error("expected false when WebAuthnCreds is nil")
		}
	})

	t.Run("false when no credentials exist", func(t *testing.T) {
		svc := newTestServiceWithWebAuthn(true)
		if svc.HasPasskeys("user1") {
			t.Error("expected false when user has no passkeys")
		}
	})

	t.Run("true when credentials exist", func(t *testing.T) {
		svc := newTestServiceWithWebAuthn(true)
		cred := WebAuthnCredential{
			ID:     []byte("cred-001"),
			UserID: "user1",
			Name:   "Test Key",
		}
		if err := svc.WebAuthnCreds.CreateWebAuthnCredential(cred); err != nil {
			t.Fatalf("CreateWebAuthnCredential: %v", err)
		}
		if !svc.HasPasskeys("user1") {
			t.Error("expected true when user has a passkey")
		}
	})
}

func TestService_LoginWithWebAuthn(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		svc := newTestServiceWithWebAuthn(true)
		user := User{
			ID:       "user1",
			Username: "admin",
			RoleID:   RoleAdminID,
		}
		if err := svc.Users.CreateUser(user); err != nil {
			t.Fatal(err)
		}

		session, gotUser, err := svc.LoginWithWebAuthn(context.Background(), "user1", "127.0.0.1", "TestAgent")
		if err != nil {
			t.Fatalf("LoginWithWebAuthn failed: %v", err)
		}
		if session == nil {
			t.Fatal("expected session, got nil")
		}
		if gotUser == nil {
			t.Fatal("expected user, got nil")
		}
		if gotUser.ID != "user1" {
			t.Errorf("expected user ID %q, got %q", "user1", gotUser.ID)
		}
		if session.UserID != "user1" {
			t.Errorf("expected session user ID %q, got %q", "user1", session.UserID)
		}
		if session.Token == "" {
			t.Error("expected non-empty session token")
		}
	})

	t.Run("user not found", func(t *testing.T) {
		svc := newTestServiceWithWebAuthn(true)

		_, _, err := svc.LoginWithWebAuthn(context.Background(), "nonexistent", "127.0.0.1", "TestAgent")
		if err != ErrInvalidCredentials {
			t.Errorf("expected ErrInvalidCredentials, got %v", err)
		}
	})

	t.Run("locked account", func(t *testing.T) {
		svc := newTestServiceWithWebAuthn(true)
		user := User{
			ID:          "user1",
			Username:    "locked-user",
			RoleID:      RoleAdminID,
			Locked:      true,
			LockedUntil: time.Now().Add(30 * time.Minute),
		}
		if err := svc.Users.CreateUser(user); err != nil {
			t.Fatal(err)
		}

		_, _, err := svc.LoginWithWebAuthn(context.Background(), "user1", "127.0.0.1", "TestAgent")
		if err != ErrAccountLocked {
			t.Errorf("expected ErrAccountLocked, got %v", err)
		}
	})

	t.Run("rate limited", func(t *testing.T) {
		svc := newTestServiceWithWebAuthn(true)

		// Exhaust rate limit with failures.
		for i := 0; i < 15; i++ {
			svc.rateLimiter.RecordFailure("10.0.0.1")
		}

		user := User{
			ID:       "user1",
			Username: "admin",
			RoleID:   RoleAdminID,
		}
		if err := svc.Users.CreateUser(user); err != nil {
			t.Fatal(err)
		}

		_, _, err := svc.LoginWithWebAuthn(context.Background(), "user1", "10.0.0.1", "TestAgent")
		if err != ErrRateLimited {
			t.Errorf("expected ErrRateLimited, got %v", err)
		}
	})
}
