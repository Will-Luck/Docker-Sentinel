package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

// authPageData extends template data with authentication context.
type authPageData struct {
	CurrentUser *auth.User `json:"-"`
	AuthEnabled bool       `json:"-"`
	CSRFToken   string     `json:"-"`
}

// getAuthData extracts authentication context from the request.
func (s *Server) getAuthData(r *http.Request) authPageData {
	rc := auth.GetRequestContext(r.Context())
	var data authPageData
	if rc != nil {
		data.CurrentUser = rc.User
		data.AuthEnabled = rc.AuthEnabled
	}
	if cookie, err := r.Cookie(auth.CSRFCookieName); err == nil {
		data.CSRFToken = cookie.Value
	}
	return data
}

// handleLogin renders the login page.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.deps.Auth != nil && !s.deps.Auth.AuthEnabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// If already logged in, redirect to dashboard.
	if token := auth.GetSessionToken(r); token != "" {
		if rc := s.deps.Auth.ValidateSession(r.Context(), token); rc != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	s.renderTemplate(w, "login.html", map[string]any{
		"Error":           r.URL.Query().Get("error"),
		"WebAuthnEnabled": s.webauthn != nil,
		"OIDCEnabled":     s.getOIDCProvider() != nil,
	})
}

// apiLogin processes login form submission.
func (s *Server) apiLogin(w http.ResponseWriter, r *http.Request) {
	var username, password string

	ct := r.Header.Get("Content-Type")
	isJSON := strings.Contains(ct, "application/json")
	if isJSON {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		username = body.Username
		password = body.Password
	} else {
		_ = r.ParseForm()
		username = r.FormValue("username")
		password = r.FormValue("password")
	}

	loginError := func(code int, msg string) {
		if isJSON {
			writeError(w, code, msg)
		} else {
			http.Redirect(w, r, "/login?error="+msg, http.StatusSeeOther)
		}
	}

	if username == "" || password == "" {
		loginError(http.StatusBadRequest, "Username and password required")
		return
	}

	ip := clientIP(r)
	session, user, err := s.deps.Auth.Login(r.Context(), username, password, ip, r.UserAgent())
	if err != nil {
		// Check for TOTP required (2FA step).
		var totpErr *auth.ErrTOTPRequired
		if errors.As(err, &totpErr) {
			if isJSON {
				writeJSON(w, http.StatusOK, map[string]any{
					"totp_required": true,
					"totp_token":    totpErr.PendingToken,
				})
			} else {
				// For form submissions, redirect to login with a hint.
				http.Redirect(w, r, "/login?totp=1", http.StatusSeeOther)
			}
			return
		}

		switch err {
		case auth.ErrRateLimited:
			loginError(http.StatusTooManyRequests, "Too many login attempts, try again later")
		case auth.ErrAccountLocked:
			loginError(http.StatusForbidden, "Account is temporarily locked")
		default:
			loginError(http.StatusUnauthorized, "Invalid username or password")
		}
		return
	}

	auth.SetSessionCookie(w, session.Token, session.ExpiresAt, s.deps.Auth.CookieSecure)

	s.logEvent(r, "auth", "", "User "+user.Username+" logged in from "+ip)

	if isJSON {
		resp := map[string]any{"redirect": "/"}
		if s.webauthn != nil && !s.deps.Auth.HasPasskeys(user.ID) {
			resp["suggest_passkey"] = true
		}
		writeJSON(w, http.StatusOK, resp)
	} else {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// handleSetup renders the first-run setup page.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.deps.Auth == nil || !s.deps.Auth.NeedsSetup() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	remaining := time.Until(s.setupDeadline)
	s.renderTemplate(w, "setup.html", map[string]any{
		"Expired":          !s.setupWindowOpen(),
		"RemainingSeconds": int(remaining.Seconds()),
	})
}

// apiSetup processes first-run setup form.
func (s *Server) apiSetup(w http.ResponseWriter, r *http.Request) {
	if s.deps.Auth == nil || !s.deps.Auth.NeedsSetup() {
		writeError(w, http.StatusConflict, "setup already complete")
		return
	}

	if !s.setupWindowOpen() {
		writeError(w, http.StatusForbidden, "setup window has expired — restart the container to try again")
		return
	}

	var username, password string
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			username = body.Username
			password = body.Password
		}
	} else {
		_ = r.ParseForm()
		username = r.FormValue("username")
		password = r.FormValue("password")
	}
	if username == "" || password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}

	if err := auth.ValidatePassword(password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	userID, err := auth.GenerateUserID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate user ID")
		return
	}

	user := auth.User{
		ID:           userID,
		Username:     username,
		PasswordHash: hash,
		RoleID:       auth.RoleAdminID,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	// Atomic: create first user only if none exist.
	if err := s.deps.Auth.Users.CreateFirstUser(user); err != nil {
		if err == auth.ErrUsersExist {
			writeError(w, http.StatusConflict, "setup already complete")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to create admin user")
		}
		return
	}

	// Seed built-in roles.
	if err := s.deps.Auth.Roles.SeedBuiltinRoles(); err != nil {
		s.deps.Log.Warn("failed to seed builtin roles", "error", err)
	}

	// Mark setup complete.
	if err := s.deps.Auth.Settings.SaveSetting("auth_setup_complete", "true"); err != nil {
		s.deps.Log.Warn("failed to save auth setup complete", "error", err)
	}

	// Close the setup window.
	s.setupDeadline = time.Time{}

	// Create session for the new admin.
	sessionToken, err := auth.GenerateSessionToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate session token")
		return
	}
	session := auth.Session{
		Token:     sessionToken,
		UserID:    user.ID,
		IP:        clientIP(r),
		UserAgent: r.UserAgent(),
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(s.deps.Auth.SessionExpiry),
	}
	if err := s.deps.Auth.Sessions.CreateSession(session); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	auth.SetSessionCookie(w, sessionToken, session.ExpiresAt, s.deps.Auth.CookieSecure)

	s.logEvent(r, "auth", "", "Initial admin user "+username+" created via setup wizard")

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]string{"redirect": "/"})
	} else {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// handleLogout processes logout.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := auth.GetSessionToken(r); token != "" {
		if err := s.deps.Auth.Logout(token); err != nil {
			s.deps.Log.Debug("failed to clear session on logout", "error", err)
		}
	}
	auth.ClearSessionCookie(w, s.deps.Auth.CookieSecure)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleAccount renders the My Account page.
func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	ad := s.getAuthData(r)
	if ad.CurrentUser == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	sessions, _ := s.deps.Auth.Sessions.ListSessionsForUser(ad.CurrentUser.ID)
	tokens, _ := s.deps.Auth.Tokens.ListAPITokensForUser(ad.CurrentUser.ID)

	var passkeys []auth.WebAuthnCredential
	if s.deps.Auth.WebAuthnCreds != nil {
		passkeys, _ = s.deps.Auth.WebAuthnCreds.ListWebAuthnCredentialsForUser(ad.CurrentUser.ID)
	}

	currentToken := auth.GetSessionToken(r)

	s.renderTemplate(w, "account.html", map[string]any{
		"CurrentUser":      ad.CurrentUser,
		"AuthEnabled":      ad.AuthEnabled,
		"CSRFToken":        ad.CSRFToken,
		"Sessions":         sessions,
		"APITokens":        tokens,
		"Passkeys":         passkeys,
		"WebAuthnEnabled":  s.webauthn != nil,
		"CurrentToken":     currentToken,
		"QueueCount":       len(s.deps.Queue.List()),
		"PortainerEnabled": s.isPortainerEnabled(),
		"ClusterEnabled":   s.deps.Cluster != nil && s.deps.Cluster.Enabled(),
	})
}

// apiChangePassword changes the current user's password.
func (s *Server) apiChangePassword(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !auth.CheckPassword(rc.User.PasswordHash, body.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}

	if err := auth.ValidatePassword(body.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	rc.User.PasswordHash = hash
	rc.User.UpdatedAt = time.Now().UTC()
	if err := s.deps.Auth.Users.UpdateUser(*rc.User); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update password")
		return
	}

	s.logEvent(r, "auth", "", "User "+rc.User.Username+" changed their password")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiListSessions returns the current user's active sessions.
func (s *Server) apiListSessions(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	sessions, err := s.deps.Auth.Sessions.ListSessionsForUser(rc.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

// apiRevokeSession revokes a specific session.
func (s *Server) apiRevokeSession(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	token := r.PathValue("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "session token required")
		return
	}

	// Verify the session belongs to the current user.
	session, err := s.deps.Auth.Sessions.GetSession(token)
	if err != nil || session == nil || session.UserID != rc.User.ID {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	if err := s.deps.Auth.Sessions.DeleteSession(token); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke session")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiRevokeAllSessions revokes all sessions except the current one.
func (s *Server) apiRevokeAllSessions(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	currentToken := auth.GetSessionToken(r)
	sessions, _ := s.deps.Auth.Sessions.ListSessionsForUser(rc.User.ID)
	for _, sess := range sessions {
		if sess.Token != currentToken {
			if err := s.deps.Auth.Sessions.DeleteSession(sess.Token); err != nil {
				s.deps.Log.Debug("failed to delete revoked session", "error", err)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiCreateToken creates a new API bearer token.
func (s *Server) apiCreateToken(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeError(w, http.StatusBadRequest, "token name required")
		return
	}

	plaintext, hash, err := auth.GenerateAPIToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	tokenID, err := auth.GenerateTokenID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token ID")
		return
	}

	apiToken := auth.APIToken{
		ID:        tokenID,
		Name:      body.Name,
		TokenHash: hash,
		UserID:    rc.User.ID,
		CreatedAt: time.Now().UTC(),
	}

	if err := s.deps.Auth.Tokens.CreateAPIToken(apiToken); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create token")
		return
	}

	s.logEvent(r, "auth", "", "API token "+body.Name+" created by "+rc.User.Username)

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":    tokenID,
		"name":  body.Name,
		"token": plaintext,
	})
}

// apiDeleteToken deletes an API token.
func (s *Server) apiDeleteToken(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "token ID required")
		return
	}

	// Users can only delete their own tokens (admin can delete any via user management).
	tokens, _ := s.deps.Auth.Tokens.ListAPITokensForUser(rc.User.ID)
	found := false
	for _, t := range tokens {
		if t.ID == id {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "token not found")
		return
	}

	if err := s.deps.Auth.Tokens.DeleteAPIToken(id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiListUsers returns all users (admin only).
func (s *Server) apiListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.deps.Auth.Users.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list users")
		return
	}

	// Strip password hashes from response.
	type safeUser struct {
		ID        string    `json:"id"`
		Username  string    `json:"username"`
		RoleID    string    `json:"role_id"`
		CreatedAt time.Time `json:"created_at"`
		Locked    bool      `json:"locked"`
	}
	result := make([]safeUser, len(users))
	for i, u := range users {
		result[i] = safeUser{
			ID:        u.ID,
			Username:  u.Username,
			RoleID:    u.RoleID,
			CreatedAt: u.CreatedAt,
			Locked:    u.Locked,
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// apiCreateUser creates a new user (admin only).
func (s *Server) apiCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		RoleID   string `json:"role_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Username == "" || body.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}

	// Validate role.
	switch body.RoleID {
	case auth.RoleAdminID, auth.RoleOperatorID, auth.RoleViewerID:
		// valid
	default:
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}

	if err := auth.ValidatePassword(body.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	userID, err := auth.GenerateUserID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate user ID")
		return
	}

	user := auth.User{
		ID:           userID,
		Username:     body.Username,
		PasswordHash: hash,
		RoleID:       body.RoleID,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}

	if err := s.deps.Auth.Users.CreateUser(user); err != nil {
		writeError(w, http.StatusConflict, "username already exists")
		return
	}

	rc := auth.GetRequestContext(r.Context())
	if rc != nil && rc.User != nil {
		s.logEvent(r, "auth", "", "User "+body.Username+" created by "+rc.User.Username+" (role: "+body.RoleID+")")
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": userID, "username": body.Username})
}

// apiDeleteUser deletes a user (admin only).
func (s *Server) apiDeleteUser(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "user ID required")
		return
	}

	// Prevent self-deletion.
	if id == rc.User.ID {
		writeError(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}

	target, err := s.deps.Auth.Users.GetUser(id)
	if err != nil || target == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	if err := s.deps.Auth.Users.DeleteUser(id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}

	s.logEvent(r, "auth", "", "User "+target.Username+" deleted by "+rc.User.Username)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiAuthSettings handles auth toggle (admin only).
func (s *Server) apiAuthSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AuthEnabled bool `json:"auth_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	val := "true"
	if !body.AuthEnabled {
		val = "false"
	}

	if err := s.deps.Auth.Settings.SaveSetting("auth_enabled", val); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}

	rc := auth.GetRequestContext(r.Context())
	if rc != nil && rc.User != nil {
		action := "enabled"
		if !body.AuthEnabled {
			action = "disabled"
		}
		s.logEvent(r, "auth", "", "Authentication "+action+" by "+rc.User.Username)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

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

	// Store state in a short-lived cookie (10 min, HttpOnly).
	http.SetCookie(w, &http.Cookie{
		Name:     "oidc_state",
		Value:    state,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   s.deps.Auth.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, provider.AuthURL(state), http.StatusFound)
}

// apiOIDCCallback handles the redirect from the Identity Provider.
func (s *Server) apiOIDCCallback(w http.ResponseWriter, r *http.Request) {
	provider := s.getOIDCProvider()
	if provider == nil {
		http.Error(w, "OIDC not configured", http.StatusNotFound)
		return
	}

	// Verify state from cookie.
	stateCookie, err := r.Cookie("oidc_state")
	if err != nil || stateCookie.Value == "" {
		http.Error(w, "missing state", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("state") != stateCookie.Value {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	// Clear the state cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     "oidc_state",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.deps.Auth.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	// Check for error from IdP.
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		s.deps.Log.Warn("OIDC login error from IdP", "error", errParam, "description", desc)
		http.Redirect(w, r, "/login?error=SSO+login+failed:+"+errParam, http.StatusSeeOther)
		return
	}

	// Exchange code for tokens.
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	userInfo, err := provider.Exchange(r.Context(), code)
	if err != nil {
		s.deps.Log.Warn("OIDC exchange failed", "error", err)
		http.Redirect(w, r, "/login?error=SSO+authentication+failed", http.StatusSeeOther)
		return
	}

	// Create or find user and create session.
	ip := clientIP(r)
	session, err := s.deps.Auth.LoginWithOIDC(
		r.Context(), userInfo,
		provider.AutoCreate(), provider.DefaultRole(),
		ip, r.UserAgent(),
	)
	if err != nil {
		s.deps.Log.Warn("OIDC login failed", "error", err, "username", userInfo.Username)
		http.Redirect(w, r, "/login?error=SSO+login+failed:+"+err.Error(), http.StatusSeeOther)
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
