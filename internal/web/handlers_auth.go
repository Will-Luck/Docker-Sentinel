package web

import (
	"encoding/json"
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
	s.renderTemplate(w, "login.html", map[string]any{"Error": r.URL.Query().Get("error")})
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

	ip := r.RemoteAddr
	session, user, err := s.deps.Auth.Login(r.Context(), username, password, ip, r.UserAgent())
	if err != nil {
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

	s.logEvent("auth", "", "User "+user.Username+" logged in from "+ip)

	if isJSON {
		writeJSON(w, http.StatusOK, map[string]string{"redirect": "/"})
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
	s.renderTemplate(w, "setup.html", map[string]any{
		"Token": r.URL.Query().Get("token"),
	})
}

// apiSetup processes first-run setup form.
func (s *Server) apiSetup(w http.ResponseWriter, r *http.Request) {
	if s.deps.Auth == nil || !s.deps.Auth.NeedsSetup() {
		writeError(w, http.StatusConflict, "setup already complete")
		return
	}

	// Verify bootstrap token.
	_ = r.ParseForm()
	token := r.FormValue("token")
	if token == "" {
		// Try JSON body.
		var body struct {
			Token    string `json:"token"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				token = body.Token
				if r.FormValue("username") == "" {
					r.Form.Set("username", body.Username)
					r.Form.Set("password", body.Password)
				}
			}
		}
	}

	if s.bootstrapToken != "" && token != s.bootstrapToken {
		writeError(w, http.StatusForbidden, "invalid setup token")
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
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
	_ = s.deps.Auth.Roles.SeedBuiltinRoles()

	// Mark setup complete.
	_ = s.deps.Auth.Settings.SaveSetting("auth_setup_complete", "true")

	// Clear bootstrap token.
	s.bootstrapToken = ""

	// Create session for the new admin.
	sessionToken, _ := auth.GenerateSessionToken()
	session := auth.Session{
		Token:     sessionToken,
		UserID:    user.ID,
		IP:        r.RemoteAddr,
		UserAgent: r.UserAgent(),
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(s.deps.Auth.SessionExpiry),
	}
	_ = s.deps.Auth.Sessions.CreateSession(session)
	auth.SetSessionCookie(w, sessionToken, session.ExpiresAt, s.deps.Auth.CookieSecure)

	s.logEvent("auth", "", "Initial admin user "+username+" created via setup wizard")

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]string{"redirect": "/"})
	} else {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// handleLogout processes logout.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := auth.GetSessionToken(r); token != "" {
		_ = s.deps.Auth.Logout(token)
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

	currentToken := auth.GetSessionToken(r)

	s.renderTemplate(w, "account.html", map[string]any{
		"CurrentUser":  ad.CurrentUser,
		"AuthEnabled":  ad.AuthEnabled,
		"CSRFToken":    ad.CSRFToken,
		"Sessions":     sessions,
		"Tokens":       tokens,
		"CurrentToken": currentToken,
		"QueueCount":   len(s.deps.Queue.List()),
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

	s.logEvent("auth", "", "User "+rc.User.Username+" changed their password")
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
			_ = s.deps.Auth.Sessions.DeleteSession(sess.Token)
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

	s.logEvent("auth", "", "API token "+body.Name+" created by "+rc.User.Username)

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
		s.logEvent("auth", "", "User "+body.Username+" created by "+rc.User.Username+" (role: "+body.RoleID+")")
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

	s.logEvent("auth", "", "User "+target.Username+" deleted by "+rc.User.Username)
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
		s.logEvent("auth", "", "Authentication "+action+" by "+rc.User.Username)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
