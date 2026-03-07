package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

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
