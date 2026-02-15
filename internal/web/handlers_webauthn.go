package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

// webauthnUser wraps auth.User to satisfy the webauthn.User interface.
// Credentials are loaded from the store and injected before ceremonies.
type webauthnUser struct {
	user  *auth.User
	creds []webauthn.Credential
}

func (wu *webauthnUser) WebAuthnID() []byte                         { return wu.user.WebAuthnUserID }
func (wu *webauthnUser) WebAuthnName() string                       { return wu.user.Username }
func (wu *webauthnUser) WebAuthnDisplayName() string                { return wu.user.Username }
func (wu *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return wu.creds }

// toWebAuthnCredentials converts auth.WebAuthnCredential slice to webauthn.Credential slice.
func toWebAuthnCredentials(creds []auth.WebAuthnCredential) []webauthn.Credential {
	result := make([]webauthn.Credential, len(creds))
	for i, c := range creds {
		var transport []protocol.AuthenticatorTransport
		for _, t := range c.Transport {
			transport = append(transport, protocol.AuthenticatorTransport(t))
		}
		result[i] = webauthn.Credential{
			ID:              c.ID,
			PublicKey:       c.PublicKey,
			AttestationType: c.AttestationType,
			Transport:       transport,
			Flags: webauthn.CredentialFlags{
				UserPresent:    c.Flags.UserPresent,
				UserVerified:   c.Flags.UserVerified,
				BackupEligible: c.Flags.BackupEligible,
				BackupState:    c.Flags.BackupState,
			},
			Authenticator: webauthn.Authenticator{
				AAGUID:       c.Authenticator.AAGUID,
				SignCount:    c.Authenticator.SignCount,
				CloneWarning: c.Authenticator.CloneWarning,
				Attachment:   protocol.AuthenticatorAttachment(c.Authenticator.Attachment),
			},
		}
	}
	return result
}

// fromWebAuthnCredential converts a webauthn.Credential to auth.WebAuthnCredential.
func fromWebAuthnCredential(cred *webauthn.Credential, userID, name string) auth.WebAuthnCredential {
	var transport []string
	for _, t := range cred.Transport {
		transport = append(transport, string(t))
	}
	return auth.WebAuthnCredential{
		ID:              cred.ID,
		PublicKey:       cred.PublicKey,
		AttestationType: cred.AttestationType,
		Transport:       transport,
		Flags: auth.WebAuthnFlags{
			UserPresent:    cred.Flags.UserPresent,
			UserVerified:   cred.Flags.UserVerified,
			BackupEligible: cred.Flags.BackupEligible,
			BackupState:    cred.Flags.BackupState,
		},
		Authenticator: auth.WebAuthnAuthenticator{
			AAGUID:       cred.Authenticator.AAGUID,
			SignCount:    cred.Authenticator.SignCount,
			CloneWarning: cred.Authenticator.CloneWarning,
			Attachment:   string(cred.Authenticator.Attachment),
		},
		UserID:    userID,
		Name:      name,
		CreatedAt: time.Now().UTC(),
	}
}

// apiPasskeyRegisterBegin starts the WebAuthn registration ceremony.
// POST /api/auth/passkeys/register/begin (session + CSRF required)
func (s *Server) apiPasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if s.webauthn == nil {
		writeError(w, http.StatusNotFound, "passkeys not configured")
		return
	}

	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	user := rc.User
	changed, err := user.EnsureWebAuthnUserID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate WebAuthn user ID")
		return
	}
	if changed {
		user.UpdatedAt = time.Now().UTC()
		if err := s.deps.Auth.Users.UpdateUser(*user); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist user")
			return
		}
	}

	// Load existing credentials for the exclusion list.
	var existingCreds []webauthn.Credential
	if s.deps.Auth.WebAuthnCreds != nil {
		stored, _ := s.deps.Auth.WebAuthnCreds.ListWebAuthnCredentialsForUser(user.ID)
		existingCreds = toWebAuthnCredentials(stored)
	}

	wu := &webauthnUser{user: user, creds: existingCreds}

	creation, sessionData, err := s.webauthn.BeginRegistration(wu,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationPreferred,
		}),
	)
	if err != nil {
		s.deps.Log.Error("webauthn begin registration failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to begin registration")
		return
	}

	s.deps.Auth.Ceremonies.Put("register::"+user.ID, sessionData, user.ID)

	writeJSON(w, http.StatusOK, creation)
}

// apiPasskeyRegisterFinish completes the WebAuthn registration ceremony.
// POST /api/auth/passkeys/register/finish (session + CSRF required)
func (s *Server) apiPasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if s.webauthn == nil {
		writeError(w, http.StatusNotFound, "passkeys not configured")
		return
	}

	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	user := rc.User
	ceremonyKey := "register::" + user.ID
	ceremony := s.deps.Auth.Ceremonies.Get(ceremonyKey)
	if ceremony == nil {
		writeError(w, http.StatusBadRequest, "no pending registration ceremony")
		return
	}

	sessionData, ok := ceremony.Data.(*webauthn.SessionData)
	if !ok {
		writeError(w, http.StatusInternalServerError, "invalid ceremony data")
		return
	}

	// Load existing credentials for the user wrapper.
	var existingCreds []webauthn.Credential
	if s.deps.Auth.WebAuthnCreds != nil {
		stored, _ := s.deps.Auth.WebAuthnCreds.ListWebAuthnCredentialsForUser(user.ID)
		existingCreds = toWebAuthnCredentials(stored)
	}

	wu := &webauthnUser{user: user, creds: existingCreds}

	credential, err := s.webauthn.FinishRegistration(wu, *sessionData, r)
	if err != nil {
		s.deps.Log.Warn("webauthn finish registration failed", "error", err, "user", user.Username)
		writeError(w, http.StatusBadRequest, "registration verification failed")
		return
	}

	// Derive credential name from query param or default.
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "Passkey"
	}

	authCred := fromWebAuthnCredential(credential, user.ID, name)
	if err := s.deps.Auth.WebAuthnCreds.CreateWebAuthnCredential(authCred); err != nil {
		s.deps.Log.Error("failed to store webauthn credential", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store credential")
		return
	}

	s.logEvent(r, "auth", "", "Passkey \""+name+"\" registered by "+user.Username)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "name": name})
}

// apiPasskeyLoginBegin starts the WebAuthn discoverable login ceremony.
// POST /api/auth/passkeys/login/begin (no auth required)
func (s *Server) apiPasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if s.webauthn == nil {
		writeError(w, http.StatusNotFound, "passkeys not configured")
		return
	}

	assertion, sessionData, err := s.webauthn.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationPreferred),
	)
	if err != nil {
		s.deps.Log.Error("webauthn begin discoverable login failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to begin passkey login")
		return
	}

	// Generate a random session ID to link Begin and Finish.
	sessionIDBuf := make([]byte, 16)
	if _, err := rand.Read(sessionIDBuf); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate session ID")
		return
	}
	sessionID := hex.EncodeToString(sessionIDBuf)

	s.deps.Auth.Ceremonies.Put("login::"+sessionID, sessionData, "")

	http.SetCookie(w, &http.Cookie{
		Name:     "sentinel_webauthn_session",
		Value:    sessionID,
		Path:     "/",
		MaxAge:   60,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.deps.Auth.CookieSecure,
	})

	writeJSON(w, http.StatusOK, assertion)
}

// apiPasskeyLoginFinish completes the WebAuthn discoverable login ceremony.
// POST /api/auth/passkeys/login/finish (no auth required)
func (s *Server) apiPasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if s.webauthn == nil {
		writeError(w, http.StatusNotFound, "passkeys not configured")
		return
	}

	cookie, err := r.Cookie("sentinel_webauthn_session")
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusBadRequest, "no pending login ceremony")
		return
	}
	sessionID := cookie.Value

	ceremony := s.deps.Auth.Ceremonies.Get("login::" + sessionID)
	if ceremony == nil {
		writeError(w, http.StatusBadRequest, "login ceremony expired or not found")
		return
	}

	sessionData, ok := ceremony.Data.(*webauthn.SessionData)
	if !ok {
		writeError(w, http.StatusInternalServerError, "invalid ceremony data")
		return
	}

	// Closure captures the resolved user during discoverable login.
	var resolvedUser *auth.User
	userHandler := func(rawID, userHandle []byte) (webauthn.User, error) {
		user, err := s.deps.Auth.WebAuthnCreds.GetUserByWebAuthnHandle(userHandle)
		if err != nil || user == nil {
			return nil, auth.ErrCredentialNotFound
		}
		resolvedUser = user
		creds, _ := s.deps.Auth.WebAuthnCreds.ListWebAuthnCredentialsForUser(user.ID)
		return &webauthnUser{user: user, creds: toWebAuthnCredentials(creds)}, nil
	}

	credential, err := s.webauthn.FinishDiscoverableLogin(userHandler, *sessionData, r)
	if err != nil {
		s.deps.Log.Warn("webauthn finish discoverable login failed", "error", err)
		writeError(w, http.StatusUnauthorized, "passkey authentication failed")
		return
	}

	// Clear the ceremony cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     "sentinel_webauthn_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.deps.Auth.CookieSecure,
	})

	// Best-effort sign count update on the stored credential.
	if credential.Authenticator.SignCount > 0 {
		stored, storeErr := s.deps.Auth.WebAuthnCreds.GetWebAuthnCredential(credential.ID)
		if storeErr == nil && stored != nil {
			stored.Authenticator.SignCount = credential.Authenticator.SignCount
			_ = s.deps.Auth.WebAuthnCreds.DeleteWebAuthnCredential(stored.ID)
			_ = s.deps.Auth.WebAuthnCreds.CreateWebAuthnCredential(*stored)
		}
	}

	if resolvedUser == nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve credential owner")
		return
	}

	session, user, err := s.deps.Auth.LoginWithWebAuthn(r.Context(), resolvedUser.ID, clientIP(r), r.UserAgent())
	if err != nil {
		switch err {
		case auth.ErrRateLimited:
			writeError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		case auth.ErrAccountLocked:
			writeError(w, http.StatusForbidden, "account is temporarily locked")
		default:
			writeError(w, http.StatusUnauthorized, "authentication failed")
		}
		return
	}

	auth.SetSessionCookie(w, session.Token, session.ExpiresAt, s.deps.Auth.CookieSecure)

	s.logEvent(r, "auth", "", "User "+user.Username+" logged in via passkey from "+clientIP(r))

	writeJSON(w, http.StatusOK, map[string]string{"redirect": "/"})
}

// apiListPasskeys returns the current user's registered passkeys.
// GET /api/auth/passkeys (session required)
func (s *Server) apiListPasskeys(w http.ResponseWriter, r *http.Request) {
	if s.webauthn == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	if s.deps.Auth.WebAuthnCreds == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	creds, err := s.deps.Auth.WebAuthnCreds.ListWebAuthnCredentialsForUser(rc.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list passkeys")
		return
	}

	type passkeyView struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		CreatedAt string   `json:"created_at"`
		Transport []string `json:"transport,omitempty"`
	}

	result := make([]passkeyView, len(creds))
	for i, c := range creds {
		result[i] = passkeyView{
			ID:        base64.RawURLEncoding.EncodeToString(c.ID),
			Name:      c.Name,
			CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
			Transport: c.Transport,
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// apiDeletePasskey removes a passkey credential.
// DELETE /api/auth/passkeys/{id} (session + CSRF required)
func (s *Server) apiDeletePasskey(w http.ResponseWriter, r *http.Request) {
	if s.webauthn == nil {
		writeError(w, http.StatusNotFound, "passkeys not configured")
		return
	}

	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	idParam := r.PathValue("id")
	if idParam == "" {
		writeError(w, http.StatusBadRequest, "credential ID required")
		return
	}

	credID, err := base64.RawURLEncoding.DecodeString(idParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid credential ID")
		return
	}

	cred, err := s.deps.Auth.WebAuthnCreds.GetWebAuthnCredential(credID)
	if err != nil || cred == nil {
		writeError(w, http.StatusNotFound, "passkey not found")
		return
	}

	// Verify the credential belongs to the current user.
	if cred.UserID != rc.User.ID {
		writeError(w, http.StatusNotFound, "passkey not found")
		return
	}

	if err := s.deps.Auth.WebAuthnCreds.DeleteWebAuthnCredential(credID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete passkey")
		return
	}

	s.logEvent(r, "auth", "", "Passkey \""+cred.Name+"\" deleted by "+rc.User.Username)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiPasskeysAvailable reports whether passkey login is available.
// GET /api/auth/passkeys/available (no auth required)
func (s *Server) apiPasskeysAvailable(w http.ResponseWriter, r *http.Request) {
	configured := s.webauthn != nil
	available := false

	if configured && s.deps.Auth.WebAuthnCreds != nil {
		exists, err := s.deps.Auth.WebAuthnCreds.AnyWebAuthnCredentialsExist()
		if err == nil {
			available = exists
		}
	}

	writeJSON(w, http.StatusOK, map[string]bool{
		"configured": configured,
		"available":  available,
	})
}
