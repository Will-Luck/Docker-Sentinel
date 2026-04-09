package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCConfig holds the OIDC provider configuration.
type OIDCConfig struct {
	Enabled       bool
	IssuerURL     string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	AutoCreate    bool              // auto-create users from OIDC claims
	DefaultRole   string            // role for auto-created users (default "viewer")
	GroupClaim    string            // claim name in ID token (default "groups")
	GroupMappings map[string]string // IdP group name -> Sentinel role ID
}

// OIDCProvider wraps the OIDC discovery and OAuth2 flow.
type OIDCProvider struct {
	mu            sync.RWMutex
	provider      *oidc.Provider
	verifier      *oidc.IDTokenVerifier
	oauth2Cfg     oauth2.Config
	autoCreate    bool
	defaultRole   string
	groupClaim    string            // ID token claim containing group list
	groupMappings map[string]string // IdP group -> Sentinel role ID
}

// OIDCUserInfo represents the user info extracted from OIDC claims.
type OIDCUserInfo struct {
	Subject  string
	Email    string
	Name     string
	Username string
	Groups   []string // groups from the ID token group claim
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

	groupClaim := cfg.GroupClaim
	if groupClaim == "" {
		groupClaim = "groups"
	}

	return &OIDCProvider{
		provider:      provider,
		verifier:      provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth2Cfg:     oauth2Cfg,
		autoCreate:    cfg.AutoCreate,
		defaultRole:   defaultRole,
		groupClaim:    groupClaim,
		groupMappings: cfg.GroupMappings,
	}, nil
}

// AuthURL generates the authorization URL with CSRF state, OIDC nonce,
// and PKCE code challenge (S256). The caller is responsible for
// generating state/nonce/verifier with GenerateOIDCState,
// GenerateOIDCNonce, and GeneratePKCEVerifier respectively, and for
// storing them so the callback handler can pass the matching values to
// Exchange.
func (p *OIDCProvider) AuthURL(state, nonce, pkceChallenge string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("code_challenge", pkceChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	}
	return p.oauth2Cfg.AuthCodeURL(state, opts...)
}

// Exchange trades an authorization code for tokens and extracts user
// info. The pkceVerifier must match the code_challenge that was sent
// to AuthURL. The expectedNonce must match the nonce claim in the
// resulting ID token — mismatch indicates either a replay attempt or
// a misconfigured IdP and the exchange is rejected.
func (p *OIDCProvider) Exchange(ctx context.Context, code, pkceVerifier, expectedNonce string) (*OIDCUserInfo, error) {
	p.mu.RLock()
	cfg := p.oauth2Cfg
	verifier := p.verifier
	p.mu.RUnlock()

	token, err := cfg.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", pkceVerifier))
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

	// Reject when the ID token's nonce does not match the nonce we sent
	// on the authorization request. The go-oidc verifier populates
	// idToken.Nonce from the "nonce" claim but does not compare it
	// automatically — that check is our responsibility. Constant-time
	// comparison prevents timing-side-channel leakage of the nonce.
	if expectedNonce == "" || idToken.Nonce == "" ||
		subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(expectedNonce)) != 1 {
		return nil, fmt.Errorf("id token nonce mismatch")
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

	// Extract group claim. The claim name is configurable (e.g. "groups",
	// "roles", "memberOf") and the value may be []string or []interface{}.
	p.mu.RLock()
	groupClaim := p.groupClaim
	p.mu.RUnlock()

	var groups []string
	if groupClaim != "" {
		var rawClaims map[string]json.RawMessage
		if err := idToken.Claims(&rawClaims); err == nil {
			if raw, ok := rawClaims[groupClaim]; ok {
				// Try []string first, then []interface{} with string conversion.
				if json.Unmarshal(raw, &groups) != nil {
					var iface []interface{}
					if json.Unmarshal(raw, &iface) == nil {
						for _, v := range iface {
							if s, ok := v.(string); ok {
								groups = append(groups, s)
							}
						}
					}
				}
			}
		}
	}

	return &OIDCUserInfo{
		Subject:  idToken.Subject,
		Email:    claims.Email,
		Name:     claims.Name,
		Username: username,
		Groups:   groups,
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

// GroupClaim returns the configured group claim name.
func (p *OIDCProvider) GroupClaim() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.groupClaim
}

// GroupMappings returns the group-to-role mapping configuration.
func (p *OIDCProvider) GroupMappings() map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.groupMappings
}

// ResolveRoleFromGroups determines the highest-privilege Sentinel role from
// a user's IdP groups. Returns defaultRole if no mappings match.
// Priority order: admin > operator > viewer.
func ResolveRoleFromGroups(groups []string, mappings map[string]string, defaultRole string) string {
	if len(groups) == 0 || len(mappings) == 0 {
		return defaultRole
	}

	rolePriority := map[string]int{
		RoleAdminID:    3,
		RoleOperatorID: 2,
		RoleViewerID:   1,
	}

	bestRole := defaultRole
	bestPri := rolePriority[defaultRole]

	for _, g := range groups {
		if roleID, ok := mappings[g]; ok {
			if pri, ok := rolePriority[roleID]; ok && pri > bestPri {
				bestRole = roleID
				bestPri = pri
			}
		}
	}
	return bestRole
}

// GenerateOIDCState creates a random 16-byte hex-encoded state parameter.
func GenerateOIDCState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateOIDCNonce creates a random 32-byte hex-encoded nonce for
// binding an ID token to a specific login attempt, preventing token
// replay across sessions.
func GenerateOIDCNonce() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GeneratePKCEVerifier creates a random code_verifier as described in
// RFC 7636 (43 characters, base64url without padding). The verifier is
// stored server-side until the callback phase and then sent with the
// token exchange to prove the login session owns the original
// authorization request.
func GeneratePKCEVerifier() (string, error) {
	// 32 bytes -> 43 base64url chars, well within the RFC 7636 range.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// PKCEChallengeFromVerifier derives the S256 code_challenge from a
// PKCE verifier: base64url(SHA256(verifier)) without padding.
func PKCEChallengeFromVerifier(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// LoginWithOIDC finds or creates a user from OIDC claims and creates a session.
// When groupMappings is non-empty, the user's role is resolved from their IdP
// groups on every login (both new and existing users), keeping Sentinel roles
// in sync with the identity provider.
func (s *Service) LoginWithOIDC(ctx context.Context, info *OIDCUserInfo, autoCreate bool, defaultRole string, groupMappings map[string]string, ip, userAgent string) (*Session, error) {
	// Resolve the target role from groups (falls back to defaultRole).
	targetRole := ResolveRoleFromGroups(info.Groups, groupMappings, defaultRole)

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

		// Validate the resolved role.
		switch targetRole {
		case RoleAdminID, RoleOperatorID, RoleViewerID:
			// valid
		default:
			targetRole = RoleViewerID
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
			RoleID:       targetRole,
			CreatedAt:    time.Now().UTC(),
			UpdatedAt:    time.Now().UTC(),
		}
		if err := s.Users.CreateUser(*user); err != nil {
			return nil, fmt.Errorf("create OIDC user: %w", err)
		}
	} else if len(groupMappings) > 0 && user.RoleID != targetRole {
		// Existing user with group mappings: sync role from IdP.
		user.RoleID = targetRole
		user.UpdatedAt = time.Now().UTC()
		_ = s.Users.UpdateUser(*user)
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
