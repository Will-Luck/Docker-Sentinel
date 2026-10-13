package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	Issuer   string // "iss" of the ID token
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
		Issuer:   idToken.Issuer,
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

// ErrOIDCAccountExists is returned when an SSO identity has the same
// username as an account it is not bound to. It is refused, never merged.
var ErrOIDCAccountExists = errors.New("This account is not linked to your sign-in provider yet. Ask an admin to allow linking, or sign in with your password and link it from your Account page.")

// ErrOIDCIdentityTaken is returned when an SSO identity is already bound to
// another account.
var ErrOIDCIdentityTaken = errors.New("this single sign-on identity is already linked to another account")

// findByOIDC returns the account bound to (issuer, subject), if any.
func (s *Service) findByOIDC(issuer, subject string) (*User, error) {
	users, err := s.Users.ListUsers()
	if err != nil {
		return nil, err
	}
	for i := range users {
		if users[i].OIDCIssuer == issuer && users[i].OIDCSubject == subject {
			u := users[i]
			return &u, nil
		}
	}
	return nil, nil
}

// oidcLinkWindow is how long an admin's "allow the next SSO login to link"
// stays open.
const oidcLinkWindow = 24 * time.Hour

// AllowOIDCLink lets the next SSO login whose username matches this account
// link to it, within 24 hours, once.
func (s *Service) AllowOIDCLink(userID string) error {
	user, err := s.Users.GetUser(userID)
	if err != nil || user == nil {
		return fmt.Errorf("user not found")
	}
	if user.OIDCSubject != "" {
		return fmt.Errorf("this account is already linked")
	}
	user.OIDCLinkUntil = time.Now().UTC().Add(oidcLinkWindow)
	user.UpdatedAt = time.Now().UTC()
	if err := s.Users.UpdateUser(*user); err != nil {
		return err
	}
	s.audit("Allowed the next single sign-on login to link to account " + user.Username)
	return nil
}

func (s *Service) audit(msg string) {
	if s.AuditHook != nil {
		s.AuditHook(msg)
	}
}

// mayLinkByUsername reports whether an unlinked account may be linked to the
// SSO identity logging in as its username: only inside an admin's window, or
// with the opt-in on, and never an admin account through the opt-in.
func (s *Service) mayLinkByUsername(u *User) (bool, string) {
	if u.OIDCSubject != "" {
		return false, ""
	}
	if time.Now().Before(u.OIDCLinkUntil) {
		return true, "an administrator allowed it"
	}
	if s.LinkExisting != nil && s.LinkExisting() && !s.isAdminRole(u.RoleID) {
		return true, "linking existing accounts is switched on"
	}
	return false, ""
}

// isAdminRole reports whether a role can change settings.
func (s *Service) isAdminRole(roleID string) bool {
	if roleID == RoleAdminID {
		return true
	}
	role, err := s.Roles.GetRole(roleID)
	if err != nil || role == nil {
		return true // unknown role: treat as privileged
	}
	for _, p := range role.Permissions {
		if p == PermSettingsModify {
			return true
		}
	}
	return false
}

// LoginWithOIDC finds or creates a user from OIDC claims and creates a session.
// Accounts are matched on the identity provider's issuer and subject, never
// on the username. A username clash with an unlinked account is refused,
// unless an admin allowed the link or the opt-in covers that account.
// When groupMappings is non-empty, the user's role is resolved from their IdP
// groups on every login, keeping Sentinel roles in sync with the provider.
func (s *Service) LoginWithOIDC(ctx context.Context, info *OIDCUserInfo, autoCreate bool, defaultRole string, groupMappings map[string]string, ip, userAgent string) (*Session, error) {
	if info.Issuer == "" || info.Subject == "" {
		return nil, fmt.Errorf("identity provider response has no issuer or subject")
	}
	targetRole := ResolveRoleFromGroups(info.Groups, groupMappings, defaultRole)

	user, err := s.findByOIDC(info.Issuer, info.Subject)
	if err != nil {
		return nil, fmt.Errorf("look up SSO identity: %w", err)
	}

	if user == nil {
		existing, _ := s.Users.GetUserByUsername(info.Username)
		switch {
		case existing != nil:
			ok, why := s.mayLinkByUsername(existing)
			if !ok {
				return nil, ErrOIDCAccountExists
			}
			existing.OIDCIssuer = info.Issuer
			existing.OIDCSubject = info.Subject
			existing.OIDCLinkUntil = time.Time{}
			existing.UpdatedAt = time.Now().UTC()
			if err := s.Users.UpdateUser(*existing); err != nil {
				return nil, fmt.Errorf("link SSO identity: %w", err)
			}
			s.audit("Linked account " + existing.Username + " to a single sign-on identity (" + why + ")")
			user = existing
		case !autoCreate:
			return nil, fmt.Errorf("user %q not found and auto-create is disabled", info.Username)
		default:
			created, err := s.createOIDCUser(info, targetRole)
			if err != nil {
				return nil, err
			}
			user = created
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

// createOIDCUser creates an account bound to the SSO identity, with a random
// password nobody knows.
func (s *Service) createOIDCUser(info *OIDCUserInfo, role string) (*User, error) {
	switch role {
	case RoleAdminID, RoleOperatorID, RoleViewerID:
	default:
		role = RoleViewerID
	}
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
	now := time.Now().UTC()
	user := User{
		ID: userID, Username: info.Username, PasswordHash: hash, RoleID: role,
		CreatedAt: now, UpdatedAt: now,
		OIDCIssuer: info.Issuer, OIDCSubject: info.Subject,
	}
	if err := s.Users.CreateUser(user); err != nil {
		return nil, fmt.Errorf("create OIDC user: %w", err)
	}
	return &user, nil
}

// LinkOIDC binds an SSO identity to a signed-in user's account.
func (s *Service) LinkOIDC(userID string, info *OIDCUserInfo) error {
	if info.Issuer == "" || info.Subject == "" {
		return fmt.Errorf("identity provider response has no issuer or subject")
	}
	if other, err := s.findByOIDC(info.Issuer, info.Subject); err != nil {
		return err
	} else if other != nil && other.ID != userID {
		return ErrOIDCIdentityTaken
	}
	user, err := s.Users.GetUser(userID)
	if err != nil || user == nil {
		return fmt.Errorf("user not found")
	}
	user.OIDCIssuer = info.Issuer
	user.OIDCSubject = info.Subject
	user.OIDCLinkUntil = time.Time{}
	user.UpdatedAt = time.Now().UTC()
	if err := s.Users.UpdateUser(*user); err != nil {
		return err
	}
	s.audit("Account " + user.Username + " linked itself to a single sign-on identity")
	return nil
}

// UnlinkOIDC removes an account's SSO binding.
func (s *Service) UnlinkOIDC(userID string) error {
	user, err := s.Users.GetUser(userID)
	if err != nil || user == nil {
		return fmt.Errorf("user not found")
	}
	user.OIDCIssuer = ""
	user.OIDCSubject = ""
	user.OIDCLinkUntil = time.Time{}
	user.UpdatedAt = time.Now().UTC()
	return s.Users.UpdateUser(*user)
}
