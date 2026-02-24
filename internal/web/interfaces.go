package web

import (
	"context"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
)

// HistoryStore reads/writes update history and maintenance state.
type HistoryStore interface {
	ListHistory(limit int, before string) ([]UpdateRecord, error)
	ListAllHistory() ([]UpdateRecord, error)
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

// RegistryTagLister lists all tags for an image from a registry.
type RegistryTagLister interface {
	ListAllTags(ctx context.Context, imageRef string) ([]string, error)
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
	Update(ctx context.Context, targetImage string) error
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

// ClusterProvider provides access to cluster host management.
// Nil when clustering is disabled.
type ClusterProvider interface {
	// AllHosts returns info about all registered agent hosts.
	AllHosts() []ClusterHost
	// GetHost returns info about a specific host.
	GetHost(id string) (ClusterHost, bool)
	// ConnectedHosts returns the IDs of currently connected agents.
	ConnectedHosts() []string
	// GenerateEnrollToken creates a new one-time enrollment token.
	// Returns the plaintext token (shown to admin once) and the token ID.
	GenerateEnrollToken() (token string, id string, err error)
	// RemoveHost removes a host from the cluster.
	RemoveHost(id string) error
	// RevokeHost revokes a host's certificate and removes it.
	RevokeHost(id string) error
	// PauseHost sets a host to paused state (no new updates).
	PauseHost(id string) error
	// UpdateRemoteContainer dispatches a container update to a remote agent.
	UpdateRemoteContainer(ctx context.Context, hostID, containerName, targetImage, targetDigest string) error
	// RemoteContainerAction dispatches a lifecycle action to a container on a remote agent.
	RemoteContainerAction(ctx context.Context, hostID, containerName, action string) error
	// AllHostContainers returns containers from all connected hosts.
	AllHostContainers() []RemoteContainer
}

// RemoteContainer represents a container on a remote host.
type RemoteContainer struct {
	Name     string            `json:"name"`
	Image    string            `json:"image"`
	State    string            `json:"state"` // "running", "exited", etc.
	HostID   string            `json:"host_id"`
	HostName string            `json:"host_name"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// ClusterHost represents a remote agent host for the web layer.
type ClusterHost struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Address       string    `json:"address"`
	State         string    `json:"state"` // "active", "paused", "decommissioned"
	Connected     bool      `json:"connected"`
	EnrolledAt    time.Time `json:"enrolled_at"`
	LastSeen      time.Time `json:"last_seen"`
	AgentVersion  string    `json:"agent_version,omitempty"`
	Containers    int       `json:"containers"` // count of known containers
	DisconnectAt  time.Time `json:"disconnect_at,omitempty"`
	DisconnectErr string    `json:"disconnect_err,omitempty"`
	DisconnectCat string    `json:"disconnect_cat,omitempty"`
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

// ReleaseSourceStore reads and writes configurable release note sources.
type ReleaseSourceStore interface {
	GetReleaseSources() ([]ReleaseSource, error)
	SetReleaseSources(sources []ReleaseSource) error
}

// ReleaseSource mirrors registry.ReleaseSource for the web layer.
type ReleaseSource struct {
	ImagePattern string `json:"image_pattern"`
	GitHubRepo   string `json:"github_repo"`
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

// ClusterLifecycle allows the settings API to start/stop the cluster
// server at runtime without restarting the container.
type ClusterLifecycle interface {
	Start() error
	Stop()
}

// PortainerProvider provides Portainer endpoint and container access for the web layer.
type PortainerProvider interface {
	TestConnection(ctx context.Context) error
	Endpoints(ctx context.Context) ([]PortainerEndpoint, error)
	AllEndpoints(ctx context.Context) ([]PortainerEndpoint, error)
	EndpointContainers(ctx context.Context, endpointID int) ([]PortainerContainerInfo, error)
}

// PortainerEndpoint represents a Portainer-managed Docker environment.
type PortainerEndpoint struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status string `json:"status"`
}

// PortainerContainerInfo is a container from a Portainer-managed environment.
type PortainerContainerInfo struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	State        string            `json:"state"`
	Labels       map[string]string `json:"labels,omitempty"`
	EndpointID   int               `json:"endpoint_id"`
	EndpointName string            `json:"endpoint_name"`
	StackID      int               `json:"stack_id,omitempty"`
	StackName    string            `json:"stack_name,omitempty"`
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
	Type          string        `json:"type,omitempty"`      // "container" (default) or "service"
	HostID        string        `json:"host_id,omitempty"`   // cluster host ID (empty = local)
	HostName      string        `json:"host_name,omitempty"` // cluster host name (empty = local)
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
	Type                   string    `json:"type,omitempty"`    // "container" (default) or "service"
	HostID                 string    `json:"host_id,omitempty"` // cluster host ID (empty = local)
	HostName               string    `json:"host_name,omitempty"`
}

// Key returns the queue map key. Remote containers use "hostID::name" to
// avoid collisions with local or other-host containers of the same name.
func (u PendingUpdate) Key() string {
	if u.HostID == "" {
		return u.ContainerName
	}
	return u.HostID + "::" + u.ContainerName
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
	SetRemoveVolumes(b bool)
	SetScanConcurrency(n int)
}
