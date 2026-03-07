package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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
			http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusSeeOther)
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

	// Invalidate all existing sessions for this user (security: old sessions
	// authenticated with the previous password should no longer be valid).
	if err := s.deps.Auth.Sessions.DeleteSessionsForUser(rc.User.ID); err != nil {
		s.deps.Log.Warn("failed to delete sessions after password change", "error", err)
	}

	// Re-create a fresh session so the current user stays logged in.
	newToken, err := auth.GenerateSessionToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate session token")
		return
	}
	newSession := auth.Session{
		Token:     newToken,
		UserID:    rc.User.ID,
		IP:        clientIP(r),
		UserAgent: r.UserAgent(),
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(s.deps.Auth.SessionExpiry),
	}
	if err := s.deps.Auth.Sessions.CreateSession(newSession); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	auth.SetSessionCookie(w, newToken, newSession.ExpiresAt, s.deps.Auth.CookieSecure)

	s.logEvent(r, "auth", "", "User "+rc.User.Username+" changed their password")
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
