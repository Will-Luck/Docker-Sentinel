package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

// WizardDeps holds the minimal dependencies for the setup wizard server.
type WizardDeps struct {
	SettingsStore SettingsStore
	Auth          *auth.Service
	Log           *slog.Logger
	Version       string
	ClusterPort   string
}

// WizardServer is a stripped-down HTTP server that only serves the setup wizard.
// It runs instead of the full server on first-run and shuts down once setup completes.
type WizardServer struct {
	deps          WizardDeps
	mux           *http.ServeMux
	tmpl          *template.Template
	server        *http.Server
	setupDeadline time.Time
	done          chan struct{}
	closeOnce     sync.Once
}

// NewWizardServer creates a WizardServer ready to serve.
func NewWizardServer(deps WizardDeps) *WizardServer {
	ws := &WizardServer{
		deps:          deps,
		mux:           http.NewServeMux(),
		setupDeadline: time.Now().Add(5 * time.Minute),
		done:          make(chan struct{}),
	}
	ws.tmpl = template.Must(template.New("").ParseFS(staticFS, "static/*.html"))
	ws.registerRoutes()
	return ws
}

// Done returns a channel that is closed when wizard setup completes.
func (ws *WizardServer) Done() <-chan struct{} {
	return ws.done
}

func (ws *WizardServer) setupWindowOpen() bool {
	return time.Now().Before(ws.setupDeadline)
}

func (ws *WizardServer) registerRoutes() {
	ws.mux.HandleFunc("GET /setup", ws.handleSetup)
	ws.mux.HandleFunc("POST /api/setup", ws.apiSetup)
	ws.mux.HandleFunc("POST /api/setup/test-enrollment", ws.apiTestEnrollment)
	ws.mux.HandleFunc("GET /static/", ws.serveStatic)
	ws.mux.HandleFunc("GET /favicon.svg", ws.serveFaviconSVG)
	ws.mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	// Catch-all: redirect everything else to /setup.
	ws.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
	})
}

func (ws *WizardServer) handleSetup(w http.ResponseWriter, r *http.Request) {
	remaining := time.Until(ws.setupDeadline)
	_ = ws.tmpl.ExecuteTemplate(w, "setup.html", map[string]any{
		"Expired":          !ws.setupWindowOpen(),
		"RemainingSeconds": int(remaining.Seconds()),
	})
}

// wizardRequest is the JSON body accepted by POST /api/setup.
type wizardRequest struct {
	Role           string `json:"role"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	DefaultPolicy  string `json:"default_policy,omitempty"`
	PollInterval   string `json:"poll_interval,omitempty"`
	ClusterEnabled bool   `json:"cluster_enabled,omitempty"`
	ServerAddr     string `json:"server_addr,omitempty"`
	EnrollToken    string `json:"enroll_token,omitempty"`
	HostName       string `json:"host_name,omitempty"`
}

func (ws *WizardServer) apiSetup(w http.ResponseWriter, r *http.Request) {
	if !ws.setupWindowOpen() {
		writeWizardError(w, http.StatusForbidden, "setup window has expired — restart the container to try again")
		return
	}

	var req wizardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeWizardError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Role != "server" && req.Role != "agent" {
		writeWizardError(w, http.StatusBadRequest, "role must be 'server' or 'agent'")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeWizardError(w, http.StatusBadRequest, "username and password required")
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		writeWizardError(w, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeWizardError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}
	userID, err := auth.GenerateUserID()
	if err != nil {
		writeWizardError(w, http.StatusInternalServerError, "failed to generate user ID")
		return
	}

	user := auth.User{
		ID:           userID,
		Username:     req.Username,
		PasswordHash: hash,
		RoleID:       auth.RoleAdminID,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := ws.deps.Auth.Users.CreateFirstUser(user); err != nil {
		if err == auth.ErrUsersExist {
			writeWizardError(w, http.StatusConflict, "setup already complete")
		} else {
			writeWizardError(w, http.StatusInternalServerError, "failed to create admin user")
		}
		return
	}

	if err := ws.deps.Auth.Roles.SeedBuiltinRoles(); err != nil {
		ws.deps.Log.Warn("wizard: failed to seed builtin roles", "error", err)
	}

	// Save instance_role.
	if err := ws.deps.SettingsStore.SaveSetting("instance_role", req.Role); err != nil {
		ws.deps.Log.Warn("wizard: failed to save instance_role", "error", err)
	}

	// Role-specific settings.
	switch req.Role {
	case "server":
		if req.DefaultPolicy != "" {
			_ = ws.deps.SettingsStore.SaveSetting("default_policy", req.DefaultPolicy)
		}
		if req.PollInterval != "" {
			_ = ws.deps.SettingsStore.SaveSetting("poll_interval", req.PollInterval)
		}
		clusterVal := "false"
		if req.ClusterEnabled {
			clusterVal = "true"
		}
		_ = ws.deps.SettingsStore.SaveSetting("cluster_enabled", clusterVal)
	case "agent":
		if req.ServerAddr != "" {
			_ = ws.deps.SettingsStore.SaveSetting("server_addr", req.ServerAddr)
		}
		if req.EnrollToken != "" {
			_ = ws.deps.SettingsStore.SaveSetting("enroll_token", req.EnrollToken)
		}
		if req.HostName != "" {
			_ = ws.deps.SettingsStore.SaveSetting("host_name", req.HostName)
		}
	}

	if err := ws.deps.SettingsStore.SaveSetting("auth_setup_complete", "true"); err != nil {
		ws.deps.Log.Warn("wizard: failed to save auth_setup_complete", "error", err)
	}

	// Create session so the user lands on the dashboard after redirect.
	sessionToken, err := auth.GenerateSessionToken()
	if err != nil {
		writeWizardError(w, http.StatusInternalServerError, "failed to generate session token")
		return
	}
	session := auth.Session{
		Token:     sessionToken,
		UserID:    user.ID,
		IP:        wizardClientIP(r),
		UserAgent: r.UserAgent(),
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(ws.deps.Auth.SessionExpiry),
	}
	if err := ws.deps.Auth.Sessions.CreateSession(session); err != nil {
		writeWizardError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	auth.SetSessionCookie(w, sessionToken, session.ExpiresAt, ws.deps.Auth.CookieSecure)

	ws.deps.Log.Info("wizard setup complete", "role", req.Role, "user", req.Username)

	writeWizardJSON(w, http.StatusOK, map[string]string{"redirect": "/"})

	// Signal completion so main.go can proceed.
	ws.closeOnce.Do(func() { close(ws.done) })
}

// apiTestEnrollment dials the configured server address to verify connectivity.
func (ws *WizardServer) apiTestEnrollment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerAddr string `json:"server_addr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ServerAddr == "" {
		writeWizardError(w, http.StatusBadRequest, "server_addr required")
		return
	}

	conn, err := net.DialTimeout("tcp", req.ServerAddr, 5*time.Second)
	if err != nil {
		writeWizardJSON(w, http.StatusOK, map[string]any{
			"reachable": false,
			"error":     fmt.Sprintf("connection failed: %v", err),
		})
		return
	}
	conn.Close()
	writeWizardJSON(w, http.StatusOK, map[string]any{"reachable": true})
}

func (ws *WizardServer) serveStatic(w http.ResponseWriter, r *http.Request) {
	path := "static" + strings.TrimPrefix(r.URL.Path, "/static")
	data, err := staticFS.ReadFile(path)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch {
	case strings.HasSuffix(path, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(path, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case strings.HasSuffix(path, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	case strings.HasSuffix(path, ".png"):
		w.Header().Set("Content-Type", "image/png")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	_, _ = w.Write(data)
}

func (ws *WizardServer) serveFaviconSVG(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/favicon.svg")
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(data)
}

// ListenAndServe starts the wizard HTTP server on addr.
func (ws *WizardServer) ListenAndServe(addr string) error {
	ws.server = &http.Server{
		Addr:         addr,
		Handler:      ws.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	ws.deps.Log.Info("wizard server listening", "addr", addr)
	return ws.server.ListenAndServe()
}

// Shutdown gracefully stops the wizard server.
func (ws *WizardServer) Shutdown(ctx context.Context) error {
	if ws.server == nil {
		return nil
	}
	return ws.server.Shutdown(ctx)
}

func writeWizardJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeWizardError(w http.ResponseWriter, status int, msg string) {
	writeWizardJSON(w, status, map[string]string{"error": msg})
}

func wizardClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
