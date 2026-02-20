package web

import (
	"context"
	"encoding/json"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

// AgentStatusProvider is implemented by the agent to expose live status.
type AgentStatusProvider interface {
	Connected() bool
	ContainerCount() int
}

// AgentDeps holds dependencies for the agent web server.
type AgentDeps struct {
	Auth          *auth.Service
	SettingsStore SettingsStore
	Log           *slog.Logger
	Version       string
}

// AgentServer is a minimal persistent web server for agent mode.
type AgentServer struct {
	deps   AgentDeps
	mux    *http.ServeMux
	tmpl   *template.Template
	server *http.Server
	mu     sync.RWMutex
	status AgentStatusProvider
}

// NewAgentServer creates an AgentServer ready to serve.
func NewAgentServer(deps AgentDeps) *AgentServer {
	as := &AgentServer{
		deps: deps,
		mux:  http.NewServeMux(),
	}
	as.tmpl = template.Must(template.New("").ParseFS(staticFS, "static/agent.html", "static/login.html"))
	as.registerRoutes()
	return as
}

// SetStatusProvider wires in the live status source once the agent is running.
func (as *AgentServer) SetStatusProvider(p AgentStatusProvider) {
	as.mu.Lock()
	as.status = p
	as.mu.Unlock()
}

func (as *AgentServer) registerRoutes() {
	// Public routes.
	as.mux.HandleFunc("GET /login", as.handleLogin)
	as.mux.HandleFunc("POST /login", as.handleLoginPost)
	as.mux.HandleFunc("GET /static/", as.serveStatic)
	as.mux.HandleFunc("GET /favicon.svg", as.serveFaviconSVG)
	as.mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Auth-protected routes.
	as.mux.HandleFunc("GET /{$}", as.authed(as.handleIndex))
	as.mux.HandleFunc("GET /api/agent/status", as.authed(as.apiStatus))
	as.mux.HandleFunc("POST /api/agent/settings", as.authed(as.apiSaveSettings))
	as.mux.HandleFunc("POST /api/auth/change-password", as.authed(as.apiChangePassword))
	as.mux.HandleFunc("POST /logout", as.authed(as.handleLogout))

	// Catch-all.
	as.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		token := auth.GetSessionToken(r)
		if token != "" {
			if rc := as.deps.Auth.ValidateSession(r.Context(), token); rc != nil {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

// authed wraps a handler with session validation.
func (as *AgentServer) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := auth.GetSessionToken(r)
		if token == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		rc := as.deps.Auth.ValidateSession(r.Context(), token)
		if rc == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), auth.ContextKey, rc)
		h(w, r.WithContext(ctx))
	}
}

func (as *AgentServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	// If already logged in, redirect to dashboard.
	if token := auth.GetSessionToken(r); token != "" {
		if rc := as.deps.Auth.ValidateSession(r.Context(), token); rc != nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	_ = as.tmpl.ExecuteTemplate(w, "login.html", map[string]any{
		"Error":           r.URL.Query().Get("error"),
		"WebAuthnEnabled": false,
	})
}

func (as *AgentServer) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	var username, password string
	ct := r.Header.Get("Content-Type")
	isJSON := strings.Contains(ct, "application/json")
	if isJSON {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAgentError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		username = body.Username
		password = body.Password
	} else {
		_ = r.ParseForm()
		username = r.FormValue("username")
		password = r.FormValue("password")
	}

	loginErr := func(msg string) {
		if isJSON {
			writeAgentError(w, http.StatusUnauthorized, msg)
		} else {
			http.Redirect(w, r, "/login?error="+msg, http.StatusSeeOther)
		}
	}

	user, err := as.deps.Auth.Users.GetUserByUsername(username)
	if err != nil || !auth.CheckPassword(user.PasswordHash, password) {
		loginErr("Invalid username or password")
		return
	}

	token, err := auth.GenerateSessionToken()
	if err != nil {
		loginErr("Internal error")
		return
	}
	session := auth.Session{
		Token:     token,
		UserID:    user.ID,
		IP:        agentClientIP(r),
		UserAgent: r.UserAgent(),
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(as.deps.Auth.SessionExpiry),
	}
	if err := as.deps.Auth.Sessions.CreateSession(session); err != nil {
		loginErr("Internal error")
		return
	}
	auth.SetSessionCookie(w, token, session.ExpiresAt, as.deps.Auth.CookieSecure)

	if isJSON {
		writeAgentJSON(w, http.StatusOK, map[string]string{"redirect": "/"})
	} else {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func (as *AgentServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	serverAddr, _ := as.deps.SettingsStore.LoadSetting("server_addr")
	hostName, _ := as.deps.SettingsStore.LoadSetting("host_name")
	_ = as.tmpl.ExecuteTemplate(w, "agent.html", map[string]any{
		"Version":    as.deps.Version,
		"ServerAddr": serverAddr,
		"HostName":   hostName,
	})
}

func (as *AgentServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := auth.GetSessionToken(r); token != "" {
		_ = as.deps.Auth.Sessions.DeleteSession(token)
	}
	auth.ClearSessionCookie(w, as.deps.Auth.CookieSecure)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (as *AgentServer) apiStatus(w http.ResponseWriter, r *http.Request) {
	serverAddr, _ := as.deps.SettingsStore.LoadSetting("server_addr")
	hostName, _ := as.deps.SettingsStore.LoadSetting("host_name")

	as.mu.RLock()
	sp := as.status
	as.mu.RUnlock()

	connected := false
	containers := 0
	if sp != nil {
		connected = sp.Connected()
		containers = sp.ContainerCount()
	}

	writeAgentJSON(w, http.StatusOK, map[string]any{
		"connected":   connected,
		"server_addr": serverAddr,
		"host_name":   hostName,
		"containers":  containers,
		"version":     as.deps.Version,
	})
}

func (as *AgentServer) apiSaveSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServerAddr   string `json:"server_addr"`
		HostName     string `json:"host_name"`
		InstanceRole string `json:"instance_role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAgentError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.ServerAddr != "" {
		if err := as.deps.SettingsStore.SaveSetting("server_addr", body.ServerAddr); err != nil {
			writeAgentError(w, http.StatusInternalServerError, "failed to save server_addr")
			return
		}
	}
	if body.HostName != "" {
		if err := as.deps.SettingsStore.SaveSetting("host_name", body.HostName); err != nil {
			writeAgentError(w, http.StatusInternalServerError, "failed to save host_name")
			return
		}
	}
	if body.InstanceRole == "server" {
		if err := as.deps.SettingsStore.SaveSetting("instance_role", "server"); err != nil {
			writeAgentError(w, http.StatusInternalServerError, "failed to save instance_role")
			return
		}
	}
	writeAgentJSON(w, http.StatusOK, map[string]any{"status": "ok", "restart_required": true})
}

func (as *AgentServer) apiChangePassword(w http.ResponseWriter, r *http.Request) {
	rc := auth.GetRequestContext(r.Context())
	if rc == nil || rc.User == nil {
		writeAgentError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAgentError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !auth.CheckPassword(rc.User.PasswordHash, body.CurrentPassword) {
		writeAgentError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if err := auth.ValidatePassword(body.NewPassword); err != nil {
		writeAgentError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeAgentError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}
	rc.User.PasswordHash = hash
	rc.User.UpdatedAt = time.Now().UTC()
	if err := as.deps.Auth.Users.UpdateUser(*rc.User); err != nil {
		writeAgentError(w, http.StatusInternalServerError, "failed to update password")
		return
	}
	writeAgentJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (as *AgentServer) serveStatic(w http.ResponseWriter, r *http.Request) {
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

func (as *AgentServer) serveFaviconSVG(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/favicon.svg")
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(data)
}

// ListenAndServe starts the agent HTTP server on addr.
func (as *AgentServer) ListenAndServe(addr string) error {
	as.server = &http.Server{
		Addr:         addr,
		Handler:      as.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	as.deps.Log.Info("agent web server listening", "addr", addr)
	return as.server.ListenAndServe()
}

// Shutdown gracefully stops the agent web server.
func (as *AgentServer) Shutdown(ctx context.Context) error {
	if as.server == nil {
		return nil
	}
	return as.server.Shutdown(ctx)
}

func writeAgentJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAgentError(w http.ResponseWriter, status int, msg string) {
	writeAgentJSON(w, status, map[string]string{"error": msg})
}

func agentClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
