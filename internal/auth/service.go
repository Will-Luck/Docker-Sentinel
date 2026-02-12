package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"
)

// UserStore is the interface for user persistence.
type UserStore interface {
	CreateUser(user User) error
	GetUser(id string) (*User, error)
	GetUserByUsername(username string) (*User, error)
	UpdateUser(user User) error
	DeleteUser(id string) error
	ListUsers() ([]User, error)
	UserCount() (int, error)
	// CreateFirstUser atomically creates a user only if no users exist.
	// Returns ErrUsersExist if any users already exist (race protection).
	CreateFirstUser(user User) error
}

// SessionStore is the interface for session persistence.
type SessionStore interface {
	CreateSession(session Session) error
	GetSession(token string) (*Session, error)
	DeleteSession(token string) error
	DeleteSessionsForUser(userID string) error
	ListSessionsForUser(userID string) ([]Session, error)
	DeleteExpiredSessions() (int, error)
}

// RoleStore is the interface for role persistence.
type RoleStore interface {
	GetRole(id string) (*Role, error)
	ListRoles() ([]Role, error)
	SeedBuiltinRoles() error
}

// APITokenStore is the interface for API token persistence.
type APITokenStore interface {
	CreateAPIToken(token APIToken) error
	GetAPITokenByHash(hash string) (*APIToken, error)
	DeleteAPIToken(id string) error
	ListAPITokensForUser(userID string) ([]APIToken, error)
}

// SettingsReader reads auth-related settings from the settings bucket.
type SettingsReader interface {
	LoadSetting(key string) (string, error)
	SaveSetting(key, value string) error
}

// Service aggregates all auth-related stores and configuration.
type Service struct {
	Users    UserStore
	Sessions SessionStore
	Roles    RoleStore
	Tokens   APITokenStore
	Settings SettingsReader
	Log      *slog.Logger

	CookieSecure   bool
	SessionExpiry  time.Duration
	AuthEnabledEnv *bool // nil = use DB setting; non-nil = override

	rateLimiter *RateLimiter
}

// NewService creates a new auth service.
func NewService(cfg ServiceConfig) *Service {
	return &Service{
		Users:          cfg.Users,
		Sessions:       cfg.Sessions,
		Roles:          cfg.Roles,
		Tokens:         cfg.Tokens,
		Settings:       cfg.Settings,
		Log:            cfg.Log,
		CookieSecure:   cfg.CookieSecure,
		SessionExpiry:  cfg.SessionExpiry,
		AuthEnabledEnv: cfg.AuthEnabledEnv,
		rateLimiter:    NewRateLimiter(),
	}
}

// ServiceConfig holds the configuration for creating a Service.
type ServiceConfig struct {
	Users          UserStore
	Sessions       SessionStore
	Roles          RoleStore
	Tokens         APITokenStore
	Settings       SettingsReader
	Log            *slog.Logger
	CookieSecure   bool
	SessionExpiry  time.Duration
	AuthEnabledEnv *bool // env var override for auth_enabled
}

// AuthEnabled returns whether auth is currently enabled.
func (s *Service) AuthEnabled() bool {
	// Env var override takes precedence.
	if s.AuthEnabledEnv != nil {
		return *s.AuthEnabledEnv
	}
	// Check DB setting.
	val, err := s.Settings.LoadSetting("auth_enabled")
	if err != nil || val == "" {
		return true // default: enabled
	}
	return val != "false"
}

// SetupComplete returns whether initial setup has been completed.
func (s *Service) SetupComplete() bool {
	val, err := s.Settings.LoadSetting("auth_setup_complete")
	if err != nil {
		return false
	}
	return val == "true"
}

// NeedsSetup returns true if the setup wizard should be shown.
func (s *Service) NeedsSetup() bool {
	if s.SetupComplete() {
		return false
	}
	count, err := s.Users.UserCount()
	if err != nil {
		return false
	}
	return count == 0
}

// GenerateBootstrapToken creates a one-time setup token.
func GenerateBootstrapToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateUserID creates a random 16-char hex user ID.
func GenerateUserID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Login authenticates a user and creates a session.
// Returns the session token and user on success.
func (s *Service) Login(ctx context.Context, username, password, ip, userAgent string) (*Session, *User, error) {
	// Rate limit check.
	if !s.rateLimiter.Allow(ip) {
		return nil, nil, ErrRateLimited
	}

	user, err := s.Users.GetUserByUsername(username)
	if err != nil || user == nil {
		s.rateLimiter.RecordFailure(ip)
		return nil, nil, ErrInvalidCredentials
	}

	// Check account lockout.
	if user.Locked && time.Now().Before(user.LockedUntil) {
		return nil, nil, ErrAccountLocked
	}

	if !CheckPassword(user.PasswordHash, password) {
		// Record failure and potentially lock account.
		user.FailedLogins++
		if user.FailedLogins >= accountLockout {
			user.Locked = true
			user.LockedUntil = time.Now().Add(accountLockoutDur)
		}
		_ = s.Users.UpdateUser(*user)
		s.rateLimiter.RecordFailure(ip)
		return nil, nil, ErrInvalidCredentials
	}

	// Success — clear failure counters.
	user.FailedLogins = 0
	user.Locked = false
	user.LockedUntil = time.Time{}
	_ = s.Users.UpdateUser(*user)
	s.rateLimiter.Reset(ip)

	// Create new session (session rotation — prevent fixation).
	token, err := GenerateSessionToken()
	if err != nil {
		return nil, nil, fmt.Errorf("generate session token: %w", err)
	}

	session := Session{
		Token:     token,
		UserID:    user.ID,
		IP:        ip,
		UserAgent: userAgent,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(s.SessionExpiry),
	}

	if err := s.Sessions.CreateSession(session); err != nil {
		return nil, nil, fmt.Errorf("create session: %w", err)
	}

	return &session, user, nil
}

// ValidateSession checks a session token and returns a RequestContext if valid.
func (s *Service) ValidateSession(ctx context.Context, token string) *RequestContext {
	session, err := s.Sessions.GetSession(token)
	if err != nil || session == nil {
		return nil
	}

	// Check expiry.
	if time.Now().After(session.ExpiresAt) {
		_ = s.Sessions.DeleteSession(token)
		return nil
	}

	user, err := s.Users.GetUser(session.UserID)
	if err != nil || user == nil {
		return nil
	}

	role, _ := s.Roles.GetRole(user.RoleID)
	perms := ResolvePermissions(role, nil)

	return &RequestContext{
		User:        user,
		Session:     session,
		Permissions: perms,
	}
}

// ValidateBearerToken checks a bearer token and returns a RequestContext if valid.
func (s *Service) ValidateBearerToken(ctx context.Context, rawToken string) *RequestContext {
	hash := HashToken(rawToken)
	apiToken, err := s.Tokens.GetAPITokenByHash(hash)
	if err != nil || apiToken == nil {
		return nil
	}

	// Check expiry.
	if !apiToken.ExpiresAt.IsZero() && time.Now().After(apiToken.ExpiresAt) {
		return nil
	}

	user, err := s.Users.GetUser(apiToken.UserID)
	if err != nil || user == nil {
		return nil
	}

	role, _ := s.Roles.GetRole(user.RoleID)
	perms := ResolvePermissions(role, apiToken.Permissions)

	// Update last used timestamp (best effort).
	apiToken.LastUsedAt = time.Now().UTC()
	// Don't error-check — this is best-effort tracking.

	return &RequestContext{
		User:        user,
		APIToken:    apiToken,
		Permissions: perms,
	}
}

// Logout revokes a session.
func (s *Service) Logout(token string) error {
	return s.Sessions.DeleteSession(token)
}

// CleanupExpiredSessions removes expired sessions from the store.
func (s *Service) CleanupExpiredSessions() (int, error) {
	return s.Sessions.DeleteExpiredSessions()
}

// Sentinel errors.
var (
	ErrInvalidCredentials = fmt.Errorf("invalid credentials")
	ErrRateLimited        = fmt.Errorf("too many login attempts")
	ErrAccountLocked      = fmt.Errorf("account is locked")
	ErrUsersExist         = fmt.Errorf("users already exist")
)
