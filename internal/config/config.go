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
	WebPort    string
	WebEnabled bool

	// Authentication
	AuthEnabled   *bool // nil = use DB default (true); non-nil = env override
	SessionExpiry time.Duration
	CookieSecure  bool

	// TLS
	TLSCert string // path to TLS certificate PEM file
	TLSKey  string // path to TLS private key PEM file
	TLSAuto bool   // auto-generate self-signed certificate

	// WebAuthn passkeys (all empty = disabled)
	WebAuthnRPID        string // Relying Party ID (e.g. "192.168.1.57")
	WebAuthnDisplayName string // RP display name shown by authenticators
	WebAuthnOrigins     string // comma-separated allowed origins
	MetricsEnabled      bool

	// mu protects the mutable runtime fields below.
	mu               sync.RWMutex
	pollInterval     time.Duration // how often to scan for updates
	gracePeriod      time.Duration // wait after starting new container before health check
	defaultPolicy    string        // "auto", "manual", or "pinned"
	latestAutoUpdate bool          // auto-update :latest containers regardless of default policy
	imageCleanup     bool
	schedule         string
	hooksEnabled     bool
	hooksWriteLabels bool
	dependencyAware  bool
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
	}
}

// Load reads all configuration from environment variables with defaults.
func Load() *Config {
	return &Config{
		DockerSock:          envStr("SENTINEL_DOCKER_SOCK", "/var/run/docker.sock"),
		pollInterval:        envDuration("SENTINEL_POLL_INTERVAL", 6*time.Hour),
		gracePeriod:         envDuration("SENTINEL_GRACE_PERIOD", 30*time.Second),
		defaultPolicy:       envStr("SENTINEL_DEFAULT_POLICY", "manual"),
		latestAutoUpdate:    envBool("SENTINEL_LATEST_AUTO_UPDATE", true),
		DBPath:              envStr("SENTINEL_DB_PATH", "/data/sentinel.db"),
		LogJSON:             envBool("SENTINEL_LOG_JSON", true),
		GotifyURL:           envStr("SENTINEL_GOTIFY_URL", ""),
		GotifyToken:         envStr("SENTINEL_GOTIFY_TOKEN", ""),
		WebhookURL:          envStr("SENTINEL_WEBHOOK_URL", ""),
		WebhookHeaders:      envStr("SENTINEL_WEBHOOK_HEADERS", ""),
		WebPort:             envStr("SENTINEL_WEB_PORT", "8080"),
		WebEnabled:          envBool("SENTINEL_WEB_ENABLED", true),
		AuthEnabled:         envBoolPtr("SENTINEL_AUTH_ENABLED"),
		SessionExpiry:       envDuration("SENTINEL_SESSION_EXPIRY", 720*time.Hour),
		CookieSecure:        envBool("SENTINEL_COOKIE_SECURE", true),
		TLSCert:             envStr("SENTINEL_TLS_CERT", ""),
		TLSKey:              envStr("SENTINEL_TLS_KEY", ""),
		TLSAuto:             envBool("SENTINEL_TLS_AUTO", false),
		WebAuthnRPID:        envStr("SENTINEL_WEBAUTHN_RPID", ""),
		WebAuthnDisplayName: envStr("SENTINEL_WEBAUTHN_DISPLAY_NAME", "Docker-Sentinel"),
		WebAuthnOrigins:     envStr("SENTINEL_WEBAUTHN_ORIGINS", ""),
		imageCleanup:        envBool("SENTINEL_IMAGE_CLEANUP", true),
		schedule:            envStr("SENTINEL_SCHEDULE", ""),
		hooksEnabled:        envBool("SENTINEL_HOOKS", false),
		hooksWriteLabels:    envBool("SENTINEL_HOOKS_WRITE_LABELS", false),
		dependencyAware:     envBool("SENTINEL_DEPS", true),
		MetricsEnabled:      envBool("SENTINEL_METRICS", false),
	}
}

// Validate checks configuration for invalid values.
func (c *Config) Validate() error {
	c.mu.RLock()
	pi := c.pollInterval
	gp := c.gracePeriod
	dp := c.defaultPolicy
	c.mu.RUnlock()

	var errs []error
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
		"SENTINEL_METRICS":               fmt.Sprintf("%t", c.MetricsEnabled),
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envBoolPtr returns a *bool from env. Returns nil if unset (lets DB default apply).
func envBoolPtr(key string) *bool {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil
	}
	return &b
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
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
