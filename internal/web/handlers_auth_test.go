package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// ---------------------------------------------------------------------------
// Helpers: build a Server with a real auth.Service backed by in-memory stores
// ---------------------------------------------------------------------------

// mockEventLogger captures log entries for assertions.
type mockEventLogger struct {
	entries []LogEntry
}

func (m *mockEventLogger) AppendLog(entry LogEntry) error {
	m.entries = append(m.entries, entry)
	return nil
}

func (m *mockEventLogger) ListLogs(_ int) ([]LogEntry, error) {
	return m.entries, nil
}

// mockUpdateQueue satisfies the UpdateQueue interface with no-ops.
type mockUpdateQueue struct{}

func (m *mockUpdateQueue) List() []PendingUpdate                  { return nil }
func (m *mockUpdateQueue) Get(_ string) (PendingUpdate, bool)     { return PendingUpdate{}, false }
func (m *mockUpdateQueue) Add(_ PendingUpdate)                    {}
func (m *mockUpdateQueue) Approve(_ string) (PendingUpdate, bool) { return PendingUpdate{}, false }
func (m *mockUpdateQueue) Remove(_ string)                        {}

// newAuthTestService creates an auth.Service with in-memory stores and a
// pre-created admin user. Returns the service and the admin's password hash.
func newAuthTestService() *auth.Service {
	enabled := true
	return auth.NewService(auth.ServiceConfig{
		Users:          newWebMockUserStore(),
		Sessions:       newWebMockSessionStore(),
		Roles:          newWebMockRoleStore(),
		Tokens:         newWebMockAPITokenStore(),
		Settings:       newWebMockSettingsReader(),
		CookieSecure:   false,
		SessionExpiry:  24 * time.Hour,
		AuthEnabledEnv: &enabled,
	})
}

// newAuthTestServer builds a Server wired for auth handler tests.
// It creates a real auth.Service with in-memory stores.
func newAuthTestServer() *Server {
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

// createTestUser creates a user in the auth service and returns the user.
func createTestUser(svc *auth.Service, username, password string) auth.User {
	hash, _ := auth.HashPassword(password)
	id, _ := auth.GenerateUserID()
	user := auth.User{
		ID:           id,
		Username:     username,
		PasswordHash: hash,
		RoleID:       auth.RoleAdminID,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	_ = svc.Users.CreateUser(user)
	return user
}

// reqWithAuthContext returns a request with an auth.RequestContext injected.
func reqWithAuthContext(r *http.Request, user *auth.User) *http.Request {
	rc := &auth.RequestContext{
		User:        user,
		Permissions: auth.AllPermissions(),
		AuthEnabled: true,
	}
	ctx := context.WithValue(r.Context(), auth.ContextKey, rc)
	return r.WithContext(ctx)
}

// ---------------------------------------------------------------------------
// In-memory auth stores (web-package versions, mirrors of auth package mocks)
// ---------------------------------------------------------------------------

type webMockUserStore struct {
	users map[string]auth.User
}

func newWebMockUserStore() *webMockUserStore {
	return &webMockUserStore{users: make(map[string]auth.User)}
}

func (m *webMockUserStore) CreateUser(user auth.User) error {
	for _, u := range m.users {
		if u.Username == user.Username {
			return auth.ErrUsersExist
		}
	}
	m.users[user.ID] = user
	return nil
}

func (m *webMockUserStore) GetUser(id string) (*auth.User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, nil
	}
	return &u, nil
}

func (m *webMockUserStore) GetUserByUsername(username string) (*auth.User, error) {
	for _, u := range m.users {
		if u.Username == username {
			return &u, nil
		}
	}
	return nil, nil
}

func (m *webMockUserStore) UpdateUser(user auth.User) error {
	m.users[user.ID] = user
	return nil
}

func (m *webMockUserStore) DeleteUser(id string) error {
	delete(m.users, id)
	return nil
}

func (m *webMockUserStore) ListUsers() ([]auth.User, error) {
	var result []auth.User
	for _, u := range m.users {
		result = append(result, u)
	}
	return result, nil
}

func (m *webMockUserStore) UserCount() (int, error) {
	return len(m.users), nil
}

func (m *webMockUserStore) CreateFirstUser(user auth.User) error {
	if len(m.users) > 0 {
		return auth.ErrUsersExist
	}
	m.users[user.ID] = user
	return nil
}

type webMockSessionStore struct {
	sessions map[string]auth.Session
}

func newWebMockSessionStore() *webMockSessionStore {
	return &webMockSessionStore{sessions: make(map[string]auth.Session)}
}

func (m *webMockSessionStore) CreateSession(session auth.Session) error {
	m.sessions[session.Token] = session
	return nil
}

func (m *webMockSessionStore) GetSession(token string) (*auth.Session, error) {
	s, ok := m.sessions[token]
	if !ok {
		return nil, nil
	}
	return &s, nil
}

func (m *webMockSessionStore) DeleteSession(token string) error {
	delete(m.sessions, token)
	return nil
}

func (m *webMockSessionStore) DeleteSessionsForUser(userID string) error {
	for k, s := range m.sessions {
		if s.UserID == userID {
			delete(m.sessions, k)
		}
	}
	return nil
}

func (m *webMockSessionStore) ListSessionsForUser(userID string) ([]auth.Session, error) {
	var result []auth.Session
	for _, s := range m.sessions {
		if s.UserID == userID {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *webMockSessionStore) DeleteExpiredSessions() (int, error) {
	now := time.Now()
	count := 0
	for k, s := range m.sessions {
		if now.After(s.ExpiresAt) {
			delete(m.sessions, k)
			count++
		}
	}
	return count, nil
}

type webMockRoleStore struct {
	roles map[string]auth.Role
}

func newWebMockRoleStore() *webMockRoleStore {
	store := &webMockRoleStore{roles: make(map[string]auth.Role)}
	for _, r := range auth.BuiltinRoles() {
		store.roles[r.ID] = r
	}
	return store
}

func (m *webMockRoleStore) GetRole(id string) (*auth.Role, error) {
	r, ok := m.roles[id]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

func (m *webMockRoleStore) ListRoles() ([]auth.Role, error) {
	var result []auth.Role
	for _, r := range m.roles {
		result = append(result, r)
	}
	return result, nil
}

func (m *webMockRoleStore) SeedBuiltinRoles() error {
	for _, r := range auth.BuiltinRoles() {
		m.roles[r.ID] = r
	}
	return nil
}

type webMockAPITokenStore struct {
	tokens map[string]auth.APIToken
}

func newWebMockAPITokenStore() *webMockAPITokenStore {
	return &webMockAPITokenStore{tokens: make(map[string]auth.APIToken)}
}

func (m *webMockAPITokenStore) CreateAPIToken(token auth.APIToken) error {
	m.tokens[token.ID] = token
	return nil
}

func (m *webMockAPITokenStore) GetAPITokenByHash(hash string) (*auth.APIToken, error) {
	for _, t := range m.tokens {
		if t.TokenHash == hash {
			return &t, nil
		}
	}
	return nil, nil
}

func (m *webMockAPITokenStore) DeleteAPIToken(id string) error {
	delete(m.tokens, id)
	return nil
}

func (m *webMockAPITokenStore) ListAPITokensForUser(userID string) ([]auth.APIToken, error) {
	var result []auth.APIToken
	for _, t := range m.tokens {
		if t.UserID == userID {
			result = append(result, t)
		}
	}
	return result, nil
}

func (m *webMockAPITokenStore) TouchAPIToken(id string, t time.Time) error {
	if tok, ok := m.tokens[id]; ok {
		tok.LastUsedAt = t
		m.tokens[id] = tok
	}
	return nil
}

type webMockSettingsReader struct {
	settings map[string]string
}

func newWebMockSettingsReader() *webMockSettingsReader {
	return &webMockSettingsReader{settings: make(map[string]string)}
}

func (m *webMockSettingsReader) LoadSetting(key string) (string, error) {
	return m.settings[key], nil
}

func (m *webMockSettingsReader) SaveSetting(key, value string) error {
	m.settings[key] = value
	return nil
}

// ---------------------------------------------------------------------------
// apiLogin tests
// ---------------------------------------------------------------------------

func TestApiLogin_ValidCredentials(t *testing.T) {
	srv := newAuthTestServer()
	createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")

	body := `{"username":"admin","password":"Str0ngP@ssword!"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	srv.apiLogin(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["redirect"] != "/" {
		t.Errorf("redirect = %v, want %q", resp["redirect"], "/")
	}

	// Verify a session cookie was set.
	cookies := w.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected session cookie to be set")
	}
}

func TestApiLogin_InvalidCredentials(t *testing.T) {
	srv := newAuthTestServer()
	createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")

	body := `{"username":"admin","password":"wrongpassword"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	srv.apiLogin(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
}

func TestApiLogin_BadJSON(t *testing.T) {
	srv := newAuthTestServer()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("{broken"))
	r.Header.Set("Content-Type", "application/json")

	srv.apiLogin(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestApiLogin_EmptyFields(t *testing.T) {
	srv := newAuthTestServer()

	cases := []struct {
		name string
		body string
	}{
		{"empty username", `{"username":"","password":"something"}`},
		{"empty password", `{"username":"admin","password":""}`},
		{"both empty", `{"username":"","password":""}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")

			srv.apiLogin(w, r)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// handleLogout tests
// ---------------------------------------------------------------------------

func TestHandleLogout_ValidSession(t *testing.T) {
	srv := newAuthTestServer()
	user := createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")

	// Create a session manually.
	session := auth.Session{
		Token:     "test-session-token",
		UserID:    user.ID,
		IP:        "127.0.0.1",
		UserAgent: "test",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}
	_ = srv.deps.Auth.Sessions.CreateSession(session)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "test-session-token"})

	srv.handleLogout(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("redirect location = %q, want %q", loc, "/login")
	}

	// Verify the session was deleted.
	s, _ := srv.deps.Auth.Sessions.GetSession("test-session-token")
	if s != nil {
		t.Error("expected session to be deleted after logout")
	}
}

func TestHandleLogout_NoSession(t *testing.T) {
	srv := newAuthTestServer()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)

	srv.handleLogout(w, r)

	// Should still redirect without error (idempotent).
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("redirect location = %q, want %q", loc, "/login")
	}
}

// ---------------------------------------------------------------------------
// Rate limiting test (via the web-level rateLimiter wrapper)
// ---------------------------------------------------------------------------

func TestApiLogin_RateLimited(t *testing.T) {
	srv := newAuthTestServer()
	// Use a tight limit: 3 requests per minute.
	srv.authLimiter = newRateLimiter(3, time.Minute)
	createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")

	// Make 3 requests to fill the window.
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
		r.Header.Set("Content-Type", "application/json")
		// Use the rateLimit wrapper as the route does.
		rateLimit(srv.authLimiter, srv.apiLogin)(w, r)
	}

	// The 4th request should be rate-limited.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	r.Header.Set("Content-Type", "application/json")
	rateLimit(srv.authLimiter, srv.apiLogin)(w, r)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d (rate limited)", w.Code, http.StatusTooManyRequests)
	}
}

// ---------------------------------------------------------------------------
// apiChangePassword tests
// ---------------------------------------------------------------------------

func TestApiChangePassword_Success(t *testing.T) {
	srv := newAuthTestServer()
	user := createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")

	body := `{"current_password":"Str0ngP@ssword!","new_password":"N3wStr0ng!Pass"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = reqWithAuthContext(r, &user)

	srv.apiChangePassword(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	// Verify a new session cookie was issued.
	cookies := w.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected new session cookie after password change")
	}
}

func TestApiChangePassword_WrongCurrent(t *testing.T) {
	srv := newAuthTestServer()
	user := createTestUser(srv.deps.Auth, "admin", "Str0ngP@ssword!")

	body := `{"current_password":"wrongpass","new_password":"N3wStr0ng!Pass"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = reqWithAuthContext(r, &user)

	srv.apiChangePassword(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestApiChangePassword_NoAuth(t *testing.T) {
	srv := newAuthTestServer()

	body := `{"current_password":"x","new_password":"y"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	srv.apiChangePassword(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
