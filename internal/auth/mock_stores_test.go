package auth

import (
	"fmt"
	"sync"
	"time"
)

// mockUserStore is an in-memory implementation of UserStore for testing.
type mockUserStore struct {
	mu    sync.Mutex
	users map[string]User // keyed by ID
}

func newMockUserStore() *mockUserStore {
	return &mockUserStore{users: make(map[string]User)}
}

func (m *mockUserStore) CreateUser(user User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.users[user.ID]; exists {
		return fmt.Errorf("user %q already exists", user.ID)
	}
	m.users[user.ID] = user
	return nil
}

func (m *mockUserStore) GetUser(id string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return nil, nil
	}
	return &u, nil
}

func (m *mockUserStore) GetUserByUsername(username string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.Username == username {
			return &u, nil
		}
	}
	return nil, nil
}

func (m *mockUserStore) UpdateUser(user User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[user.ID] = user
	return nil
}

func (m *mockUserStore) DeleteUser(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.users, id)
	return nil
}

func (m *mockUserStore) ListUsers() ([]User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []User
	for _, u := range m.users {
		result = append(result, u)
	}
	return result, nil
}

func (m *mockUserStore) UserCount() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users), nil
}

func (m *mockUserStore) CreateFirstUser(user User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.users) > 0 {
		return ErrUsersExist
	}
	m.users[user.ID] = user
	return nil
}

// mockSessionStore is an in-memory implementation of SessionStore for testing.
type mockSessionStore struct {
	mu       sync.Mutex
	sessions map[string]Session // keyed by token
}

func newMockSessionStore() *mockSessionStore {
	return &mockSessionStore{sessions: make(map[string]Session)}
}

func (m *mockSessionStore) CreateSession(session Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.Token] = session
	return nil
}

func (m *mockSessionStore) GetSession(token string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok {
		return nil, nil
	}
	return &s, nil
}

func (m *mockSessionStore) DeleteSession(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
	return nil
}

func (m *mockSessionStore) DeleteSessionsForUser(userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.sessions {
		if s.UserID == userID {
			delete(m.sessions, k)
		}
	}
	return nil
}

func (m *mockSessionStore) ListSessionsForUser(userID string) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []Session
	for _, s := range m.sessions {
		if s.UserID == userID {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *mockSessionStore) DeleteExpiredSessions() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
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

// mockRoleStore is an in-memory implementation of RoleStore for testing.
type mockRoleStore struct {
	mu    sync.Mutex
	roles map[string]Role
}

func newMockRoleStore() *mockRoleStore {
	store := &mockRoleStore{roles: make(map[string]Role)}
	// Seed built-in roles by default.
	for _, r := range BuiltinRoles() {
		store.roles[r.ID] = r
	}
	return store
}

func (m *mockRoleStore) GetRole(id string) (*Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.roles[id]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

func (m *mockRoleStore) ListRoles() ([]Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []Role
	for _, r := range m.roles {
		result = append(result, r)
	}
	return result, nil
}

func (m *mockRoleStore) SeedBuiltinRoles() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range BuiltinRoles() {
		m.roles[r.ID] = r
	}
	return nil
}

// mockAPITokenStore is an in-memory implementation of APITokenStore for testing.
type mockAPITokenStore struct {
	mu     sync.Mutex
	tokens map[string]APIToken // keyed by ID
}

func newMockAPITokenStore() *mockAPITokenStore {
	return &mockAPITokenStore{tokens: make(map[string]APIToken)}
}

func (m *mockAPITokenStore) CreateAPIToken(token APIToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[token.ID] = token
	return nil
}

func (m *mockAPITokenStore) GetAPITokenByHash(hash string) (*APIToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.TokenHash == hash {
			return &t, nil
		}
	}
	return nil, nil
}

func (m *mockAPITokenStore) DeleteAPIToken(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, id)
	return nil
}

func (m *mockAPITokenStore) ListAPITokensForUser(userID string) ([]APIToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []APIToken
	for _, t := range m.tokens {
		if t.UserID == userID {
			result = append(result, t)
		}
	}
	return result, nil
}

// mockSettingsReader is an in-memory implementation of SettingsReader for testing.
type mockSettingsReader struct {
	mu       sync.Mutex
	settings map[string]string
}

func newMockSettingsReader() *mockSettingsReader {
	return &mockSettingsReader{settings: make(map[string]string)}
}

func (m *mockSettingsReader) LoadSetting(key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings[key], nil
}

func (m *mockSettingsReader) SaveSetting(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings[key] = value
	return nil
}

// newTestService creates a Service with mock stores for testing.
func newTestService(authEnabled bool) *Service {
	enabled := authEnabled
	return NewService(ServiceConfig{
		Users:          newMockUserStore(),
		Sessions:       newMockSessionStore(),
		Roles:          newMockRoleStore(),
		Tokens:         newMockAPITokenStore(),
		Settings:       newMockSettingsReader(),
		CookieSecure:   false,
		SessionExpiry:  24 * time.Hour,
		AuthEnabledEnv: &enabled,
	})
}
