package store

import (
	"errors"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

// testAuthStore creates a temp Store with auth buckets initialised.
func testAuthStore(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	if err := s.EnsureAuthBuckets(); err != nil {
		t.Fatalf("EnsureAuthBuckets: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// User CRUD
// ---------------------------------------------------------------------------

func TestCreateFirstUser(t *testing.T) {
	s := testAuthStore(t)

	user := auth.User{
		ID:           "u1",
		Username:     "admin",
		PasswordHash: "hash1",
		RoleID:       "admin",
		CreatedAt:    time.Now().UTC(),
	}

	if err := s.CreateFirstUser(user); err != nil {
		t.Fatalf("CreateFirstUser: %v", err)
	}

	// Verify the user was created.
	got, err := s.GetUser("u1")
	if err != nil {
		t.Fatalf("GetUser after create: %v", err)
	}
	if got.Username != "admin" {
		t.Errorf("Username = %q, want %q", got.Username, "admin")
	}
	if got.PasswordHash != "hash1" {
		t.Errorf("PasswordHash = %q, want %q", got.PasswordHash, "hash1")
	}

	// Second call should fail with ErrUsersExist.
	user2 := auth.User{
		ID:       "u2",
		Username: "other",
		RoleID:   "viewer",
	}
	err = s.CreateFirstUser(user2)
	if !errors.Is(err, auth.ErrUsersExist) {
		t.Errorf("expected ErrUsersExist, got %v", err)
	}
}

func TestCreateUserDuplicateUsername(t *testing.T) {
	s := testAuthStore(t)

	u1 := auth.User{ID: "u1", Username: "alice", RoleID: "admin"}
	if err := s.CreateUser(u1); err != nil {
		t.Fatal(err)
	}

	u2 := auth.User{ID: "u2", Username: "alice", RoleID: "viewer"}
	err := s.CreateUser(u2)
	if err == nil {
		t.Fatal("expected error for duplicate username, got nil")
	}
}

func TestGetUser(t *testing.T) {
	s := testAuthStore(t)

	u := auth.User{ID: "u1", Username: "bob", RoleID: "admin"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetUser("u1")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Username != "bob" {
		t.Errorf("Username = %q, want %q", got.Username, "bob")
	}

	// Not found.
	_, err = s.GetUser("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent user, got nil")
	}
}

func TestGetUserByUsername(t *testing.T) {
	s := testAuthStore(t)

	u := auth.User{ID: "u1", Username: "carol", RoleID: "viewer"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetUserByUsername("carol")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.ID != "u1" {
		t.Errorf("ID = %q, want %q", got.ID, "u1")
	}

	// Not found.
	_, err = s.GetUserByUsername("nobody")
	if err == nil {
		t.Error("expected error for nonexistent username, got nil")
	}
}

func TestUpdateUser(t *testing.T) {
	s := testAuthStore(t)

	u := auth.User{ID: "u1", Username: "dave", RoleID: "admin"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	// Update role (same username).
	u.RoleID = "viewer"
	if err := s.UpdateUser(u); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	got, err := s.GetUser("u1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RoleID != "viewer" {
		t.Errorf("RoleID = %q, want %q", got.RoleID, "viewer")
	}

	// Change username and verify index is updated.
	u.Username = "david"
	if err := s.UpdateUser(u); err != nil {
		t.Fatalf("UpdateUser (rename): %v", err)
	}

	// Old username should not resolve.
	_, err = s.GetUserByUsername("dave")
	if err == nil {
		t.Error("expected error for old username, got nil")
	}

	// New username should resolve.
	got, err = s.GetUserByUsername("david")
	if err != nil {
		t.Fatalf("GetUserByUsername(david): %v", err)
	}
	if got.ID != "u1" {
		t.Errorf("ID = %q, want %q", got.ID, "u1")
	}
}

func TestUpdateUserRenameTakenUsername(t *testing.T) {
	s := testAuthStore(t)

	u1 := auth.User{ID: "u1", Username: "alice", RoleID: "admin"}
	u2 := auth.User{ID: "u2", Username: "bob", RoleID: "viewer"}
	if err := s.CreateUser(u1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(u2); err != nil {
		t.Fatal(err)
	}

	// Try to rename u1 to bob's username.
	u1.Username = "bob"
	err := s.UpdateUser(u1)
	if err == nil {
		t.Fatal("expected error for taken username, got nil")
	}
}

func TestDeleteUser(t *testing.T) {
	s := testAuthStore(t)

	u := auth.User{ID: "u1", Username: "eve", RoleID: "admin"}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	// Create sessions and API tokens for this user.
	sess := auth.Session{
		Token:     "sess-token-1",
		UserID:    "u1",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatal(err)
	}

	apiToken := auth.APIToken{
		ID:        "tok1",
		Name:      "test-token",
		TokenHash: "hash-abc",
		UserID:    "u1",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAPIToken(apiToken); err != nil {
		t.Fatal(err)
	}

	// Delete user.
	if err := s.DeleteUser("u1"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	// User should be gone.
	_, err := s.GetUser("u1")
	if err == nil {
		t.Error("expected error for deleted user, got nil")
	}

	// Username index should be gone.
	_, err = s.GetUserByUsername("eve")
	if err == nil {
		t.Error("expected error for deleted username index, got nil")
	}

	// Sessions should be cascade-deleted.
	_, err = s.GetSession("sess-token-1")
	if err == nil {
		t.Error("expected error for cascade-deleted session, got nil")
	}

	// API tokens should be cascade-deleted.
	_, err = s.GetAPITokenByHash("hash-abc")
	if err == nil {
		t.Error("expected error for cascade-deleted API token, got nil")
	}
}

func TestListUsers(t *testing.T) {
	s := testAuthStore(t)

	users, err := s.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 0 {
		t.Fatalf("expected 0 users, got %d", len(users))
	}

	for _, u := range []auth.User{
		{ID: "u1", Username: "alice", RoleID: "admin"},
		{ID: "u2", Username: "bob", RoleID: "viewer"},
		{ID: "u3", Username: "carol", RoleID: "operator"},
	} {
		if err := s.CreateUser(u); err != nil {
			t.Fatal(err)
		}
	}

	users, err = s.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 3 {
		t.Fatalf("expected 3 users, got %d", len(users))
	}
}

func TestUserCount(t *testing.T) {
	s := testAuthStore(t)

	count, err := s.UserCount()
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	if err := s.CreateUser(auth.User{ID: "u1", Username: "alice", RoleID: "admin"}); err != nil {
		t.Fatal(err)
	}
	count, err = s.UserCount()
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1, got %d", count)
	}

	if err := s.CreateUser(auth.User{ID: "u2", Username: "bob", RoleID: "viewer"}); err != nil {
		t.Fatal(err)
	}
	count, err = s.UserCount()
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Session CRUD
// ---------------------------------------------------------------------------

func TestCreateSession(t *testing.T) {
	s := testAuthStore(t)

	sess := auth.Session{
		Token:     "tok-abc-123",
		UserID:    "u1",
		IP:        "10.0.0.1",
		UserAgent: "TestAgent/1.0",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := s.GetSession("tok-abc-123")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.UserID != "u1" {
		t.Errorf("UserID = %q, want %q", got.UserID, "u1")
	}
	if got.IP != "10.0.0.1" {
		t.Errorf("IP = %q, want %q", got.IP, "10.0.0.1")
	}
	if got.UserAgent != "TestAgent/1.0" {
		t.Errorf("UserAgent = %q, want %q", got.UserAgent, "TestAgent/1.0")
	}
}

func TestGetSessionNotFound(t *testing.T) {
	s := testAuthStore(t)

	_, err := s.GetSession("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session, got nil")
	}
}

func TestDeleteSession(t *testing.T) {
	s := testAuthStore(t)

	sess := auth.Session{
		Token:     "tok-del",
		UserID:    "u1",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteSession("tok-del"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	_, err := s.GetSession("tok-del")
	if err == nil {
		t.Error("expected error after deleting session, got nil")
	}

	// Idempotent: deleting again should not error.
	if err := s.DeleteSession("tok-del"); err != nil {
		t.Fatalf("DeleteSession (idempotent): %v", err)
	}
}

func TestDeleteSessionsForUser(t *testing.T) {
	s := testAuthStore(t)

	// Create sessions for two different users.
	for _, sess := range []auth.Session{
		{Token: "u1-s1", UserID: "u1", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)},
		{Token: "u1-s2", UserID: "u1", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)},
		{Token: "u2-s1", UserID: "u2", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)},
	} {
		if err := s.CreateSession(sess); err != nil {
			t.Fatal(err)
		}
	}

	// Delete sessions for u1 only.
	if err := s.DeleteSessionsForUser("u1"); err != nil {
		t.Fatalf("DeleteSessionsForUser: %v", err)
	}

	// u1's sessions should be gone.
	_, err := s.GetSession("u1-s1")
	if err == nil {
		t.Error("expected u1-s1 to be deleted")
	}
	_, err = s.GetSession("u1-s2")
	if err == nil {
		t.Error("expected u1-s2 to be deleted")
	}

	// u2's session should still exist.
	got, err := s.GetSession("u2-s1")
	if err != nil {
		t.Fatalf("u2's session should survive: %v", err)
	}
	if got.UserID != "u2" {
		t.Errorf("UserID = %q, want %q", got.UserID, "u2")
	}
}

func TestListSessionsForUser(t *testing.T) {
	s := testAuthStore(t)

	for _, sess := range []auth.Session{
		{Token: "u1-s1", UserID: "u1", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)},
		{Token: "u1-s2", UserID: "u1", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)},
		{Token: "u2-s1", UserID: "u2", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)},
	} {
		if err := s.CreateSession(sess); err != nil {
			t.Fatal(err)
		}
	}

	sessions, err := s.ListSessionsForUser("u1")
	if err != nil {
		t.Fatalf("ListSessionsForUser: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions for u1, got %d", len(sessions))
	}
	for _, sess := range sessions {
		if sess.UserID != "u1" {
			t.Errorf("session UserID = %q, want %q", sess.UserID, "u1")
		}
	}

	// u2 should have exactly 1.
	sessions, err = s.ListSessionsForUser("u2")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session for u2, got %d", len(sessions))
	}

	// Nonexistent user should have 0.
	sessions, err = s.ListSessionsForUser("u99")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions for u99, got %d", len(sessions))
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	s := testAuthStore(t)

	now := time.Now().UTC()
	for _, sess := range []auth.Session{
		{Token: "expired-1", UserID: "u1", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-1 * time.Hour)},
		{Token: "expired-2", UserID: "u1", CreatedAt: now.Add(-3 * time.Hour), ExpiresAt: now.Add(-30 * time.Minute)},
		{Token: "valid-1", UserID: "u1", CreatedAt: now, ExpiresAt: now.Add(1 * time.Hour)},
		{Token: "valid-2", UserID: "u2", CreatedAt: now, ExpiresAt: now.Add(2 * time.Hour)},
	} {
		if err := s.CreateSession(sess); err != nil {
			t.Fatal(err)
		}
	}

	deleted, err := s.DeleteExpiredSessions()
	if err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}

	// Expired sessions should be gone.
	_, err = s.GetSession("expired-1")
	if err == nil {
		t.Error("expected expired-1 to be deleted")
	}
	_, err = s.GetSession("expired-2")
	if err == nil {
		t.Error("expected expired-2 to be deleted")
	}

	// Valid sessions should remain.
	if _, err := s.GetSession("valid-1"); err != nil {
		t.Errorf("valid-1 should survive: %v", err)
	}
	if _, err := s.GetSession("valid-2"); err != nil {
		t.Errorf("valid-2 should survive: %v", err)
	}
}

// ---------------------------------------------------------------------------
// API Token CRUD
// ---------------------------------------------------------------------------

func TestCreateAPIToken(t *testing.T) {
	s := testAuthStore(t)

	tok := auth.APIToken{
		ID:        "tok1",
		Name:      "CI Token",
		TokenHash: "sha256-aabbcc",
		UserID:    "u1",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAPIToken(tok); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// Retrieve by hash.
	got, err := s.GetAPITokenByHash("sha256-aabbcc")
	if err != nil {
		t.Fatalf("GetAPITokenByHash: %v", err)
	}
	if got.ID != "tok1" {
		t.Errorf("ID = %q, want %q", got.ID, "tok1")
	}
	if got.Name != "CI Token" {
		t.Errorf("Name = %q, want %q", got.Name, "CI Token")
	}
	if got.UserID != "u1" {
		t.Errorf("UserID = %q, want %q", got.UserID, "u1")
	}
}

func TestGetAPITokenByHashNotFound(t *testing.T) {
	s := testAuthStore(t)

	_, err := s.GetAPITokenByHash("nonexistent-hash")
	if err == nil {
		t.Error("expected error for nonexistent hash, got nil")
	}
}

func TestDeleteAPIToken(t *testing.T) {
	s := testAuthStore(t)

	tok := auth.APIToken{
		ID:        "tok1",
		Name:      "temp",
		TokenHash: "hash-del",
		UserID:    "u1",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAPIToken(tok); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteAPIToken("tok1"); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}

	// Primary lookup should fail.
	_, err := s.GetAPITokenByHash("hash-del")
	if err == nil {
		t.Error("expected error after deleting token, got nil")
	}

	// Idempotent: deleting again should not error.
	if err := s.DeleteAPIToken("tok1"); err != nil {
		t.Fatalf("DeleteAPIToken (idempotent): %v", err)
	}
}

func TestTouchAPIToken(t *testing.T) {
	s := testAuthStore(t)

	tok := auth.APIToken{
		ID:        "tok1",
		Name:      "touch-test",
		TokenHash: "hash-touch",
		UserID:    "u1",
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateAPIToken(tok); err != nil {
		t.Fatal(err)
	}

	touchTime := time.Now().UTC().Add(5 * time.Minute)
	if err := s.TouchAPIToken("tok1", touchTime); err != nil {
		t.Fatalf("TouchAPIToken: %v", err)
	}

	got, err := s.GetAPITokenByHash("hash-touch")
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastUsedAt.Equal(touchTime) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, touchTime)
	}
}

func TestListAPITokensForUser(t *testing.T) {
	s := testAuthStore(t)

	for _, tok := range []auth.APIToken{
		{ID: "tok1", Name: "t1", TokenHash: "h1", UserID: "u1", CreatedAt: time.Now().UTC()},
		{ID: "tok2", Name: "t2", TokenHash: "h2", UserID: "u1", CreatedAt: time.Now().UTC()},
		{ID: "tok3", Name: "t3", TokenHash: "h3", UserID: "u2", CreatedAt: time.Now().UTC()},
	} {
		if err := s.CreateAPIToken(tok); err != nil {
			t.Fatal(err)
		}
	}

	tokens, err := s.ListAPITokensForUser("u1")
	if err != nil {
		t.Fatalf("ListAPITokensForUser: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens for u1, got %d", len(tokens))
	}
	for _, tok := range tokens {
		if tok.UserID != "u1" {
			t.Errorf("token UserID = %q, want %q", tok.UserID, "u1")
		}
	}

	// u2 should have 1.
	tokens, err = s.ListAPITokensForUser("u2")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token for u2, got %d", len(tokens))
	}

	// Nonexistent user should have 0.
	tokens, err = s.ListAPITokensForUser("u99")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens for u99, got %d", len(tokens))
	}
}

// ---------------------------------------------------------------------------
// TOTP Pending Tokens
// ---------------------------------------------------------------------------

func TestSavePendingTOTP(t *testing.T) {
	s := testAuthStore(t)

	expires := time.Now().UTC().Add(5 * time.Minute)
	if err := s.SavePendingTOTP("totp-token-1", "u1", expires); err != nil {
		t.Fatalf("SavePendingTOTP: %v", err)
	}

	userID, err := s.GetPendingTOTP("totp-token-1")
	if err != nil {
		t.Fatalf("GetPendingTOTP: %v", err)
	}
	if userID != "u1" {
		t.Errorf("userID = %q, want %q", userID, "u1")
	}

	// Delete and verify gone.
	if err := s.DeletePendingTOTP("totp-token-1"); err != nil {
		t.Fatalf("DeletePendingTOTP: %v", err)
	}

	userID, err = s.GetPendingTOTP("totp-token-1")
	if err != nil {
		t.Fatalf("GetPendingTOTP after delete: %v", err)
	}
	if userID != "" {
		t.Errorf("expected empty userID after delete, got %q", userID)
	}
}

func TestGetPendingTOTPNotFound(t *testing.T) {
	s := testAuthStore(t)

	userID, err := s.GetPendingTOTP("nonexistent")
	if err != nil {
		t.Fatalf("GetPendingTOTP: %v", err)
	}
	if userID != "" {
		t.Errorf("expected empty string for nonexistent token, got %q", userID)
	}
}

func TestGetPendingTOTPExpired(t *testing.T) {
	s := testAuthStore(t)

	// Create an already-expired token.
	expires := time.Now().UTC().Add(-1 * time.Minute)
	if err := s.SavePendingTOTP("expired-totp", "u1", expires); err != nil {
		t.Fatal(err)
	}

	userID, err := s.GetPendingTOTP("expired-totp")
	if err != nil {
		t.Fatalf("GetPendingTOTP: %v", err)
	}
	if userID != "" {
		t.Errorf("expected empty string for expired token, got %q", userID)
	}
}

func TestDeletePendingTOTPIdempotent(t *testing.T) {
	s := testAuthStore(t)

	// Deleting a nonexistent token should not error.
	if err := s.DeletePendingTOTP("nonexistent"); err != nil {
		t.Fatalf("DeletePendingTOTP (nonexistent): %v", err)
	}
}

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

func TestSeedBuiltinRoles(t *testing.T) {
	s := testAuthStore(t)

	if err := s.SeedBuiltinRoles(); err != nil {
		t.Fatalf("SeedBuiltinRoles: %v", err)
	}

	// Verify admin role.
	admin, err := s.GetRole(auth.RoleAdminID)
	if err != nil {
		t.Fatalf("GetRole(admin): %v", err)
	}
	if admin.Name != "Admin" {
		t.Errorf("admin Name = %q, want %q", admin.Name, "Admin")
	}
	if !admin.BuiltIn {
		t.Error("admin BuiltIn should be true")
	}
	if len(admin.Permissions) == 0 {
		t.Error("admin should have permissions")
	}

	// Verify viewer role.
	viewer, err := s.GetRole(auth.RoleViewerID)
	if err != nil {
		t.Fatalf("GetRole(viewer): %v", err)
	}
	if viewer.Name != "Viewer" {
		t.Errorf("viewer Name = %q, want %q", viewer.Name, "Viewer")
	}

	// Seeding again should not overwrite (idempotent).
	if err := s.SeedBuiltinRoles(); err != nil {
		t.Fatalf("SeedBuiltinRoles (idempotent): %v", err)
	}
}

func TestGetRoleNotFound(t *testing.T) {
	s := testAuthStore(t)

	_, err := s.GetRole("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent role, got nil")
	}
}

func TestListRoles(t *testing.T) {
	s := testAuthStore(t)

	// Empty initially.
	roles, err := s.ListRoles()
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 0 {
		t.Fatalf("expected 0 roles, got %d", len(roles))
	}

	// Seed and list.
	if err := s.SeedBuiltinRoles(); err != nil {
		t.Fatal(err)
	}

	roles, err = s.ListRoles()
	if err != nil {
		t.Fatal(err)
	}
	// BuiltinRoles returns 3 roles (admin, operator, viewer).
	if len(roles) != 3 {
		t.Fatalf("expected 3 roles, got %d", len(roles))
	}

	// Verify all role IDs are present.
	ids := make(map[string]bool)
	for _, r := range roles {
		ids[r.ID] = true
	}
	for _, expected := range []string{auth.RoleAdminID, auth.RoleOperatorID, auth.RoleViewerID} {
		if !ids[expected] {
			t.Errorf("missing role %q in list", expected)
		}
	}
}
