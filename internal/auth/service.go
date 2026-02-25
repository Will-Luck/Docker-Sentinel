package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/pquerna/otp"
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

// PendingTOTPStore persists temporary TOTP tokens for the 2-step login flow.
type PendingTOTPStore interface {
	SavePendingTOTP(token, userID string, expiresAt time.Time) error
	GetPendingTOTP(token string) (userID string, err error) // checks expiry
	DeletePendingTOTP(token string) error
}

// SettingsReader reads auth-related settings from the settings bucket.
type SettingsReader interface {
	LoadSetting(key string) (string, error)
	SaveSetting(key, value string) error
}

// Service aggregates all auth-related stores and configuration.
type Service struct {
	Users         UserStore
	Sessions      SessionStore
	Roles         RoleStore
	Tokens        APITokenStore
	Settings      SettingsReader
	WebAuthnCreds WebAuthnCredentialStore
	PendingTOTP   PendingTOTPStore
	Ceremonies    *CeremonyStore
	Log           *slog.Logger

	CookieSecure   bool
	SessionExpiry  time.Duration
	AuthEnabledEnv *bool // nil = use DB setting; non-nil = override

	rateLimiter *RateLimiter
}

// NewService creates a new auth service.
func NewService(cfg ServiceConfig) *Service {
	s := &Service{
		Users:          cfg.Users,
		Sessions:       cfg.Sessions,
		Roles:          cfg.Roles,
		Tokens:         cfg.Tokens,
		Settings:       cfg.Settings,
		WebAuthnCreds:  cfg.WebAuthnCreds,
		PendingTOTP:    cfg.PendingTOTP,
		Log:            cfg.Log,
		CookieSecure:   cfg.CookieSecure,
		SessionExpiry:  cfg.SessionExpiry,
		AuthEnabledEnv: cfg.AuthEnabledEnv,
		rateLimiter:    NewRateLimiter(),
	}
	if cfg.WebAuthnCreds != nil {
		s.Ceremonies = NewCeremonyStore()
	}
	return s
}

// ServiceConfig holds the configuration for creating a Service.
type ServiceConfig struct {
	Users          UserStore
	Sessions       SessionStore
	Roles          RoleStore
	Tokens         APITokenStore
	Settings       SettingsReader
	WebAuthnCreds  WebAuthnCredentialStore
	PendingTOTP    PendingTOTPStore
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

	// If TOTP is enabled, don't create a session yet — return a pending token.
	if user.TOTPEnabled && s.PendingTOTP != nil {
		pendingToken, err := s.createPendingTOTP(user.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("create pending TOTP: %w", err)
		}
		return nil, user, &ErrTOTPRequired{PendingToken: pendingToken}
	}

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

// HasPasskeys returns whether the user has any WebAuthn credentials registered.
func (s *Service) HasPasskeys(userID string) bool {
	if s.WebAuthnCreds == nil {
		return false
	}
	creds, err := s.WebAuthnCreds.ListWebAuthnCredentialsForUser(userID)
	if err != nil {
		return false
	}
	return len(creds) > 0
}

// LoginWithWebAuthn creates a session for a user authenticated via WebAuthn.
func (s *Service) LoginWithWebAuthn(ctx context.Context, userID, ip, userAgent string) (*Session, *User, error) {
	if !s.rateLimiter.Allow(ip) {
		return nil, nil, ErrRateLimited
	}

	user, err := s.Users.GetUser(userID)
	if err != nil || user == nil {
		s.rateLimiter.RecordFailure(ip)
		return nil, nil, ErrInvalidCredentials
	}

	if user.Locked && time.Now().Before(user.LockedUntil) {
		return nil, nil, ErrAccountLocked
	}

	// Success — clear failure counters.
	user.FailedLogins = 0
	user.Locked = false
	user.LockedUntil = time.Time{}
	_ = s.Users.UpdateUser(*user)
	s.rateLimiter.Reset(ip)

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

// createPendingTOTP generates a random token and stores it for the 2FA step.
func (s *Service) createPendingTOTP(userID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate pending TOTP token: %w", err)
	}
	token := hex.EncodeToString(b)
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	if err := s.PendingTOTP.SavePendingTOTP(token, userID, expiresAt); err != nil {
		return "", err
	}
	return token, nil
}

// VerifyTOTP completes the 2FA login by validating the TOTP code or recovery code.
// Returns a session on success.
func (s *Service) VerifyTOTP(ctx context.Context, pendingToken, code, ip, userAgent string) (*Session, error) {
	if s.PendingTOTP == nil {
		return nil, ErrTOTPInvalidToken
	}

	// Rate limit check.
	if !s.rateLimiter.Allow(ip) {
		return nil, ErrRateLimited
	}

	// Look up pending token.
	userID, err := s.PendingTOTP.GetPendingTOTP(pendingToken)
	if err != nil || userID == "" {
		s.rateLimiter.RecordFailure(ip)
		return nil, ErrTOTPInvalidToken
	}

	// Get user.
	user, err := s.Users.GetUser(userID)
	if err != nil || user == nil {
		return nil, ErrTOTPInvalidToken
	}

	if !user.TOTPEnabled || user.TOTPSecret == "" {
		return nil, ErrTOTPNotEnabled
	}

	// Try TOTP code first.
	valid := ValidateTOTPCode(user.TOTPSecret, code)

	// If TOTP didn't match, try recovery codes.
	if !valid {
		idx := ValidateRecoveryCode(code, user.RecoveryCodes)
		if idx >= 0 {
			valid = true
			// Remove the used recovery code.
			user.RecoveryCodes = append(user.RecoveryCodes[:idx], user.RecoveryCodes[idx+1:]...)
			user.UpdatedAt = time.Now().UTC()
			_ = s.Users.UpdateUser(*user)
		}
	}

	if !valid {
		s.rateLimiter.RecordFailure(ip)
		return nil, ErrTOTPInvalidCode
	}

	// Delete pending token.
	_ = s.PendingTOTP.DeletePendingTOTP(pendingToken)
	s.rateLimiter.Reset(ip)

	// Create session.
	sessionToken, err := GenerateSessionToken()
	if err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}

	session := Session{
		Token:     sessionToken,
		UserID:    user.ID,
		IP:        ip,
		UserAgent: userAgent,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(s.SessionExpiry),
	}

	if err := s.Sessions.CreateSession(session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	return &session, nil
}

// EnableTOTP generates a TOTP secret for a user. Returns the key for QR display.
// The secret is NOT activated until ConfirmTOTP is called with a valid code.
func (s *Service) EnableTOTP(ctx context.Context, userID string) (*otp.Key, []string, error) {
	user, err := s.Users.GetUser(userID)
	if err != nil || user == nil {
		return nil, nil, fmt.Errorf("user not found")
	}

	if user.TOTPEnabled {
		return nil, nil, ErrTOTPAlreadyEnabled
	}

	// Generate TOTP secret.
	key, err := GenerateTOTPSecret(user.Username)
	if err != nil {
		return nil, nil, fmt.Errorf("generate TOTP secret: %w", err)
	}

	// Generate recovery codes.
	plain, stored, err := GenerateRecoveryCodes()
	if err != nil {
		return nil, nil, fmt.Errorf("generate recovery codes: %w", err)
	}

	// Store secret and recovery codes — TOTPEnabled stays false until ConfirmTOTP.
	user.TOTPSecret = key.Secret()
	user.RecoveryCodes = stored
	user.UpdatedAt = time.Now().UTC()
	if err := s.Users.UpdateUser(*user); err != nil {
		return nil, nil, fmt.Errorf("save user: %w", err)
	}

	return key, plain, nil
}

// ConfirmTOTP activates 2FA after the user proves they can generate valid codes.
// Returns the recovery codes (plain text, shown once).
func (s *Service) ConfirmTOTP(ctx context.Context, userID, code string) ([]string, error) {
	user, err := s.Users.GetUser(userID)
	if err != nil || user == nil {
		return nil, fmt.Errorf("user not found")
	}

	if user.TOTPEnabled {
		return nil, ErrTOTPAlreadyEnabled
	}

	if user.TOTPSecret == "" {
		return nil, fmt.Errorf("no TOTP secret set — call EnableTOTP first")
	}

	// Validate the code against the secret.
	if !ValidateTOTPCode(user.TOTPSecret, code) {
		return nil, ErrTOTPInvalidCode
	}

	// Activate 2FA.
	user.TOTPEnabled = true
	user.UpdatedAt = time.Now().UTC()
	if err := s.Users.UpdateUser(*user); err != nil {
		return nil, fmt.Errorf("save user: %w", err)
	}

	// Return the recovery codes (already stored during EnableTOTP).
	return user.RecoveryCodes, nil
}

// DisableTOTP removes 2FA from a user's account after verifying the password.
func (s *Service) DisableTOTP(ctx context.Context, userID, password string) error {
	user, err := s.Users.GetUser(userID)
	if err != nil || user == nil {
		return fmt.Errorf("user not found")
	}

	if !user.TOTPEnabled {
		return ErrTOTPNotEnabled
	}

	// Verify password.
	if !CheckPassword(user.PasswordHash, password) {
		return ErrInvalidCredentials
	}

	// Clear TOTP fields.
	user.TOTPSecret = ""
	user.TOTPEnabled = false
	user.RecoveryCodes = nil
	user.UpdatedAt = time.Now().UTC()
	if err := s.Users.UpdateUser(*user); err != nil {
		return fmt.Errorf("save user: %w", err)
	}

	return nil
}

// ErrTOTPRequired is returned when login succeeds but TOTP verification is needed.
type ErrTOTPRequired struct {
	PendingToken string
}

func (e *ErrTOTPRequired) Error() string { return "TOTP verification required" }

// Sentinel errors.
var (
	ErrInvalidCredentials = fmt.Errorf("invalid credentials")
	ErrRateLimited        = fmt.Errorf("too many login attempts")
	ErrAccountLocked      = fmt.Errorf("account is locked")
	ErrUsersExist         = fmt.Errorf("users already exist")
	ErrTOTPNotEnabled     = fmt.Errorf("TOTP is not enabled for this user")
	ErrTOTPAlreadyEnabled = fmt.Errorf("TOTP is already enabled")
	ErrTOTPInvalidCode    = fmt.Errorf("invalid TOTP code")
	ErrTOTPInvalidToken   = fmt.Errorf("invalid or expired TOTP pending token")
)
