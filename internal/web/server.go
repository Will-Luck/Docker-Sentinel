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

	"github.com/GiteaLN/Docker-Sentinel/internal/events"
)

//go:embed static/*
var staticFS embed.FS

// Dependencies defines what the web server needs from the rest of the application.
type Dependencies struct {
	Store     HistoryStore
	Queue     UpdateQueue
	Docker    ContainerLister
	Updater   ContainerUpdater
	Config    ConfigReader
	EventBus  *events.Bus
	Snapshots SnapshotStore
	Rollback  ContainerRollback
	Registry  RegistryVersionChecker
	Policy    PolicyChanger
	Log       *slog.Logger
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

// PolicyChanger applies a new update policy to a container.
type PolicyChanger interface {
	ChangePolicy(ctx context.Context, name, newPolicy string) error
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
}

// ContainerLister lists running containers.
type ContainerLister interface {
	ListContainers(ctx context.Context) ([]ContainerSummary, error)
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

	// HTML pages.
	s.mux.HandleFunc("GET /{$}", s.handleDashboard)
	s.mux.HandleFunc("GET /queue", s.handleQueue)
	s.mux.HandleFunc("GET /history", s.handleHistory)

	// SSE event stream.
	s.mux.HandleFunc("GET /api/events", s.apiSSE)

	// JSON API.
	s.mux.HandleFunc("GET /api/containers", s.apiContainers)
	s.mux.HandleFunc("GET /api/containers/{name}", s.apiContainerDetail)
	s.mux.HandleFunc("GET /api/containers/{name}/versions", s.apiContainerVersions)
	s.mux.HandleFunc("POST /api/containers/{name}/rollback", s.apiRollback)
	s.mux.HandleFunc("POST /api/containers/{name}/policy", s.apiChangePolicy)
	s.mux.HandleFunc("GET /api/history", s.apiHistory)
	s.mux.HandleFunc("GET /api/queue", s.apiQueue)
	s.mux.HandleFunc("POST /api/approve/{name}", s.apiApprove)
	s.mux.HandleFunc("POST /api/reject/{name}", s.apiReject)
	s.mux.HandleFunc("POST /api/update/{name}", s.apiUpdate)
	s.mux.HandleFunc("GET /api/settings", s.apiSettings)

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
