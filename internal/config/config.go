package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config holds all Docker-Sentinel configuration from environment variables.
// Mutable fields (PollInterval, GracePeriod, DefaultPolicy) are protected by
// an RWMutex and must be accessed via getter/setter methods at runtime, since
// the engine goroutine reads them while HTTP handlers may write them.
type Config struct {
	// Docker connection
	DockerSock string

	// Storage
	DBPath string

	// Logging
	LogJSON bool

	// Notifications
	GotifyURL      string
	GotifyToken    string
	WebhookURL     string
	WebhookHeaders string // comma-separated "Key:Value" pairs

	// Web dashboard
	WebPort     string
	WebEnabled  bool
	HostAddress string // SENTINEL_HOST — Docker host IP/hostname for port links (auto-detected if empty)

	// Authentication
	AuthEnabled   *bool // nil = use DB default (true); non-nil = env override
	SessionExpiry time.Duration
	CookieSecure  bool

	// TLS
	TLSCert string // path to TLS certificate PEM file
	TLSKey  string // path to TLS private key PEM file
	TLSAuto bool   // auto-generate self-signed certificate

	// WebAuthn passkeys (all empty = disabled)
	WebAuthnRPID        string // Relying Party ID (e.g. "192.0.2.10")
	WebAuthnDisplayName string // RP display name shown by authenticators
	WebAuthnOrigins     string // comma-separated allowed origins
	MetricsEnabled      bool
	MetricsTextfile     string // SENTINEL_METRICS_TEXTFILE — path for node_exporter textfile collector

	// Cluster / multi-host
	Mode                 string        // "server" or "agent" — set via subcommand or SENTINEL_MODE env
	ClusterEnabled       bool          // server: whether to start the gRPC listener
	ClusterPort          string        // gRPC port (default "9443")
	ClusterDataDir       string        // CA/cert storage directory (default "/data/cluster")
	ClusterAdvertiseAddr string        // server: extra IPs/hostnames for TLS cert SANs (comma-separated)
	ServerAddr           string        // agent: server gRPC address (host:port)
	EnrollToken          string        // agent: one-time enrollment token
	HostName             string        // agent: human-readable label for this host
	GracePeriodOffline   time.Duration // agent: time before switching to autonomous mode (default 30m)

	// NPM (Nginx Proxy Manager) integration
	NPMURL      string
	NPMEmail    string
	NPMPassword string

	// Retention: history and activity-log records older than this many days
	// are pruned (update and rollback records are kept). 0 keeps everything.
	// SENTINEL_HISTORY_RETENTION_DAYS, default 90; a saved setting overrides it.
	HistoryRetentionDays int
	// TrustedProxies lists reverse proxies (IPs or CIDR ranges, comma
	// separated) whose X-Forwarded-For is believed when working out the
	// client address for login rate limiting. Empty trusts none.
	TrustedProxies string
	// OIDCLinkExisting lets an unlinked non-admin account be linked to a
	// single sign-on identity by username (SENTINEL_OIDC_LINK_EXISTING). A
	// value saved in Settings replaces it.
	OIDCLinkExisting bool
	// OIDCLinkAdmin names one account that the next SSO login with that
	// username links to, once (SENTINEL_OIDC_LINK_ADMIN), admin or not.
	OIDCLinkAdmin string

	// envProblems holds environment values that were set but could not be
	// parsed; the default was used instead. See EnvProblems.
	envProblems []EnvProblem

	// mu protects the mutable runtime fields below.
	mu                sync.RWMutex
	pollInterval      time.Duration // how often to scan for updates
	gracePeriod       time.Duration // wait after starting new container before health check
	defaultPolicy     string        // "auto", "manual", or "pinned"
	latestAutoUpdate  bool          // auto-update :latest containers regardless of default policy
	imageCleanup      bool
	schedule          string
	hooksEnabled      bool
	hooksWriteLabels  bool
	dependencyAware   bool
	rollbackPolicy    string // "", "manual", or "pinned"
	imageBackup       bool
	showStopped       bool
	removeVolumes     bool
	scanConcurrency   int
	maintenanceWindow string // time-range expression for auto-update window
}

// NewTestConfig creates a Config with sensible defaults for testing.
// Use the setter methods to override specific values.
func NewTestConfig() *Config {
	return &Config{
		pollInterval:     6 * time.Hour,
		gracePeriod:      30 * time.Second,
		defaultPolicy:    "manual",
		latestAutoUpdate: true,
		imageCleanup:     true,
		dependencyAware:  true,
		showStopped:      true,
	}
}

// Load reads all configuration from environment variables with defaults.
func Load() *Config {
	r := &envReader{}
	c := &Config{
		DockerSock:          envStr("SENTINEL_DOCKER_SOCK", "/var/run/docker.sock"),
		pollInterval:        r.duration("SENTINEL_POLL_INTERVAL", 6*time.Hour),
		gracePeriod:         r.duration("SENTINEL_GRACE_PERIOD", 30*time.Second),
		defaultPolicy:       envStr("SENTINEL_DEFAULT_POLICY", "manual"),
		latestAutoUpdate:    r.bool("SENTINEL_LATEST_AUTO_UPDATE", false),
		DBPath:              envStr("SENTINEL_DB_PATH", "/data/sentinel.db"),
		LogJSON:             r.bool("SENTINEL_LOG_JSON", true),
		GotifyURL:           envStr("SENTINEL_GOTIFY_URL", ""),
		GotifyToken:         envStr("SENTINEL_GOTIFY_TOKEN", ""),
		WebhookURL:          envStr("SENTINEL_WEBHOOK_URL", ""),
		WebhookHeaders:      envStr("SENTINEL_WEBHOOK_HEADERS", ""),
		WebPort:             envStr("SENTINEL_WEB_PORT", "8080"),
		WebEnabled:          r.bool("SENTINEL_WEB_ENABLED", true),
		HostAddress:         envStr("SENTINEL_HOST", ""),
		AuthEnabled:         r.boolPtr("SENTINEL_AUTH_ENABLED"),
		SessionExpiry:       r.duration("SENTINEL_SESSION_EXPIRY", 720*time.Hour),
		CookieSecure:        r.bool("SENTINEL_COOKIE_SECURE", true),
		TLSCert:             envStr("SENTINEL_TLS_CERT", ""),
		TLSKey:              envStr("SENTINEL_TLS_KEY", ""),
		TLSAuto:             r.bool("SENTINEL_TLS_AUTO", false),
		WebAuthnRPID:        envStr("SENTINEL_WEBAUTHN_RPID", ""),
		WebAuthnDisplayName: envStr("SENTINEL_WEBAUTHN_DISPLAY_NAME", "Docker-Sentinel"),
		WebAuthnOrigins:     envStr("SENTINEL_WEBAUTHN_ORIGINS", ""),
		imageCleanup:        r.bool("SENTINEL_IMAGE_CLEANUP", true),
		schedule:            envStr("SENTINEL_SCHEDULE", ""),
		hooksEnabled:        r.bool("SENTINEL_HOOKS", false),
		hooksWriteLabels:    r.bool("SENTINEL_HOOKS_WRITE_LABELS", false),
		dependencyAware:     r.bool("SENTINEL_DEPS", true),
		rollbackPolicy:      envStr("SENTINEL_ROLLBACK_POLICY", ""),
		MetricsEnabled:      r.bool("SENTINEL_METRICS", false),
		MetricsTextfile:     envStr("SENTINEL_METRICS_TEXTFILE", ""),
		imageBackup:         r.bool("SENTINEL_IMAGE_BACKUP", false),
		showStopped:         r.bool("SENTINEL_SHOW_STOPPED", true),
		removeVolumes:       r.bool("SENTINEL_REMOVE_VOLUMES", false),
		scanConcurrency:     r.int("SENTINEL_SCAN_CONCURRENCY", 1),
		maintenanceWindow:   envStr("SENTINEL_MAINTENANCE_WINDOW", ""),

		// Cluster / multi-host
		Mode:                 envStr("SENTINEL_MODE", ""),
		ClusterEnabled:       r.bool("SENTINEL_CLUSTER", false),
		ClusterPort:          envStr("SENTINEL_CLUSTER_PORT", "9443"),
		ClusterDataDir:       envStr("SENTINEL_CLUSTER_DIR", "/data/cluster"),
		ClusterAdvertiseAddr: envStr("SENTINEL_CLUSTER_ADVERTISE", ""),
		ServerAddr:           envStr("SENTINEL_SERVER_ADDR", ""),
		EnrollToken:          envStr("SENTINEL_ENROLL_TOKEN", ""),
		HostName:             envStr("SENTINEL_HOST_NAME", ""),
		GracePeriodOffline:   r.duration("SENTINEL_GRACE_PERIOD_OFFLINE", 30*time.Minute),

		// NPM integration
		NPMURL:      envStr("SENTINEL_NPM_URL", ""),
		NPMEmail:    envStr("SENTINEL_NPM_EMAIL", ""),
		NPMPassword: envStr("SENTINEL_NPM_PASSWORD", ""),

		HistoryRetentionDays: r.int("SENTINEL_HISTORY_RETENTION_DAYS", 90),
		TrustedProxies:       envStr("SENTINEL_TRUSTED_PROXIES", ""),
		OIDCLinkExisting:     r.bool("SENTINEL_OIDC_LINK_EXISTING", false),
		OIDCLinkAdmin:        envStr("SENTINEL_OIDC_LINK_ADMIN", ""),
	}
	c.envProblems = r.problems
	return c
}

// Validate checks configuration for invalid values.
func (c *Config) Validate() error {
	c.mu.RLock()
	pi := c.pollInterval
	gp := c.gracePeriod
	dp := c.defaultPolicy
	c.mu.RUnlock()

	var errs []error
	if c.HistoryRetentionDays < 0 {
		errs = append(errs, fmt.Errorf("SENTINEL_HISTORY_RETENTION_DAYS must be >= 0 (0 keeps everything), got %d", c.HistoryRetentionDays))
	}
	if pi <= 0 {
		errs = append(errs, fmt.Errorf("SENTINEL_POLL_INTERVAL must be > 0, got %s", pi))
	}
	if gp < 0 {
		errs = append(errs, fmt.Errorf("SENTINEL_GRACE_PERIOD must be >= 0, got %s", gp))
	}
	switch dp {
	case "auto", "manual", "pinned":
		// valid
	default:
		errs = append(errs, fmt.Errorf("SENTINEL_DEFAULT_POLICY must be auto, manual, or pinned, got %q", dp))
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		errs = append(errs, fmt.Errorf("SENTINEL_TLS_CERT and SENTINEL_TLS_KEY must both be set or both empty"))
	}
	// WebAuthn: RPID and origins must both be set or both empty.
	if c.WebAuthnRPID != "" && c.WebAuthnOrigins == "" {
		errs = append(errs, fmt.Errorf("SENTINEL_WEBAUTHN_ORIGINS is required when SENTINEL_WEBAUTHN_RPID is set"))
	}
	if c.WebAuthnRPID == "" && c.WebAuthnOrigins != "" {
		errs = append(errs, fmt.Errorf("SENTINEL_WEBAUTHN_RPID is required when SENTINEL_WEBAUTHN_ORIGINS is set"))
	}

	// Cluster mode validation.
	if c.Mode != "" && c.Mode != "server" && c.Mode != "agent" {
		errs = append(errs, fmt.Errorf("SENTINEL_MODE must be 'server' or 'agent', got %q", c.Mode))
	}
	if c.Mode == "agent" {
		if c.ServerAddr == "" && c.EnrollToken == "" {
			errs = append(errs, fmt.Errorf("SENTINEL_SERVER_ADDR is required in agent mode"))
		}
		// Web UI check removed: agents now run a minimal web UI for setup.
	}

	return errors.Join(errs...)
}

// Values returns all configuration as a string map for display.
func (c *Config) Values() map[string]string {
	c.mu.RLock()
	pi := c.pollInterval
	gp := c.gracePeriod
	dp := c.defaultPolicy
	ic := c.imageCleanup
	sched := c.schedule
	he := c.hooksEnabled
	hwl := c.hooksWriteLabels
	da := c.dependencyAware
	rp := c.rollbackPolicy
	ib := c.imageBackup
	ss := c.showStopped
	rv := c.removeVolumes
	sc := c.scanConcurrency
	mw := c.maintenanceWindow
	c.mu.RUnlock()

	return map[string]string{
		"SENTINEL_DOCKER_SOCK":           c.DockerSock,
		"SENTINEL_POLL_INTERVAL":         pi.String(),
		"SENTINEL_GRACE_PERIOD":          gp.String(),
		"SENTINEL_DEFAULT_POLICY":        dp,
		"SENTINEL_DB_PATH":               c.DBPath,
		"SENTINEL_LOG_JSON":              fmt.Sprintf("%t", c.LogJSON),
		"SENTINEL_GOTIFY_URL":            c.GotifyURL,
		"SENTINEL_WEBHOOK_URL":           c.WebhookURL,
		"SENTINEL_WEB_PORT":              c.WebPort,
		"SENTINEL_WEB_ENABLED":           fmt.Sprintf("%t", c.WebEnabled),
		"SENTINEL_HOST":                  c.HostAddress,
		"SENTINEL_SESSION_EXPIRY":        c.SessionExpiry.String(),
		"SENTINEL_COOKIE_SECURE":         fmt.Sprintf("%t", c.CookieSecure),
		"SENTINEL_TLS_CERT":              c.TLSCert,
		"SENTINEL_TLS_KEY":               redactPath(c.TLSKey),
		"SENTINEL_TLS_AUTO":              fmt.Sprintf("%t", c.TLSAuto),
		"SENTINEL_WEBAUTHN_RPID":         c.WebAuthnRPID,
		"SENTINEL_WEBAUTHN_DISPLAY_NAME": c.WebAuthnDisplayName,
		"SENTINEL_WEBAUTHN_ORIGINS":      c.WebAuthnOrigins,
		"SENTINEL_IMAGE_CLEANUP":         fmt.Sprintf("%t", ic),
		"SENTINEL_SCHEDULE":              sched,
		"SENTINEL_HOOKS":                 fmt.Sprintf("%t", he),
		"SENTINEL_HOOKS_WRITE_LABELS":    fmt.Sprintf("%t", hwl),
		"SENTINEL_DEPS":                  fmt.Sprintf("%t", da),
		"SENTINEL_ROLLBACK_POLICY":       rp,
		"SENTINEL_METRICS":               fmt.Sprintf("%t", c.MetricsEnabled),

		// Cluster / multi-host
		"SENTINEL_MODE":                   c.Mode,
		"SENTINEL_CLUSTER":                fmt.Sprintf("%t", c.ClusterEnabled),
		"SENTINEL_CLUSTER_PORT":           c.ClusterPort,
		"SENTINEL_CLUSTER_DIR":            c.ClusterDataDir,
		"SENTINEL_CLUSTER_ADVERTISE":      c.ClusterAdvertiseAddr,
		"SENTINEL_SERVER_ADDR":            c.ServerAddr,
		"SENTINEL_HOST_NAME":              c.HostName,
		"SENTINEL_GRACE_PERIOD_OFFLINE":   c.GracePeriodOffline.String(),
		"SENTINEL_IMAGE_BACKUP":           fmt.Sprintf("%t", ib),
		"SENTINEL_SHOW_STOPPED":           fmt.Sprintf("%t", ss),
		"SENTINEL_REMOVE_VOLUMES":         fmt.Sprintf("%t", rv),
		"SENTINEL_SCAN_CONCURRENCY":       fmt.Sprintf("%d", sc),
		"SENTINEL_MAINTENANCE_WINDOW":     mw,
		"SENTINEL_OIDC_LINK_EXISTING":     fmt.Sprintf("%t", c.OIDCLinkExisting),
		"SENTINEL_HISTORY_RETENTION_DAYS": fmt.Sprintf("%d", c.HistoryRetentionDays),

		// NPM
		"SENTINEL_NPM_URL": c.NPMURL,
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envReader parses typed environment variables and remembers every value
// that is set but unparseable, so Validate can refuse to start instead of
// quietly using the default.
type envReader struct{ problems []EnvProblem }

// EnvProblem describes an environment variable that was set but could not be
// parsed, so its default is in use.
type EnvProblem struct {
	Key     string
	Value   string // empty when the value is secret and must not be logged
	Default string
	Want    string // what a valid value looks like
}

// EnvProblems returns the environment values that could not be parsed. The
// server keeps running with the defaults and logs each one at startup.
func (c *Config) EnvProblems() []EnvProblem {
	return append([]EnvProblem(nil), c.envProblems...)
}

func (r *envReader) bad(key, value, def, want string) {
	if isSecretEnv(key, value) {
		value = ""
	}
	r.problems = append(r.problems, EnvProblem{Key: key, Value: value, Default: def, Want: want})
}

// isSecretEnv reports whether a variable's value must never be logged.
func isSecretEnv(key, value string) bool {
	k := strings.ToUpper(key)
	for _, w := range []string{"TOKEN", "PASSWORD", "SECRET", "KEY"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return strings.Contains(value, "://") && strings.Contains(value, "@")
}

// boolPtr returns a *bool from env. Returns nil if unset (lets DB default apply).
func (r *envReader) boolPtr(key string) *bool {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	b, err := parseBool(v)
	if err != nil {
		r.bad(key, v, "unset", "true or false")
		return nil
	}
	return &b
}

func (r *envReader) bool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := parseBool(v)
	if err != nil {
		r.bad(key, v, strconv.FormatBool(def), "true or false")
		return def
	}
	return b
}

func (r *envReader) int(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.bad(key, v, strconv.Itoa(def), "a whole number")
		return def
	}
	return n
}

func (r *envReader) duration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.bad(key, v, def.String(), "a duration with a unit, for example 6h or 30m")
		return def
	}
	return d
}

// parseBool accepts what strconv does plus yes/no/on/off, which people
// naturally write.
func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "y", "on":
		return true, nil
	case "no", "n", "off":
		return false, nil
	}
	return strconv.ParseBool(strings.TrimSpace(v))
}

// envBool, envDuration are the lenient forms: a bad value gives the default.
func envBool(key string, def bool) bool {
	return (&envReader{}).bool(key, def)
}

func envDuration(key string, def time.Duration) time.Duration {
	return (&envReader{}).duration(key, def)
}

// PollInterval returns the current poll interval (thread-safe).
func (c *Config) PollInterval() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pollInterval
}

// SetPollInterval updates the poll interval at runtime (thread-safe).
func (c *Config) SetPollInterval(d time.Duration) {
	c.mu.Lock()
	c.pollInterval = d
	c.mu.Unlock()
}

// GracePeriod returns the current grace period (thread-safe).
func (c *Config) GracePeriod() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.gracePeriod
}

// SetGracePeriod updates the grace period at runtime (thread-safe).
func (c *Config) SetGracePeriod(d time.Duration) {
	c.mu.Lock()
	c.gracePeriod = d
	c.mu.Unlock()
}

// DefaultPolicy returns the current default policy (thread-safe).
func (c *Config) DefaultPolicy() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.defaultPolicy
}

// SetDefaultPolicy updates the default policy at runtime (thread-safe).
func (c *Config) SetDefaultPolicy(s string) {
	c.mu.Lock()
	c.defaultPolicy = s
	c.mu.Unlock()
}

// LatestAutoUpdate returns whether :latest containers auto-update (thread-safe).
func (c *Config) LatestAutoUpdate() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latestAutoUpdate
}

// SetLatestAutoUpdate updates the :latest auto-update setting at runtime (thread-safe).
func (c *Config) SetLatestAutoUpdate(b bool) {
	c.mu.Lock()
	c.latestAutoUpdate = b
	c.mu.Unlock()
}

func (c *Config) ImageCleanup() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.imageCleanup
}

func (c *Config) SetImageCleanup(b bool) {
	c.mu.Lock()
	c.imageCleanup = b
	c.mu.Unlock()
}

func (c *Config) Schedule() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.schedule
}

func (c *Config) SetSchedule(s string) {
	c.mu.Lock()
	c.schedule = s
	c.mu.Unlock()
}

func (c *Config) HooksEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hooksEnabled
}

func (c *Config) SetHooksEnabled(b bool) {
	c.mu.Lock()
	c.hooksEnabled = b
	c.mu.Unlock()
}

func (c *Config) HooksWriteLabels() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hooksWriteLabels
}

func (c *Config) SetHooksWriteLabels(b bool) {
	c.mu.Lock()
	c.hooksWriteLabels = b
	c.mu.Unlock()
}

func (c *Config) DependencyAware() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dependencyAware
}

func (c *Config) SetDependencyAware(b bool) {
	c.mu.Lock()
	c.dependencyAware = b
	c.mu.Unlock()
}

func (c *Config) RollbackPolicy() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rollbackPolicy
}

func (c *Config) SetRollbackPolicy(s string) {
	c.mu.Lock()
	c.rollbackPolicy = s
	c.mu.Unlock()
}

func (c *Config) ImageBackup() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.imageBackup
}

func (c *Config) SetImageBackup(b bool) {
	c.mu.Lock()
	c.imageBackup = b
	c.mu.Unlock()
}

func (c *Config) ShowStopped() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.showStopped
}

func (c *Config) SetShowStopped(b bool) {
	c.mu.Lock()
	c.showStopped = b
	c.mu.Unlock()
}

func (c *Config) RemoveVolumes() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.removeVolumes
}

func (c *Config) SetRemoveVolumes(b bool) {
	c.mu.Lock()
	c.removeVolumes = b
	c.mu.Unlock()
}

// maxScanConcurrency is the hard upper bound for parallel registry checks.
// Values above this are clamped to prevent resource exhaustion.
const maxScanConcurrency = 50

func (c *Config) ScanConcurrency() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.scanConcurrency > maxScanConcurrency {
		return maxScanConcurrency
	}
	return c.scanConcurrency
}

func (c *Config) SetScanConcurrency(n int) {
	if n > maxScanConcurrency {
		n = maxScanConcurrency
	}
	c.mu.Lock()
	c.scanConcurrency = n
	c.mu.Unlock()
}

func (c *Config) MaintenanceWindow() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.maintenanceWindow
}

func (c *Config) SetMaintenanceWindow(s string) {
	c.mu.Lock()
	c.maintenanceWindow = s
	c.mu.Unlock()
}

// redactPath returns "(set)" if the path is non-empty, empty string otherwise.
func redactPath(s string) string {
	if s != "" {
		return "(set)"
	}
	return ""
}

// TLSEnabled returns true when TLS is configured (cert+key or auto).
func (c *Config) TLSEnabled() bool {
	return (c.TLSCert != "" && c.TLSKey != "") || c.TLSAuto
}

// WebAuthnEnabled returns true when WebAuthn passkeys are configured.
func (c *Config) WebAuthnEnabled() bool {
	return c.WebAuthnRPID != ""
}

// WebAuthnOriginList parses the comma-separated origins into a slice.
func (c *Config) WebAuthnOriginList() []string {
	if c.WebAuthnOrigins == "" {
		return nil
	}
	var origins []string
	for _, o := range strings.Split(c.WebAuthnOrigins, ",") {
		if trimmed := strings.TrimSpace(o); trimmed != "" {
			origins = append(origins, trimmed)
		}
	}
	return origins
}

// IsAgent returns true when running in agent mode.
func (c *Config) IsAgent() bool {
	return c.Mode == "agent"
}

// IsServer returns true when running in server mode (explicit or default).
func (c *Config) IsServer() bool {
	return c.Mode == "" || c.Mode == "server"
}
