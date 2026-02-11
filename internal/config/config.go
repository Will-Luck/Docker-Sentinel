package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
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

	// mu protects the mutable runtime fields below.
	mu            sync.RWMutex
	pollInterval  time.Duration // how often to scan for updates
	gracePeriod   time.Duration // wait after starting new container before health check
	defaultPolicy string        // "auto", "manual", or "pinned"
}

// NewTestConfig creates a Config with sensible defaults for testing.
// Use the setter methods to override specific values.
func NewTestConfig() *Config {
	return &Config{
		pollInterval:  6 * time.Hour,
		gracePeriod:   30 * time.Second,
		defaultPolicy: "manual",
	}
}

// Load reads all configuration from environment variables with defaults.
func Load() *Config {
	return &Config{
		DockerSock:     envStr("SENTINEL_DOCKER_SOCK", "/var/run/docker.sock"),
		pollInterval:   envDuration("SENTINEL_POLL_INTERVAL", 6*time.Hour),
		gracePeriod:    envDuration("SENTINEL_GRACE_PERIOD", 30*time.Second),
		defaultPolicy:  envStr("SENTINEL_DEFAULT_POLICY", "manual"),
		DBPath:         envStr("SENTINEL_DB_PATH", "/data/sentinel.db"),
		LogJSON:        envBool("SENTINEL_LOG_JSON", true),
		GotifyURL:      envStr("SENTINEL_GOTIFY_URL", ""),
		GotifyToken:    envStr("SENTINEL_GOTIFY_TOKEN", ""),
		WebhookURL:     envStr("SENTINEL_WEBHOOK_URL", ""),
		WebhookHeaders: envStr("SENTINEL_WEBHOOK_HEADERS", ""),
		WebPort:        envStr("SENTINEL_WEB_PORT", "8080"),
		WebEnabled:     envBool("SENTINEL_WEB_ENABLED", true),
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
	return errors.Join(errs...)
}

// Values returns all configuration as a string map for display.
func (c *Config) Values() map[string]string {
	c.mu.RLock()
	pi := c.pollInterval
	gp := c.gracePeriod
	dp := c.defaultPolicy
	c.mu.RUnlock()

	return map[string]string{
		"SENTINEL_DOCKER_SOCK":    c.DockerSock,
		"SENTINEL_POLL_INTERVAL":  pi.String(),
		"SENTINEL_GRACE_PERIOD":   gp.String(),
		"SENTINEL_DEFAULT_POLICY": dp,
		"SENTINEL_DB_PATH":        c.DBPath,
		"SENTINEL_LOG_JSON":       fmt.Sprintf("%t", c.LogJSON),
		"SENTINEL_GOTIFY_URL":     c.GotifyURL,
		"SENTINEL_WEBHOOK_URL":    c.WebhookURL,
		"SENTINEL_WEB_PORT":       c.WebPort,
		"SENTINEL_WEB_ENABLED":    fmt.Sprintf("%t", c.WebEnabled),
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
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
