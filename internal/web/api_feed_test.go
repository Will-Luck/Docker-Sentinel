package web

import (
	"context"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// ---------------------------------------------------------------------------
// Mock: auth.Service replacement for feed tests
// ---------------------------------------------------------------------------

// feedAuthService wraps a real auth.Service with controllable token validation.
// We can't mock *auth.Service directly (concrete type), so we build a minimal
// real Service with an in-memory token store that we can populate.
type feedTokenStore struct {
	tokens map[string]*auth.APIToken // keyed by SHA-256 hash
}

func (s *feedTokenStore) CreateAPIToken(t auth.APIToken) error {
	s.tokens[t.TokenHash] = &t
	return nil
}

func (s *feedTokenStore) GetAPITokenByHash(hash string) (*auth.APIToken, error) {
	t, ok := s.tokens[hash]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (s *feedTokenStore) DeleteAPIToken(_ string) error { return nil }
func (s *feedTokenStore) ListAPITokensForUser(_ string) ([]auth.APIToken, error) {
	return nil, nil
}
func (s *feedTokenStore) TouchAPIToken(_ string, _ time.Time) error { return nil }

type feedUserStore struct {
	users map[string]*auth.User
}

func (s *feedUserStore) CreateUser(u auth.User) error                   { s.users[u.ID] = &u; return nil }
func (s *feedUserStore) GetUser(id string) (*auth.User, error)          { return s.users[id], nil }
func (s *feedUserStore) GetUserByUsername(_ string) (*auth.User, error) { return nil, nil }
func (s *feedUserStore) UpdateUser(u auth.User) error                   { s.users[u.ID] = &u; return nil }
func (s *feedUserStore) DeleteUser(_ string) error                      { return nil }
func (s *feedUserStore) ListUsers() ([]auth.User, error)                { return nil, nil }
func (s *feedUserStore) UserCount() (int, error)                        { return len(s.users), nil }
func (s *feedUserStore) CreateFirstUser(u auth.User) error              { s.users[u.ID] = &u; return nil }

type feedRoleStore struct{}

func (s *feedRoleStore) GetRole(_ string) (*auth.Role, error) { return nil, nil }
func (s *feedRoleStore) ListRoles() ([]auth.Role, error)      { return nil, nil }
func (s *feedRoleStore) SeedBuiltinRoles() error              { return nil }

type feedSessionStore struct{}

func (s *feedSessionStore) CreateSession(_ auth.Session) error                   { return nil }
func (s *feedSessionStore) GetSession(_ string) (*auth.Session, error)           { return nil, nil }
func (s *feedSessionStore) DeleteSession(_ string) error                         { return nil }
func (s *feedSessionStore) DeleteSessionsForUser(_ string) error                 { return nil }
func (s *feedSessionStore) ListSessionsForUser(_ string) ([]auth.Session, error) { return nil, nil }
func (s *feedSessionStore) DeleteExpiredSessions() (int, error)                  { return 0, nil }

type feedSettingsReader struct{}

func (s *feedSettingsReader) LoadSetting(_ string) (string, error) { return "true", nil }
func (s *feedSettingsReader) SaveSetting(_, _ string) error        { return nil }

// newFeedAuthService builds a real auth.Service with an in-memory token store
// and pre-registers a valid API token. Returns the service and the raw token string.
func newFeedAuthService() (*auth.Service, string) {
	const rawToken = "test-feed-token-abc123"
	hash := auth.HashToken(rawToken)

	users := &feedUserStore{users: make(map[string]*auth.User)}
	_ = users.CreateUser(auth.User{
		ID:       "user1",
		Username: "admin",
		RoleID:   "admin",
	})

	tokens := &feedTokenStore{tokens: make(map[string]*auth.APIToken)}
	_ = tokens.CreateAPIToken(auth.APIToken{
		ID:        "tok1",
		TokenHash: hash,
		UserID:    "user1",
	})

	svc := auth.NewService(auth.ServiceConfig{
		Users:         users,
		Sessions:      &feedSessionStore{},
		Roles:         &feedRoleStore{},
		Tokens:        tokens,
		Settings:      &feedSettingsReader{},
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		SessionExpiry: 24 * time.Hour,
	})
	return svc, rawToken
}

// ---------------------------------------------------------------------------
// Mock: HistoryStore that returns configurable records
// ---------------------------------------------------------------------------

type feedHistoryStore struct {
	records []UpdateRecord
}

func (s *feedHistoryStore) ListHistory(limit int, _ string) ([]UpdateRecord, error) {
	if limit > len(s.records) {
		limit = len(s.records)
	}
	return s.records[:limit], nil
}

func (s *feedHistoryStore) ListAllHistory() ([]UpdateRecord, error) { return s.records, nil }
func (s *feedHistoryStore) ListHistoryByContainer(_ string, _ int) ([]UpdateRecord, error) {
	return nil, nil
}
func (s *feedHistoryStore) GetMaintenance(_ string) (bool, error) { return false, nil }
func (s *feedHistoryStore) RecordUpdate(_ UpdateRecord) error     { return nil }

// ---------------------------------------------------------------------------
// Test helper
// ---------------------------------------------------------------------------

func newFeedTestServer(authSvc *auth.Service, store HistoryStore) *Server {
	return &Server{
		deps: Dependencies{
			Auth:     authSvc,
			Store:    store,
			Queue:    &mockUpdateQueue{},
			Cluster:  NewClusterController(),
			EventBus: events.New(),
			Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
}

// atomFeedXML mirrors the feed struct for test unmarshalling.
type atomFeedXML struct {
	XMLName xml.Name       `xml:"feed"`
	Title   string         `xml:"title"`
	Links   []atomLinkXML  `xml:"link"`
	Updated string         `xml:"updated"`
	Entries []atomEntryXML `xml:"entry"`
}

type atomLinkXML struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type atomEntryXML struct {
	Title   string `xml:"title"`
	ID      string `xml:"id"`
	Updated string `xml:"updated"`
	Summary string `xml:"summary"`
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestAtomFeed_ValidToken(t *testing.T) {
	authSvc, token := newFeedAuthService()
	now := time.Now().UTC().Truncate(time.Second)
	store := &feedHistoryStore{
		records: []UpdateRecord{
			{
				Timestamp:     now,
				ContainerName: "nginx",
				OldImage:      "nginx:1.24",
				NewImage:      "nginx:1.25",
				Outcome:       "success",
				Duration:      12 * time.Second,
			},
			{
				Timestamp:     now.Add(-time.Hour),
				ContainerName: "redis",
				OldImage:      "redis:7.0",
				NewImage:      "redis:7.2",
				Outcome:       "failed",
				Duration:      5 * time.Second,
				Error:         "timeout pulling image",
			},
			{
				Timestamp:     now.Add(-2 * time.Hour),
				ContainerName: "postgres",
				OldImage:      "postgres:15",
				NewImage:      "postgres:16",
				Outcome:       "success",
				Duration:      8 * time.Second,
				HostName:      "remote-1",
			},
		},
	}
	srv := newFeedTestServer(authSvc, store)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/history/feed?token="+token, nil)
	srv.apiHistoryFeed(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/atom+xml; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want application/atom+xml; charset=utf-8", ct)
	}

	var feed atomFeedXML
	if err := xml.Unmarshal(w.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v\nbody: %s", err, w.Body.String())
	}

	if feed.Title != "Docker Sentinel Updates" {
		t.Errorf("feed title = %q, want %q", feed.Title, "Docker Sentinel Updates")
	}

	if len(feed.Entries) != 3 {
		t.Fatalf("entry count = %d, want 3", len(feed.Entries))
	}

	// First entry should be the most recent (nginx).
	e := feed.Entries[0]
	wantTitle := "Updated nginx: nginx:1.24 → nginx:1.25"
	if e.Title != wantTitle {
		t.Errorf("entry[0] title = %q, want %q", e.Title, wantTitle)
	}

	// Second entry should include the error.
	e2 := feed.Entries[1]
	if e2.Summary == "" {
		t.Error("entry[1] summary is empty, expected error details")
	}

	// Third entry should include the host name.
	e3 := feed.Entries[2]
	if e3.Summary == "" {
		t.Error("entry[2] summary is empty, expected host details")
	}
}

func TestAtomFeed_MissingToken(t *testing.T) {
	authSvc, _ := newFeedAuthService()
	srv := newFeedTestServer(authSvc, &feedHistoryStore{})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/history/feed", nil)
	srv.apiHistoryFeed(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", w.Code, w.Body.String())
	}
}

func TestAtomFeed_InvalidToken(t *testing.T) {
	authSvc, _ := newFeedAuthService()
	srv := newFeedTestServer(authSvc, &feedHistoryStore{})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/history/feed?token=bad-token", nil)
	srv.apiHistoryFeed(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", w.Code, w.Body.String())
	}
}

func TestAtomFeed_EmptyHistory(t *testing.T) {
	authSvc, token := newFeedAuthService()
	srv := newFeedTestServer(authSvc, &feedHistoryStore{})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/history/feed?token="+token, nil)
	srv.apiHistoryFeed(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var feed atomFeedXML
	if err := xml.Unmarshal(w.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v\nbody: %s", err, w.Body.String())
	}

	if len(feed.Entries) != 0 {
		t.Errorf("entry count = %d, want 0", len(feed.Entries))
	}

	if feed.Title != "Docker Sentinel Updates" {
		t.Errorf("feed title = %q, want %q", feed.Title, "Docker Sentinel Updates")
	}
}

func TestAtomFeed_AuthDisabled(t *testing.T) {
	// When auth is nil (disabled), the feed should be accessible without a token.
	srv := newFeedTestServer(nil, &feedHistoryStore{
		records: []UpdateRecord{
			{
				Timestamp:     time.Now().UTC(),
				ContainerName: "test",
				OldImage:      "test:1",
				NewImage:      "test:2",
				Outcome:       "success",
				Duration:      time.Second,
			},
		},
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/history/feed", nil)
	srv.apiHistoryFeed(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var feed atomFeedXML
	if err := xml.Unmarshal(w.Body.Bytes(), &feed); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if len(feed.Entries) != 1 {
		t.Errorf("entry count = %d, want 1", len(feed.Entries))
	}
}

// Ensure the ValidateBearerToken codepath is exercised with context.
func TestAtomFeed_UsesRequestContext(t *testing.T) {
	authSvc, token := newFeedAuthService()
	srv := newFeedTestServer(authSvc, &feedHistoryStore{})

	ctx := context.Background()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/history/feed?token="+token, nil)
	r = r.WithContext(ctx)
	srv.apiHistoryFeed(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}
