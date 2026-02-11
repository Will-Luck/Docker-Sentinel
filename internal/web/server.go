package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
)

//go:embed static/*
var staticFS embed.FS

// Dependencies defines what the web server needs from the rest of the application.
type Dependencies struct {
	Store              HistoryStore
	Queue              UpdateQueue
	Docker             ContainerLister
	Updater            ContainerUpdater
	Config             ConfigReader
	EventBus           *events.Bus
	Snapshots          SnapshotStore
	Rollback           ContainerRollback
	Restarter          ContainerRestarter
	Stopper            ContainerStopper
	Starter            ContainerStarter
	Registry           RegistryVersionChecker
	RegistryChecker    RegistryChecker
	Policy             PolicyStore
	EventLog           EventLogger
	Scheduler          SchedulerController
	SettingsStore      SettingsStore
	SelfUpdater        SelfUpdater
	NotifyConfig       NotificationConfigStore
	NotifyReconfigurer NotifierReconfigurer
	Log                *slog.Logger
}

// HistoryStore reads update history and maintenance state.
type HistoryStore interface {
	ListHistory(limit int) ([]UpdateRecord, error)
	ListHistoryByContainer(name string, limit int) ([]UpdateRecord, error)
	GetMaintenance(name string) (bool, error)
}

// SnapshotStore reads container snapshots.
type SnapshotStore interface {
	ListSnapshots(name string) ([]SnapshotEntry, error)
}

// ContainerRollback triggers a rollback to the most recent snapshot.
type ContainerRollback interface {
	RollbackContainer(ctx context.Context, name string) error
}

// RegistryVersionChecker lists available image versions from a registry.
type RegistryVersionChecker interface {
	ListVersions(ctx context.Context, imageRef string) ([]string, error)
}

// RegistryChecker performs a full registry check for a single container.
type RegistryChecker interface {
	CheckForUpdate(ctx context.Context, imageRef string) (updateAvailable bool, newerVersions []string, err error)
}

// PolicyStore reads and writes policy overrides in BoltDB.
type PolicyStore interface {
	GetPolicyOverride(name string) (string, bool)
	SetPolicyOverride(name, policy string) error
	DeletePolicyOverride(name string) error
	AllPolicyOverrides() map[string]string
}

// EventLogger writes and reads activity log entries.
type EventLogger interface {
	AppendLog(entry LogEntry) error
	ListLogs(limit int) ([]LogEntry, error)
}

// SelfUpdater triggers self-update via an ephemeral helper container.
type SelfUpdater interface {
	Update(ctx context.Context) error
}

// NotificationConfigStore persists notification channel configuration.
type NotificationConfigStore interface {
	GetNotificationChannels() ([]notify.Channel, error)
	SetNotificationChannels(channels []notify.Channel) error
}

// NotifierReconfigurer allows runtime reconfiguration of the notification chain.
type NotifierReconfigurer interface {
	Reconfigure(notifiers ...notify.Notifier)
}

// SchedulerController controls the scheduler's poll interval.
type SchedulerController interface {
	SetPollInterval(d time.Duration)
}

// SettingsStore reads and writes settings in BoltDB.
type SettingsStore interface {
	SaveSetting(key, value string) error
	LoadSetting(key string) (string, error)
}

// LogEntry mirrors store.LogEntry.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Container string    `json:"container,omitempty"`
}

// UpdateRecord mirrors store.UpdateRecord to avoid importing store.
type UpdateRecord struct {
	Timestamp     time.Time     `json:"timestamp"`
	ContainerName string        `json:"container_name"`
	OldImage      string        `json:"old_image"`
	OldDigest     string        `json:"old_digest"`
	NewImage      string        `json:"new_image"`
	NewDigest     string        `json:"new_digest"`
	Outcome       string        `json:"outcome"`
	Duration      time.Duration `json:"duration"`
	Error         string        `json:"error,omitempty"`
}

// SnapshotEntry represents a snapshot with a parsed image reference for display.
type SnapshotEntry struct {
	Timestamp time.Time `json:"timestamp"`
	ImageRef  string    `json:"image_ref"`
}

// UpdateQueue manages pending manual approvals.
type UpdateQueue interface {
	List() []PendingUpdate
	Get(name string) (PendingUpdate, bool)     // Returns a pending update without removing it.
	Add(update PendingUpdate)                  // Adds or replaces a pending update.
	Approve(name string) (PendingUpdate, bool) // Returns the update and removes it from the queue.
	Remove(name string)
}

// PendingUpdate mirrors engine.PendingUpdate.
type PendingUpdate struct {
	ContainerID   string    `json:"container_id"`
	ContainerName string    `json:"container_name"`
	CurrentImage  string    `json:"current_image"`
	CurrentDigest string    `json:"current_digest"`
	RemoteDigest  string    `json:"remote_digest"`
	DetectedAt    time.Time `json:"detected_at"`
	NewerVersions []string  `json:"newer_versions,omitempty"`
}

// ContainerLister lists containers.
type ContainerLister interface {
	ListContainers(ctx context.Context) ([]ContainerSummary, error)
	ListAllContainers(ctx context.Context) ([]ContainerSummary, error)
	InspectContainer(ctx context.Context, id string) (ContainerInspect, error)
}

// ContainerSummary is a minimal container info struct.
type ContainerSummary struct {
	ID     string
	Names  []string
	Image  string
	Labels map[string]string
	State  string
}

// ContainerInspect has just what the dashboard needs.
type ContainerInspect struct {
	ID    string
	Name  string
	Image string
	State struct {
		Status     string
		Running    bool
		Restarting bool
	}
}

// ContainerUpdater triggers container updates.
type ContainerUpdater interface {
	UpdateContainer(ctx context.Context, id, name string) error
}

// ContainerRestarter restarts a container by ID.
type ContainerRestarter interface {
	RestartContainer(ctx context.Context, id string) error
}

// ContainerStopper stops a container by ID.
type ContainerStopper interface {
	StopContainer(ctx context.Context, id string) error
}

// ContainerStarter starts a container by ID.
type ContainerStarter interface {
	StartContainer(ctx context.Context, id string) error
}

// ConfigReader provides settings for display.
type ConfigReader interface {
	Values() map[string]string
}

// Server is the web dashboard HTTP server.
type Server struct {
	deps   Dependencies
	mux    *http.ServeMux
	tmpl   *template.Template
	server *http.Server
}

// NewServer creates a Server with all routes registered.
func NewServer(deps Dependencies) *Server {
	s := &Server{
		deps: deps,
		mux:  http.NewServeMux(),
	}

	s.parseTemplates()
	s.registerRoutes()
	return s
}

// ListenAndServe starts the HTTP server on the given address.
func (s *Server) ListenAndServe(addr string) error {
	s.server = &http.Server{
		Addr:         addr,
		Handler:      s.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE connections are long-lived; per-handler timeouts used instead.
		IdleTimeout:  120 * time.Second,
	}
	s.deps.Log.Info("web dashboard listening", "addr", addr)
	return s.server.ListenAndServe()
}

// Shutdown gracefully shuts down the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *Server) parseTemplates() {
	funcMap := template.FuncMap{
		"fmtDuration":  formatDuration,
		"fmtTime":      formatTime,
		"fmtTimeAgo":   formatTimeAgo,
		"truncDigest":  truncateDigest,
		"json":         marshalJSON,
		"changelogURL": ChangelogURL,
	}

	s.tmpl = template.Must(template.New("").Funcs(funcMap).ParseFS(staticFS, "static/*.html"))
}

func (s *Server) registerRoutes() {
	// Static assets.
	s.mux.HandleFunc("GET /static/style.css", s.serveCSS)
	s.mux.HandleFunc("GET /static/app.js", s.serveJS)
	s.mux.HandleFunc("GET /favicon.svg", s.serveFavicon)
	s.mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// HTML pages.
	s.mux.HandleFunc("GET /{$}", s.handleDashboard)
	s.mux.HandleFunc("GET /queue", s.handleQueue)
	s.mux.HandleFunc("GET /history", s.handleHistory)
	s.mux.HandleFunc("GET /settings", s.handleSettings)
	s.mux.HandleFunc("GET /logs", s.handleLogs)

	// SSE event stream.
	s.mux.HandleFunc("GET /api/events", s.apiSSE)

	// JSON API.
	s.mux.HandleFunc("GET /api/containers", s.apiContainers)
	s.mux.HandleFunc("GET /api/containers/{name}", s.apiContainerDetail)
	s.mux.HandleFunc("GET /api/containers/{name}/versions", s.apiContainerVersions)
	s.mux.HandleFunc("POST /api/containers/{name}/rollback", s.apiRollback)
	s.mux.HandleFunc("POST /api/containers/{name}/policy", s.apiChangePolicy)
	s.mux.HandleFunc("DELETE /api/containers/{name}/policy", s.apiDeletePolicy)
	s.mux.HandleFunc("POST /api/bulk/policy", s.apiBulkPolicy)
	s.mux.HandleFunc("GET /api/history", s.apiHistory)
	s.mux.HandleFunc("GET /api/queue", s.apiQueue)
	s.mux.HandleFunc("POST /api/approve/{name}", s.apiApprove)
	s.mux.HandleFunc("POST /api/reject/{name}", s.apiReject)
	s.mux.HandleFunc("POST /api/update/{name}", s.apiUpdate)
	s.mux.HandleFunc("POST /api/check/{name}", s.apiCheck)
	s.mux.HandleFunc("POST /api/containers/{name}/restart", s.apiRestart)
	s.mux.HandleFunc("GET /api/settings", s.apiSettings)
	s.mux.HandleFunc("POST /api/settings/poll-interval", s.apiSetPollInterval)
	s.mux.HandleFunc("GET /api/logs", s.apiLogs)
	s.mux.HandleFunc("POST /api/self-update", s.apiSelfUpdate)
	s.mux.HandleFunc("POST /api/containers/{name}/stop", s.apiStop)
	s.mux.HandleFunc("POST /api/containers/{name}/start", s.apiStart)
	s.mux.HandleFunc("GET /api/settings/notifications", s.apiGetNotifications)
	s.mux.HandleFunc("PUT /api/settings/notifications", s.apiSaveNotifications)
	s.mux.HandleFunc("POST /api/settings/notifications/test", s.apiTestNotification)

	// Per-container HTML partial (for live row updates).
	s.mux.HandleFunc("GET /api/containers/{name}/row", s.handleContainerRow)

	// Per-container HTML page.
	s.mux.HandleFunc("GET /container/{name}", s.handleContainerDetail)
}

func (s *Server) serveCSS(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) serveJS(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) serveFavicon(w http.ResponseWriter, r *http.Request) {
	const favicon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><circle cx="16" cy="16" r="16" fill="#3b82f6"/><text x="16" y="22" text-anchor="middle" fill="#fff" font-family="system-ui,sans-serif" font-weight="700" font-size="20">S</text></svg>`
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte(favicon))
}

// writeJSON encodes v as JSON and writes it to the response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// Template helper functions.

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(time.Second).String()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func formatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}

func truncateDigest(digest string) string {
	if len(digest) > 19 {
		return digest[:19] + "..."
	}
	return digest
}

func marshalJSON(v any) template.JS {
	data, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return template.JS(data) //nolint:gosec // data is server-controlled JSON, not user input
}
