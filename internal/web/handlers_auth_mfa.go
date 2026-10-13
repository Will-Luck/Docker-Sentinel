package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

// apiLoginTOTP completes 2FA login by validating a TOTP or recovery code.
func (s *Server) apiLoginTOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TOTPToken string `json:"totp_token"`
		Code      string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TOTPToken == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, "totp_token and code are required")
		return
	}

	ip := clientIP(r)
	session, err := s.deps.Auth.VerifyTOTP(r.Context(), req.TOTPToken, req.Code, ip, r.UserAgent())
	if err != nil {
		switch err {
		case auth.ErrRateLimited:
			writeError(w, http.StatusTooManyRequests, "Too many attempts, try again later")
		case auth.ErrTOTPInvalidCode:
			writeError(w, http.StatusUnauthorized, "Invalid code")
		case auth.ErrTOTPInvalidToken:
			writeError(w, http.StatusUnauthorized, "Session expired — please log in again")
		default:
			writeError(w, http.StatusUnauthorized, "Verification failed")
		}
		return
	}

	auth.SetSessionCookie(w, session.Token, session.ExpiresAt, s.deps.Auth.CookieSecure)

	// Look up user for logging.
	user, _ := s.deps.Auth.Users.GetUser(session.UserID)
	username := session.UserID
	if user != nil {
		username = user.Username
	}
	s.logEvent(r, "auth", "", "User "+username+" completed 2FA login from "+ip)

	writeJSON(w, http.StatusOK, map[string]any{"redirect": "/"})
}

// apiTOTPSetup begins 2FA setup — generates secret and returns QR data.
func (s *Server) apiTOTPSetup(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	key, _, err := s.deps.Auth.EnableTOTP(r.Context(), rc.User.ID)
	if err != nil {
		if err == auth.ErrTOTPAlreadyEnabled {
			writeError(w, http.StatusConflict, "2FA is already enabled")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to setup 2FA")
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"secret": key.Secret(),
		"qr_url": key.URL(),
		"issuer": "Docker-Sentinel",
	})
}

// apiTOTPConfirm activates 2FA after user proves they can generate codes.
func (s *Server) apiTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		writeError(w, http.StatusBadRequest, "code is required")
		return
	}

	codes, err := s.deps.Auth.ConfirmTOTP(r.Context(), rc.User.ID, req.Code)
	if err != nil {
		if err == auth.ErrTOTPInvalidCode {
			writeError(w, http.StatusUnauthorized, "Invalid code — check your authenticator app and try again")
		} else if err == auth.ErrTOTPAlreadyEnabled {
			writeError(w, http.StatusConflict, "2FA is already enabled")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to confirm 2FA")
		}
		return
	}

	s.logEvent(r, "auth", "", "User "+rc.User.Username+" enabled 2FA")

	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"recovery_codes": codes,
	})
}

// apiTOTPDisable removes 2FA from the user's account.
func (s *Server) apiTOTPDisable(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}

	err := s.deps.Auth.DisableTOTP(r.Context(), rc.User.ID, req.Password)
	if err != nil {
		switch err {
		case auth.ErrInvalidCredentials:
			writeError(w, http.StatusUnauthorized, "incorrect password")
		case auth.ErrTOTPNotEnabled:
			writeError(w, http.StatusConflict, "2FA is not enabled")
		default:
			writeError(w, http.StatusInternalServerError, "failed to disable 2FA")
		}
		return
	}

	s.logEvent(r, "auth", "", "User "+rc.User.Username+" disabled 2FA")

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiTOTPStatus returns whether the current user has 2FA enabled.
func (s *Server) apiTOTPStatus(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Re-fetch user to get current TOTP state.
	user, err := s.deps.Auth.Users.GetUser(rc.User.ID)
	if err != nil || user == nil {
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"totp_enabled":        user.TOTPEnabled,
		"recovery_codes_left": len(user.RecoveryCodes),
	})
}

// apiGetMe returns the current user's information.
func (s *Server) apiGetMe(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":           rc.User.ID,
		"username":     rc.User.Username,
		"role_id":      rc.User.RoleID,
		"permissions":  rc.Permissions,
		"auth_enabled": rc.AuthEnabled,
	})
}

// apiOIDCLogin initiates the OIDC authorization flow.
func (s *Server) apiOIDCLogin(w http.ResponseWriter, r *http.Request) {
	provider := s.getOIDCProvider()
	if provider == nil {
		http.Error(w, "OIDC not configured", http.StatusNotFound)
		return
	}
	authURL, err := s.startOIDCFlow(w, provider)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// apiOIDCLinkStart begins linking the signed-in user's account to their
// single sign-on identity. It is a POST so the CSRF check applies: a link
// started from another site would bind the attacker's identity to the victim.
func (s *Server) apiOIDCLinkStart(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil || !rc.AuthEnabled {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	provider := s.getOIDCProvider()
	if provider == nil {
		writeError(w, http.StatusNotFound, "single sign-on is not configured")
		return
	}
	authURL, err := s.startOIDCFlow(w, provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	setOIDCCookie(w, "oidc_link", rc.User.ID, s.deps.Auth.CookieSecure)
	writeJSON(w, http.StatusOK, map[string]string{"url": authURL})
}

// apiOIDCAllowLink lets the next SSO login that matches a user's username
// link to that account, for 24 hours, once (admin only).
func (s *Server) apiOIDCAllowLink(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Auth.AllowOIDCLink(r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiOIDCUnlink removes a user's single sign-on binding (admin only).
func (s *Server) apiOIDCUnlink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.deps.Auth.UnlinkOIDC(id); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	s.logEvent(r, "auth", "", "Single sign-on link removed for user "+id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// startOIDCFlow sets the state, nonce and PKCE cookies and returns the
// provider's authorisation URL.
func (s *Server) startOIDCFlow(w http.ResponseWriter, provider *auth.OIDCProvider) (string, error) {
	state, err := auth.GenerateOIDCState()
	if err != nil {
		return "", fmt.Errorf("failed to generate state")
	}
	nonce, err := auth.GenerateOIDCNonce()
	if err != nil {
		return "", fmt.Errorf("failed to generate nonce")
	}
	pkceVerifier, err := auth.GeneratePKCEVerifier()
	if err != nil {
		return "", fmt.Errorf("failed to generate PKCE verifier")
	}
	pkceChallenge := auth.PKCEChallengeFromVerifier(pkceVerifier)

	// Store state, nonce, and PKCE verifier in short-lived HttpOnly
	// cookies so the callback handler can validate them. The state
	// prevents CSRF on the callback, the nonce binds the ID token to
	// this login attempt, and the verifier proves the token exchange
	// owns the original authorization request.
	setOIDCCookie(w, "oidc_state", state, s.deps.Auth.CookieSecure)
	setOIDCCookie(w, "oidc_nonce", nonce, s.deps.Auth.CookieSecure)
	setOIDCCookie(w, "oidc_pkce", pkceVerifier, s.deps.Auth.CookieSecure)

	return provider.AuthURL(state, nonce, pkceChallenge), nil
}

// setOIDCCookie writes a short-lived (10 minute) HttpOnly cookie used
// by the OIDC login flow. SameSite=Lax is the tightest setting that
// still works with the OIDC redirect_uri GET callback.
func setOIDCCookie(w http.ResponseWriter, name, value string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearOIDCCookie removes a cookie previously set by setOIDCCookie.
func clearOIDCCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// apiOIDCCallback handles the redirect from the Identity Provider.
func (s *Server) apiOIDCCallback(w http.ResponseWriter, r *http.Request) {
	provider := s.getOIDCProvider()
	if provider == nil {
		http.Error(w, "OIDC not configured", http.StatusNotFound)
		return
	}

	// Verify state, nonce, and PKCE verifier cookies are all present
	// before doing anything else. Missing cookies mean the login flow
	// was not initiated from apiOIDCLogin — either a replay attempt
	// or a broken browser session.
	stateCookie, err := r.Cookie("oidc_state")
	if err != nil || stateCookie.Value == "" {
		http.Error(w, "missing state", http.StatusBadRequest)
		return
	}
	nonceCookie, err := r.Cookie("oidc_nonce")
	if err != nil || nonceCookie.Value == "" {
		http.Error(w, "missing nonce", http.StatusBadRequest)
		return
	}
	pkceCookie, err := r.Cookie("oidc_pkce")
	if err != nil || pkceCookie.Value == "" {
		http.Error(w, "missing PKCE verifier", http.StatusBadRequest)
		return
	}

	// Always clear the OIDC cookies once we've captured their values,
	// even if the flow fails later — they are single-use by design.
	defer func() {
		clearOIDCCookie(w, "oidc_state", s.deps.Auth.CookieSecure)
		clearOIDCCookie(w, "oidc_nonce", s.deps.Auth.CookieSecure)
		clearOIDCCookie(w, "oidc_pkce", s.deps.Auth.CookieSecure)
	}()

	if r.URL.Query().Get("state") != stateCookie.Value {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	// Check for error from IdP. The raw IdP error string is discarded from
	// the redirect to avoid reflecting attacker-controlled content into
	// the login page (finding J). Server-side logs retain full detail.
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		s.deps.Log.Warn("OIDC login error from IdP", "error", errParam, "description", desc)
		http.Redirect(w, r, "/login?error=sso_failed", http.StatusSeeOther)
		return
	}

	// Exchange code for tokens.
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	userInfo, err := provider.Exchange(r.Context(), code, pkceCookie.Value, nonceCookie.Value)
	if err != nil {
		s.deps.Log.Warn("OIDC exchange failed", "error", err)
		http.Redirect(w, r, "/login?error=sso_failed", http.StatusSeeOther)
		return
	}

	// A link intent: bind this identity to the signed-in user, no new session.
	if linkCookie, err := r.Cookie("oidc_link"); err == nil && linkCookie.Value != "" {
		clearOIDCCookie(w, "oidc_link", s.deps.Auth.CookieSecure)
		s.finishOIDCLink(w, r, linkCookie.Value, userInfo)
		return
	}

	// Create or find user and create session.
	ip := clientIP(r)
	session, err := s.deps.Auth.LoginWithOIDC(
		r.Context(), userInfo,
		provider.AutoCreate(), provider.DefaultRole(),
		provider.GroupMappings(),
		ip, r.UserAgent(),
	)
	if err != nil {
		// Discard err.Error() from the redirect URL (finding J). The detail
		// is logged server-side; the user sees a generic slug.
		s.deps.Log.Warn("OIDC login failed", "error", err, "username", userInfo.Username)
		http.Redirect(w, r, "/login?error="+oidcLoginErrorSlug(err), http.StatusSeeOther)
		return
	}

	// Set session cookie (same pattern as apiLogin).
	auth.SetSessionCookie(w, session.Token, session.ExpiresAt, s.deps.Auth.CookieSecure)

	s.logEvent(r, "auth", "", "User "+userInfo.Username+" logged in via OIDC from "+ip)

	// Redirect to dashboard.
	http.Redirect(w, r, "/", http.StatusFound)
}

// apiOIDCAvailable returns whether OIDC is configured (for login page JS).
func (s *Server) apiOIDCAvailable(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"available": s.getOIDCProvider() != nil,
	})
}

// oidcLoginErrorSlug maps a login failure to the slug shown on the login
// page. An account clash gets its own slug so the user is told what to do.
func oidcLoginErrorSlug(err error) string {
	if errors.Is(err, auth.ErrOIDCAccountExists) {
		return "sso_account_exists"
	}
	return "sso_failed"
}

// finishOIDCLink completes a link started from the Account page. The signed-in
// session must still belong to the user who started it.
func (s *Server) finishOIDCLink(w http.ResponseWriter, r *http.Request, startedBy string, info *auth.OIDCUserInfo) {
	rc := s.deps.Auth.ValidateSession(r.Context(), auth.GetSessionToken(r))
	if rc == nil || rc.User == nil || rc.User.ID != startedBy {
		http.Redirect(w, r, "/login?error=sso_failed", http.StatusSeeOther)
		return
	}
	if err := s.deps.Auth.LinkOIDC(rc.User.ID, info); err != nil {
		s.deps.Log.Warn("OIDC link failed", "error", err, "user", rc.User.Username)
		slug := "sso_link_failed"
		if errors.Is(err, auth.ErrOIDCIdentityTaken) {
			slug = "sso_identity_taken"
		}
		http.Redirect(w, r, "/account?sso="+slug, http.StatusSeeOther)
		return
	}
	s.logEvent(r, "auth", "", "User "+rc.User.Username+" linked a single sign-on identity")
	http.Redirect(w, r, "/account?sso=linked", http.StatusSeeOther)
}
