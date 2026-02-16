package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
)

//go:embed static/*
var staticFS embed.FS

// Dependencies defines what the web server needs from the rest of the application.
type Dependencies struct {
	Store               HistoryStore
	Queue               UpdateQueue
	Docker              ContainerLister
	Updater             ContainerUpdater
	Config              ConfigReader
	ConfigWriter        ConfigWriter
	EventBus            *events.Bus
	Snapshots           SnapshotStore
	Rollback            ContainerRollback
	Restarter           ContainerRestarter
	Stopper             ContainerStopper
	Starter             ContainerStarter
	Registry            RegistryVersionChecker
	RegistryChecker     RegistryChecker
	Policy              PolicyStore
	EventLog            EventLogger
	Scheduler           SchedulerController
	SettingsStore       SettingsStore
	SelfUpdater         SelfUpdater
	NotifyConfig        NotificationConfigStore
	NotifyReconfigurer  NotifierReconfigurer
	NotifyState         NotifyStateStore
	Digest              DigestController
	IgnoredVersions     IgnoredVersionStore
	RegistryCredentials RegistryCredentialStore
	RateTracker         RateLimitProvider
	GHCRCache           GHCRAlternativeProvider
	AboutStore          AboutStore
	HookStore           HookStore
	Swarm               SwarmProvider // nil when not in Swarm mode
	MetricsEnabled      bool
	Auth                *auth.Service
	Version             string
	Log                 *slog.Logger
}

// HistoryStore reads/writes update history and maintenance state.
type HistoryStore interface {
	ListHistory(limit int) ([]UpdateRecord, error)
	ListHistoryByContainer(name string, limit int) ([]UpdateRecord, error)
	GetMaintenance(name string) (bool, error)
	RecordUpdate(rec UpdateRecord) error
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
	CheckForUpdate(ctx context.Context, imageRef string) (updateAvailable bool, newerVersions []string, resolvedCurrent string, resolvedTarget string, err error)
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

// NotifyStateStore reads and writes per-container notification state and preferences.
type NotifyStateStore interface {
	GetNotifyPref(name string) (*NotifyPref, error)
	SetNotifyPref(name string, pref *NotifyPref) error
	DeleteNotifyPref(name string) error
	AllNotifyPrefs() (map[string]*NotifyPref, error)
	AllNotifyStates() (map[string]*NotifyState, error)
	ClearNotifyState(name string) error
}

// IgnoredVersionStore reads and writes per-container ignored versions.
type IgnoredVersionStore interface {
	AddIgnoredVersion(containerName, version string) error
	GetIgnoredVersions(containerName string) ([]string, error)
	ClearIgnoredVersions(containerName string) error
}

// RegistryCredentialStore persists registry credentials.
type RegistryCredentialStore interface {
	GetRegistryCredentials() ([]RegistryCredential, error)
	SetRegistryCredentials(creds []RegistryCredential) error
}

// RegistryCredential mirrors registry.RegistryCredential for the web layer.
type RegistryCredential struct {
	ID       string `json:"id"`
	Registry string `json:"registry"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

// RateLimitProvider returns rate limit status for display.
type RateLimitProvider interface {
	Status() []RateLimitStatus
	OverallHealth() string
	// ProbeAndRecord makes a lightweight request to discover a registry's
	// rate limits and records the result. Used after credential changes.
	ProbeAndRecord(ctx context.Context, host string, cred RegistryCredential) error
}

// RateLimitStatus mirrors registry.RegistryStatus for the web layer.
type RateLimitStatus struct {
	Registry       string    `json:"registry"`
	Limit          int       `json:"limit"`
	Remaining      int       `json:"remaining"`
	ResetAt        time.Time `json:"reset_at"`
	IsAuth         bool      `json:"is_auth"`
	HasLimits      bool      `json:"has_limits"`
	ContainerCount int       `json:"container_count"`
	LastUpdated    time.Time `json:"last_updated"`
}

// GHCRAlternativeProvider returns GHCR alternative detection results.
type GHCRAlternativeProvider interface {
	Get(repo, tag string) (*GHCRAlternative, bool)
	All() []GHCRAlternative
}

// GHCRAlternative mirrors registry.GHCRAlternative for the web layer.
type GHCRAlternative struct {
	DockerHubImage string    `json:"docker_hub_image"`
	GHCRImage      string    `json:"ghcr_image"`
	Tag            string    `json:"tag"`
	Available      bool      `json:"available"`
	DigestMatch    bool      `json:"digest_match"`
	HubDigest      string    `json:"hub_digest,omitempty"`
	GHCRDigest     string    `json:"ghcr_digest,omitempty"`
	CheckedAt      time.Time `json:"checked_at"`
}

// AboutStore provides aggregate counts for the About page.
type AboutStore interface {
	CountHistory() (int, error)
	CountSnapshots() (int, error)
}

// DigestController controls the digest scheduler.
type DigestController interface {
	SetDigestConfig()
	TriggerDigest(ctx context.Context)
	LastRunTime() time.Time
}

// SchedulerController controls the scheduler's poll interval and scan triggers.
type SchedulerController interface {
	SetPollInterval(d time.Duration)
	TriggerScan(ctx context.Context)
	LastScanTime() time.Time
	SetSchedule(sched string)
}

// SwarmProvider provides Swarm service operations for the dashboard.
// Nil when the daemon is not a Swarm manager.
type SwarmProvider interface {
	IsSwarmMode() bool
	ListServices(ctx context.Context) ([]ServiceSummary, error)
	ListServiceDetail(ctx context.Context) ([]ServiceDetail, error)
	UpdateService(ctx context.Context, id, name, targetImage string) error
	RollbackService(ctx context.Context, id, name string) error
	ScaleService(ctx context.Context, name string, replicas uint64) error
}

// ServiceSummary is a minimal Swarm service info struct for the web layer.
type ServiceSummary struct {
	ID              string
	Name            string
	Image           string
	Labels          map[string]string
	Replicas        string // e.g. "3/3"
	DesiredReplicas uint64
	RunningReplicas uint64
}

// TaskInfo describes a single Swarm task (one replica on one node).
type TaskInfo struct {
	NodeID   string
	NodeName string
	NodeAddr string
	State    string
	Image    string
	Tag      string
	Slot     int
	Error    string
}

// ServiceDetail is a ServiceSummary enriched with per-node task info.
type ServiceDetail struct {
	ServiceSummary
	Tasks        []TaskInfo
	UpdateStatus string
}

// HookStore reads and writes lifecycle hook configurations.
type HookStore interface {
	ListHooks(containerName string) ([]HookEntry, error)
	SaveHook(hook HookEntry) error
	DeleteHook(containerName, phase string) error
}

// HookEntry mirrors hooks.Hook for the web layer.
type HookEntry struct {
	ContainerName string   `json:"container_name"`
	Phase         string   `json:"phase"`
	Command       []string `json:"command"`
	Timeout       int      `json:"timeout"`
}

// SettingsStore reads and writes settings in BoltDB.
type SettingsStore interface {
	SaveSetting(key, value string) error
	LoadSetting(key string) (string, error)
	GetAllSettings() (map[string]string, error)
}

// LogEntry mirrors store.LogEntry.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Container string    `json:"container,omitempty"`
	User      string    `json:"user,omitempty"`
	Kind      string    `json:"kind,omitempty"` // "service" or "" (default = container)
}

// NotifyPref mirrors store.NotifyPref.
type NotifyPref struct {
	Mode string `json:"mode"`
}

// NotifyState mirrors store.NotifyState.
type NotifyState struct {
	LastDigest   string    `json:"last_digest"`
	LastNotified time.Time `json:"last_notified"`
	FirstSeen    time.Time `json:"first_seen"`
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
	Type          string        `json:"type,omitempty"` // "container" (default) or "service"
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
	ContainerID            string    `json:"container_id"`
	ContainerName          string    `json:"container_name"`
	CurrentImage           string    `json:"current_image"`
	CurrentDigest          string    `json:"current_digest"`
	RemoteDigest           string    `json:"remote_digest"`
	DetectedAt             time.Time `json:"detected_at"`
	NewerVersions          []string  `json:"newer_versions,omitempty"`
	ResolvedCurrentVersion string    `json:"resolved_current_version,omitempty"`
	ResolvedTargetVersion  string    `json:"resolved_target_version,omitempty"`
	Type                   string    `json:"type,omitempty"` // "container" (default) or "service"
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
	UpdateContainer(ctx context.Context, id, name, targetImage string) error
	IsUpdating(name string) bool
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

// ConfigWriter updates mutable runtime settings in memory.
type ConfigWriter interface {
	SetDefaultPolicy(s string)
	SetGracePeriod(d time.Duration)
	SetLatestAutoUpdate(b bool)
	SetImageCleanup(b bool)
	SetHooksEnabled(b bool)
	SetHooksWriteLabels(b bool)
	SetDependencyAware(b bool)
	SetRollbackPolicy(s string)
}

// Server is the web dashboard HTTP server.
type Server struct {
	deps          Dependencies
	mux           *http.ServeMux
	tmpl          *template.Template
	server        *http.Server
	startTime     time.Time          // when the server was created
	setupDeadline time.Time          // setup page closes after this; zero = no window
	webauthn      *webauthn.WebAuthn // nil when WebAuthn is not configured
	tlsCert       string             // path to TLS certificate PEM (empty = plain HTTP)
	tlsKey        string             // path to TLS private key PEM
}

// SetSetupDeadline sets the time limit for first-run setup.
// After this deadline, the setup page will reject new account creation
// until the container is restarted.
func (s *Server) SetSetupDeadline(d time.Time) {
	s.setupDeadline = d
}

// setupWindowOpen returns true if the setup time window is still active.
func (s *Server) setupWindowOpen() bool {
	if s.setupDeadline.IsZero() {
		return false
	}
	return time.Now().Before(s.setupDeadline)
}

// SetTLS configures TLS certificate and key paths for HTTPS serving.
func (s *Server) SetTLS(cert, key string) {
	s.tlsCert = cert
	s.tlsKey = key
}

// SetWebAuthn configures WebAuthn support. When wa is nil, passkey routes return 404.
func (s *Server) SetWebAuthn(wa *webauthn.WebAuthn) {
	s.webauthn = wa
}

// NewServer creates a Server with all routes registered.
func NewServer(deps Dependencies) *Server {
	s := &Server{
		deps:      deps,
		mux:       http.NewServeMux(),
		startTime: time.Now(),
	}

	s.parseTemplates()
	s.registerRoutes()
	return s
}

// ListenAndServe starts the HTTP server on the given address.
func (s *Server) ListenAndServe(addr string) error {
	handler := http.Handler(s.mux)
	// Wrap with setup redirect when auth is configured.
	if s.deps.Auth != nil {
		handler = s.setupRedirectHandler(s.mux)
	}
	s.server = &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE connections are long-lived; per-handler timeouts used instead.
		IdleTimeout:  120 * time.Second,
	}
	if s.tlsCert != "" {
		s.deps.Log.Info("web dashboard listening (TLS)", "addr", addr)
		return s.server.ListenAndServeTLS(s.tlsCert, s.tlsKey)
	}
	s.deps.Log.Info("web dashboard listening", "addr", addr)
	return s.server.ListenAndServe()
}

// setupRedirectHandler redirects all non-setup requests to /setup when first-run is needed.
func (s *Server) setupRedirectHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Auth.NeedsSetup() {
			p := r.URL.Path
			if p != "/setup" && !strings.HasPrefix(p, "/static/") &&
				p != "/favicon.svg" && p != "/favicon.ico" {
				http.Redirect(w, r, "/setup", http.StatusSeeOther)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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
		"fmtTimeUntil": formatTimeUntil,
		"truncDigest":  truncateDigest,
		"json":         marshalJSON,
		"changelogURL": ChangelogURL,
		"versionURL":   VersionURL,
		"imageTag":     ImageTag,
		"serviceOrContainer": func(kind, name string) string {
			if kind == "service" {
				return "/service/" + name
			}
			return "/container/" + name
		},
	}

	s.tmpl = template.Must(template.New("").Funcs(funcMap).ParseFS(staticFS, "static/*.html"))
}

func (s *Server) registerRoutes() {
	// Middleware helpers — wraps handlers with auth + CSRF + permission check.
	authMw := auth.AuthMiddleware(s.deps.Auth)
	csrfMw := auth.CSRFMiddleware

	perm := func(p auth.Permission, h http.HandlerFunc) http.Handler {
		return authMw(csrfMw(auth.RequirePermission(p)(h)))
	}
	authed := func(h http.HandlerFunc) http.Handler {
		return authMw(csrfMw(h))
	}

	// --- Public routes (no auth required) ---
	if s.deps.MetricsEnabled {
		s.mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
			promhttp.Handler().ServeHTTP(w, r)
		})
	}
	s.mux.HandleFunc("GET /static/style.css", s.serveCSS)
	s.mux.HandleFunc("GET /static/app.js", s.serveJS)
	s.mux.HandleFunc("GET /static/auth.js", s.serveAuthJS)
	s.mux.HandleFunc("GET /static/webauthn.js", s.serveWebAuthnJS)
	s.mux.HandleFunc("GET /static/", s.serveStaticFile)
	s.mux.HandleFunc("GET /favicon.svg", s.serveFavicon)
	s.mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	s.mux.HandleFunc("GET /login", s.handleLogin)
	s.mux.HandleFunc("POST /login", s.apiLogin)
	s.mux.HandleFunc("GET /setup", s.handleSetup)
	s.mux.HandleFunc("POST /setup", s.apiSetup)
	s.mux.HandleFunc("POST /logout", s.handleLogout)
	s.mux.HandleFunc("GET /logout", s.handleLogout)
	s.mux.HandleFunc("POST /api/auth/passkeys/login/begin", s.apiPasskeyLoginBegin)
	s.mux.HandleFunc("POST /api/auth/passkeys/login/finish", s.apiPasskeyLoginFinish)
	s.mux.HandleFunc("GET /api/auth/passkeys/available", s.apiPasskeysAvailable)

	// --- Auth-only routes (authenticated, no specific permission) ---
	s.mux.Handle("GET /account", authed(s.handleAccount))
	s.mux.Handle("POST /api/auth/change-password", authed(s.apiChangePassword))
	s.mux.Handle("GET /api/auth/sessions", authed(s.apiListSessions))
	s.mux.Handle("DELETE /api/auth/sessions/{token}", authed(s.apiRevokeSession))
	s.mux.Handle("DELETE /api/auth/sessions", authed(s.apiRevokeAllSessions))
	s.mux.Handle("POST /api/auth/tokens", authed(s.apiCreateToken))
	s.mux.Handle("DELETE /api/auth/tokens/{id}", authed(s.apiDeleteToken))
	s.mux.Handle("GET /api/auth/me", authed(s.apiGetMe))
	s.mux.Handle("POST /api/auth/passkeys/register/begin", authed(s.apiPasskeyRegisterBegin))
	s.mux.Handle("POST /api/auth/passkeys/register/finish", authed(s.apiPasskeyRegisterFinish))
	s.mux.Handle("GET /api/auth/passkeys", authed(s.apiListPasskeys))
	s.mux.Handle("DELETE /api/auth/passkeys/{id}", authed(s.apiDeletePasskey))

	// --- Permission-gated routes ---

	// containers.view
	s.mux.Handle("GET /{$}", perm(auth.PermContainersView, s.handleDashboard))
	s.mux.Handle("GET /queue", perm(auth.PermContainersView, s.handleQueue))
	s.mux.Handle("GET /container/{name}", perm(auth.PermContainersView, s.handleContainerDetail))
	s.mux.Handle("GET /api/containers", perm(auth.PermContainersView, s.apiContainers))
	s.mux.Handle("GET /api/containers/{name}", perm(auth.PermContainersView, s.apiContainerDetail))
	s.mux.Handle("GET /api/containers/{name}/versions", perm(auth.PermContainersView, s.apiContainerVersions))
	s.mux.Handle("GET /api/containers/{name}/row", perm(auth.PermContainersView, s.handleContainerRow))
	s.mux.Handle("GET /api/events", perm(auth.PermContainersView, s.apiSSE))
	s.mux.Handle("GET /api/queue", perm(auth.PermContainersView, s.apiQueue))
	s.mux.Handle("GET /api/last-scan", perm(auth.PermContainersView, s.apiLastScan))

	// containers.update
	s.mux.Handle("POST /api/update/{name}", perm(auth.PermContainersUpdate, s.apiUpdate))
	s.mux.Handle("POST /api/check/{name}", perm(auth.PermContainersUpdate, s.apiCheck))
	s.mux.Handle("POST /api/scan", perm(auth.PermContainersUpdate, s.apiTriggerScan))
	s.mux.Handle("POST /api/containers/{name}/switch-ghcr", perm(auth.PermContainersUpdate, s.apiSwitchToGHCR))

	// containers.approve
	s.mux.Handle("POST /api/approve/{name}", perm(auth.PermContainersApprove, s.apiApprove))
	s.mux.Handle("POST /api/ignore/{name}", perm(auth.PermContainersApprove, s.apiIgnoreVersion))
	s.mux.Handle("POST /api/reject/{name}", perm(auth.PermContainersApprove, s.apiReject))

	// containers.rollback
	s.mux.Handle("POST /api/containers/{name}/rollback", perm(auth.PermContainersRollback, s.apiRollback))

	// Swarm service operations (reuse container permissions).
	s.mux.Handle("GET /api/services", perm(auth.PermContainersView, s.apiServicesList))
	s.mux.Handle("GET /api/services/{name}/detail", perm(auth.PermContainersView, s.apiServiceDetail))
	s.mux.Handle("POST /api/services/{name}/update", perm(auth.PermContainersUpdate, s.apiServiceUpdate))
	s.mux.Handle("POST /api/services/{name}/rollback", perm(auth.PermContainersRollback, s.apiServiceRollback))
	s.mux.Handle("POST /api/services/{name}/scale", perm(auth.PermContainersManage, s.apiServiceScale))

	s.mux.Handle("GET /service/{name}", perm(auth.PermContainersView, s.handleServiceDetail))

	// containers.manage
	s.mux.Handle("POST /api/containers/{name}/restart", perm(auth.PermContainersManage, s.apiRestart))
	s.mux.Handle("POST /api/containers/{name}/stop", perm(auth.PermContainersManage, s.apiStop))
	s.mux.Handle("POST /api/containers/{name}/start", perm(auth.PermContainersManage, s.apiStart))
	s.mux.Handle("POST /api/containers/{name}/policy", perm(auth.PermContainersManage, s.apiChangePolicy))
	s.mux.Handle("DELETE /api/containers/{name}/policy", perm(auth.PermContainersManage, s.apiDeletePolicy))
	s.mux.Handle("POST /api/bulk/policy", perm(auth.PermContainersManage, s.apiBulkPolicy))

	// settings.view
	s.mux.Handle("GET /settings", perm(auth.PermSettingsView, s.handleSettings))
	s.mux.Handle("GET /api/settings", perm(auth.PermSettingsView, s.apiSettings))
	s.mux.Handle("GET /api/settings/notifications", perm(auth.PermSettingsView, s.apiGetNotifications))
	s.mux.Handle("GET /api/settings/notifications/event-types", perm(auth.PermSettingsView, s.apiNotificationEventTypes))
	s.mux.Handle("GET /api/settings/registries", perm(auth.PermSettingsView, s.apiGetRegistryCredentials))
	s.mux.Handle("GET /api/ratelimits", perm(auth.PermContainersView, s.apiGetRateLimits))
	s.mux.Handle("GET /api/about", perm(auth.PermSettingsView, s.apiAbout))
	s.mux.Handle("GET /api/ghcr/alternatives", perm(auth.PermContainersView, s.apiGetGHCRAlternatives))
	s.mux.Handle("GET /api/containers/{name}/ghcr", perm(auth.PermContainersView, s.apiGetContainerGHCR))
	s.mux.Handle("GET /api/hooks/{container}", perm(auth.PermSettingsView, s.apiGetHooks))
	s.mux.Handle("GET /api/deps", perm(auth.PermContainersView, s.apiGetDeps))
	s.mux.Handle("GET /api/deps/{container}", perm(auth.PermContainersView, s.apiGetContainerDeps))

	// Notification prefs & digest (read)
	s.mux.Handle("GET /api/containers/{name}/notify-pref", perm(auth.PermSettingsView, s.apiGetNotifyPref))
	s.mux.Handle("GET /api/settings/digest", perm(auth.PermSettingsView, s.apiGetDigestSettings))
	s.mux.Handle("GET /api/settings/container-notify-prefs", perm(auth.PermSettingsView, s.apiGetAllNotifyPrefs))
	s.mux.Handle("GET /api/digest/banner", perm(auth.PermContainersView, s.apiGetDigestBanner))

	// settings.modify
	s.mux.Handle("POST /api/settings/poll-interval", perm(auth.PermSettingsModify, s.apiSetPollInterval))
	s.mux.Handle("POST /api/settings/default-policy", perm(auth.PermSettingsModify, s.apiSetDefaultPolicy))
	s.mux.Handle("POST /api/settings/grace-period", perm(auth.PermSettingsModify, s.apiSetGracePeriod))
	s.mux.Handle("POST /api/settings/pause", perm(auth.PermSettingsModify, s.apiSetPause))
	s.mux.Handle("POST /api/settings/latest-auto-update", perm(auth.PermSettingsModify, s.apiSetLatestAutoUpdate))
	s.mux.Handle("POST /api/settings/filters", perm(auth.PermSettingsModify, s.apiSetFilters))
	s.mux.Handle("POST /api/settings/stack-order", perm(auth.PermSettingsModify, s.apiSaveStackOrder))
	s.mux.Handle("PUT /api/settings/notifications", perm(auth.PermSettingsModify, s.apiSaveNotifications))
	s.mux.Handle("POST /api/settings/notifications/test", perm(auth.PermSettingsModify, s.apiTestNotification))
	s.mux.Handle("PUT /api/settings/registries", perm(auth.PermSettingsModify, s.apiSaveRegistryCredentials))
	s.mux.Handle("POST /api/settings/registries/test", perm(auth.PermSettingsModify, s.apiTestRegistryCredential))
	s.mux.Handle("DELETE /api/settings/registries/{id}", perm(auth.PermSettingsModify, s.apiDeleteRegistryCredential))
	s.mux.Handle("POST /api/self-update", perm(auth.PermSettingsModify, s.apiSelfUpdate))
	s.mux.Handle("POST /api/settings/image-cleanup", perm(auth.PermSettingsModify, s.apiSetImageCleanup))
	s.mux.Handle("POST /api/settings/schedule", perm(auth.PermSettingsModify, s.apiSetSchedule))
	s.mux.Handle("POST /api/settings/hooks-enabled", perm(auth.PermSettingsModify, s.apiSetHooksEnabled))
	s.mux.Handle("POST /api/settings/hooks-write-labels", perm(auth.PermSettingsModify, s.apiSetHooksWriteLabels))
	s.mux.Handle("POST /api/settings/dependency-aware", perm(auth.PermSettingsModify, s.apiSetDependencyAware))
	s.mux.Handle("POST /api/settings/rollback-policy", perm(auth.PermSettingsModify, s.apiSetRollbackPolicy))
	s.mux.Handle("POST /api/hooks/{container}", perm(auth.PermSettingsModify, s.apiSaveHook))
	s.mux.Handle("DELETE /api/hooks/{container}/{phase}", perm(auth.PermSettingsModify, s.apiDeleteHook))

	// Notification prefs & digest (write)
	s.mux.Handle("POST /api/containers/{name}/notify-pref", perm(auth.PermSettingsModify, s.apiSetNotifyPref))
	s.mux.Handle("DELETE /api/notify-states", perm(auth.PermSettingsModify, s.apiClearAllNotifyStates))
	s.mux.Handle("POST /api/settings/digest", perm(auth.PermSettingsModify, s.apiSaveDigestSettings))
	s.mux.Handle("POST /api/digest/trigger", perm(auth.PermSettingsModify, s.apiTriggerDigest))
	s.mux.Handle("POST /api/digest/banner/dismiss", perm(auth.PermContainersView, s.apiDismissDigestBanner))

	// users.manage
	s.mux.Handle("GET /api/auth/users", perm(auth.PermUsersManage, s.apiListUsers))
	s.mux.Handle("POST /api/auth/users", perm(auth.PermUsersManage, s.apiCreateUser))
	s.mux.Handle("DELETE /api/auth/users/{id}", perm(auth.PermUsersManage, s.apiDeleteUser))
	s.mux.Handle("POST /api/auth/settings", perm(auth.PermUsersManage, s.apiAuthSettings))

	// logs.view
	s.mux.Handle("GET /logs", perm(auth.PermLogsView, s.handleLogs))
	s.mux.Handle("GET /api/logs", perm(auth.PermLogsView, s.apiLogs))

	// history.view
	s.mux.Handle("GET /history", perm(auth.PermHistoryView, s.handleHistory))
	s.mux.Handle("GET /api/history", perm(auth.PermHistoryView, s.apiHistory))
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

func (s *Server) serveAuthJS(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/auth.js")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) serveWebAuthnJS(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/webauthn.js")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) serveFavicon(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/favicon.svg")
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

func (s *Server) serveStaticFile(w http.ResponseWriter, r *http.Request) {
	path := "static" + strings.TrimPrefix(r.URL.Path, "/static")
	data, err := staticFS.ReadFile(path)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	ext := filepath.Ext(path)
	switch ext {
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	case ".ico":
		w.Header().Set("Content-Type", "image/x-icon")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
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

func formatTimeUntil(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Until(t)
	if d < 0 {
		return "expired"
	}
	switch {
	case d < time.Minute:
		return "in less than a minute"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "in 1 minute"
		}
		return fmt.Sprintf("in %d minutes", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "in 1 hour"
		}
		return fmt.Sprintf("in %d hours", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "in 1 day"
		}
		return fmt.Sprintf("in %d days", days)
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
