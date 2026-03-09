package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newSessionTestServer creates a Server for session handler tests.
func newSessionTestServer() *Server {
	authSvc := newAuthTestService()
	return &Server{
		deps: Dependencies{
			Auth:     authSvc,
			EventBus: events.New(),
			EventLog: &mockEventLogger{},
			Queue:    &mockUpdateQueue{},
			Log:      slog.Default(),
		},
		authLimiter: newRateLimiter(10, time.Minute),
	}
}

// createSession creates a session in the auth service and returns it.
func createSession(svc *auth.Service, userID, token string) auth.Session {
	session := auth.Session{
		Token:     token,
		UserID:    userID,
		IP:        "127.0.0.1",
		UserAgent: "test-agent",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}
	_ = svc.Sessions.CreateSession(session)
	return session
}

// ---------------------------------------------------------------------------
// apiListSessions tests
// ---------------------------------------------------------------------------

func TestApiListSessions_Success(t *testing.T) {
	srv := newSessionTestServer()
	user := createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")

	// Create a couple of sessions for this user.
	createSession(srv.deps.Auth, user.ID, "session-1")
	createSession(srv.deps.Auth, user.ID, "session-2")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/sessions", nil)
	r = reqWithAuthContext(r, &user)

	srv.apiListSessions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var sessions []auth.Session
	if err := json.Unmarshal(w.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("got %d sessions, want 2", len(sessions))
	}
}

func TestApiListSessions_NoAuth(t *testing.T) {
	srv := newSessionTestServer()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/sessions", nil)

	srv.apiListSessions(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestApiListSessions_Empty(t *testing.T) {
	srv := newSessionTestServer()
	user := createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/sessions", nil)
	r = reqWithAuthContext(r, &user)

	srv.apiListSessions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Should return null or empty array, both valid.
	body := w.Body.String()
	if body != "null\n" && body != "[]\n" {
		// Try to parse as array.
		var sessions []auth.Session
		if err := json.Unmarshal(w.Body.Bytes(), &sessions); err == nil && len(sessions) == 0 {
			return // empty array, fine
		}
		t.Errorf("expected empty result, got: %s", body)
	}
}

// ---------------------------------------------------------------------------
// apiRevokeSession tests
// ---------------------------------------------------------------------------

func TestApiRevokeSession_ValidSession(t *testing.T) {
	srv := newSessionTestServer()
	user := createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")

	// Create a target session to revoke.
	createSession(srv.deps.Auth, user.ID, "target-session")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions/target-session", nil)
	r.SetPathValue("token", "target-session")
	r = reqWithAuthContext(r, &user)

	srv.apiRevokeSession(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify the session was deleted.
	s, _ := srv.deps.Auth.Sessions.GetSession("target-session")
	if s != nil {
		t.Error("expected session to be deleted")
	}
}

func TestApiRevokeSession_NotFound(t *testing.T) {
	srv := newSessionTestServer()
	user := createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions/nonexistent", nil)
	r.SetPathValue("token", "nonexistent")
	r = reqWithAuthContext(r, &user)

	srv.apiRevokeSession(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestApiRevokeSession_OtherUsersSession(t *testing.T) {
	srv := newSessionTestServer()
	admin := createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")
	other := createTestUser(srv.deps.Auth, "other", "0therP@ssword!")

	// Create a session owned by the other user.
	createSession(srv.deps.Auth, other.ID, "other-session")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions/other-session", nil)
	r.SetPathValue("token", "other-session")
	r = reqWithAuthContext(r, &admin)

	srv.apiRevokeSession(w, r)

	// Should return 404 because the session belongs to a different user.
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (session belongs to another user)", w.Code, http.StatusNotFound)
	}
}

func TestApiRevokeSession_EmptyToken(t *testing.T) {
	srv := newSessionTestServer()
	user := createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions/", nil)
	r.SetPathValue("token", "")
	r = reqWithAuthContext(r, &user)

	srv.apiRevokeSession(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestApiRevokeSession_NoAuth(t *testing.T) {
	srv := newSessionTestServer()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions/some-token", nil)
	r.SetPathValue("token", "some-token")

	srv.apiRevokeSession(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// ---------------------------------------------------------------------------
// apiRevokeAllSessions tests
// ---------------------------------------------------------------------------

func TestApiRevokeAllSessions_Success(t *testing.T) {
	srv := newSessionTestServer()
	user := createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")

	// Create several sessions.
	createSession(srv.deps.Auth, user.ID, "session-a")
	createSession(srv.deps.Auth, user.ID, "session-b")
	createSession(srv.deps.Auth, user.ID, "current-session")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions", nil)
	// The current session is identified by the cookie.
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "current-session"})
	r = reqWithAuthContext(r, &user)

	srv.apiRevokeAllSessions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify the current session was kept but others were deleted.
	remaining, _ := srv.deps.Auth.Sessions.ListSessionsForUser(user.ID)
	if len(remaining) != 1 {
		t.Fatalf("got %d remaining sessions, want 1 (current only)", len(remaining))
	}
	if remaining[0].Token != "current-session" {
		t.Errorf("remaining session token = %q, want %q", remaining[0].Token, "current-session")
	}
}

func TestApiRevokeAllSessions_NoAuth(t *testing.T) {
	srv := newSessionTestServer()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions", nil)

	srv.apiRevokeAllSessions(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
