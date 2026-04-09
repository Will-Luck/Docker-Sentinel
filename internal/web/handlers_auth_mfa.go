package web

import (
	"encoding/json"
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

	state, err := auth.GenerateOIDCState()
	if err != nil {
		http.Error(w, "failed to generate state", http.StatusInternalServerError)
		return
	}
	nonce, err := auth.GenerateOIDCNonce()
	if err != nil {
		http.Error(w, "failed to generate nonce", http.StatusInternalServerError)
		return
	}
	pkceVerifier, err := auth.GeneratePKCEVerifier()
	if err != nil {
		http.Error(w, "failed to generate PKCE verifier", http.StatusInternalServerError)
		return
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

	http.Redirect(w, r, provider.AuthURL(state, nonce, pkceChallenge), http.StatusFound)
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
		http.Redirect(w, r, "/login?error=sso_failed", http.StatusSeeOther)
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
