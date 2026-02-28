package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig holds the OIDC provider configuration.
type OIDCConfig struct {
	Enabled      bool
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	AutoCreate   bool   // auto-create users from OIDC claims
	DefaultRole  string // role for auto-created users (default "viewer")
}

// OIDCProvider wraps the OIDC discovery and OAuth2 flow.
type OIDCProvider struct {
	mu          sync.RWMutex
	provider    *oidc.Provider
	verifier    *oidc.IDTokenVerifier
	oauth2Cfg   oauth2.Config
	autoCreate  bool
	defaultRole string
}

// OIDCUserInfo represents the user info extracted from OIDC claims.
type OIDCUserInfo struct {
	Subject  string
	Email    string
	Name     string
	Username string
}

// NewOIDCProvider initialises the OIDC provider via discovery.
// Returns nil, nil if the config is not enabled or incomplete.
func NewOIDCProvider(ctx context.Context, cfg OIDCConfig) (*OIDCProvider, error) {
	if !cfg.Enabled || cfg.IssuerURL == "" || cfg.ClientID == "" {
		return nil, nil
	}

	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}

	oauth2Cfg := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}

	defaultRole := cfg.DefaultRole
	if defaultRole == "" {
		defaultRole = RoleViewerID
	}

	return &OIDCProvider{
		provider:    provider,
		verifier:    provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth2Cfg:   oauth2Cfg,
		autoCreate:  cfg.AutoCreate,
		defaultRole: defaultRole,
	}, nil
}

// AuthURL generates the authorization URL with the given state parameter.
func (p *OIDCProvider) AuthURL(state string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.oauth2Cfg.AuthCodeURL(state)
}

// Exchange trades an authorization code for tokens and extracts user info.
func (p *OIDCProvider) Exchange(ctx context.Context, code string) (*OIDCUserInfo, error) {
	p.mu.RLock()
	cfg := p.oauth2Cfg
	verifier := p.verifier
	p.mu.RUnlock()

	token, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("no id_token in response")
	}

	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("token verification: %w", err)
	}

	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}

	username := claims.PreferredUsername
	if username == "" {
		username = claims.Email
	}
	if username == "" {
		username = idToken.Subject
	}

	return &OIDCUserInfo{
		Subject:  idToken.Subject,
		Email:    claims.Email,
		Name:     claims.Name,
		Username: username,
	}, nil
}

// AutoCreate returns whether users should be auto-created.
func (p *OIDCProvider) AutoCreate() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.autoCreate
}

// DefaultRole returns the role for auto-created users.
func (p *OIDCProvider) DefaultRole() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.defaultRole
}

// GenerateOIDCState creates a random 16-byte hex-encoded state parameter.
func GenerateOIDCState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// LoginWithOIDC finds or creates a user from OIDC claims and creates a session.
func (s *Service) LoginWithOIDC(ctx context.Context, info *OIDCUserInfo, autoCreate bool, defaultRole, ip, userAgent string) (*Session, error) {
	// Try to find existing user by username.
	user, err := s.Users.GetUserByUsername(info.Username)
	if err != nil {
		// Treat any error as "not found" — the store returns an error when missing.
		user = nil
	}

	// If not found and auto-create is enabled, create the user.
	if user == nil {
		if !autoCreate {
			return nil, fmt.Errorf("user %q not found and auto-create is disabled", info.Username)
		}

		// Validate the default role.
		switch defaultRole {
		case RoleAdminID, RoleOperatorID, RoleViewerID:
			// valid
		default:
			defaultRole = RoleViewerID
		}

		// Create with a random password (user authenticates via OIDC, not password).
		randomPass, err := generateRandomPassword()
		if err != nil {
			return nil, fmt.Errorf("generate random password: %w", err)
		}
		hash, err := HashPassword(randomPass)
		if err != nil {
			return nil, fmt.Errorf("hash password: %w", err)
		}

		userID, err := GenerateUserID()
		if err != nil {
			return nil, fmt.Errorf("generate user ID: %w", err)
		}

		user = &User{
			ID:           userID,
			Username:     info.Username,
			PasswordHash: hash,
			RoleID:       defaultRole,
			CreatedAt:    time.Now().UTC(),
			UpdatedAt:    time.Now().UTC(),
		}
		if err := s.Users.CreateUser(*user); err != nil {
			return nil, fmt.Errorf("create OIDC user: %w", err)
		}
	}

	// Create session (same pattern as LoginWithWebAuthn).
	token, err := GenerateSessionToken()
	if err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
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
		return nil, fmt.Errorf("create session: %w", err)
	}

	return &session, nil
}

// generateRandomPassword creates a 32-byte hex random string for OIDC-created users.
func generateRandomPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
