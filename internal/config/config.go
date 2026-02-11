package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all Docker-Sentinel configuration from environment variables.
type Config struct {
	// Docker connection
	DockerSock string

	// Scanning
	PollInterval time.Duration // how often to scan for updates
	GracePeriod  time.Duration // wait after starting new container before health check

	// Update policy
	DefaultPolicy string // "auto", "manual", or "pinned"

	// Storage
	DBPath string

	// Logging
	LogJSON bool

	// Notifications
	GotifyURL      string
	GotifyToken    string
	WebhookURL     string
	WebhookHeaders string // comma-separated "Key:Value" pairs

	// Registry
	DockerConfigPath string // path to docker config.json for private registry auth

	// Web dashboard
	WebPort    string
	WebEnabled bool
}

// Load reads all configuration from environment variables with defaults.
func Load() *Config {
	return &Config{
		DockerSock:       envStr("SENTINEL_DOCKER_SOCK", "/var/run/docker.sock"),
		PollInterval:     envDuration("SENTINEL_POLL_INTERVAL", 6*time.Hour),
		GracePeriod:      envDuration("SENTINEL_GRACE_PERIOD", 30*time.Second),
		DefaultPolicy:    envStr("SENTINEL_DEFAULT_POLICY", "manual"),
		DBPath:           envStr("SENTINEL_DB_PATH", "/data/sentinel.db"),
		LogJSON:          envBool("SENTINEL_LOG_JSON", true),
		GotifyURL:        envStr("SENTINEL_GOTIFY_URL", ""),
		GotifyToken:      envStr("SENTINEL_GOTIFY_TOKEN", ""),
		WebhookURL:       envStr("SENTINEL_WEBHOOK_URL", ""),
		WebhookHeaders:   envStr("SENTINEL_WEBHOOK_HEADERS", ""),
		DockerConfigPath: envStr("SENTINEL_DOCKER_CONFIG", "/root/.docker/config.json"),
		WebPort:          envStr("SENTINEL_WEB_PORT", "8080"),
		WebEnabled:       envBool("SENTINEL_WEB_ENABLED", true),
	}
}

// Validate checks configuration for invalid values.
func (c *Config) Validate() error {
	var errs []error
	if c.PollInterval <= 0 {
		errs = append(errs, fmt.Errorf("SENTINEL_POLL_INTERVAL must be > 0, got %s", c.PollInterval))
	}
	if c.GracePeriod < 0 {
		errs = append(errs, fmt.Errorf("SENTINEL_GRACE_PERIOD must be >= 0, got %s", c.GracePeriod))
	}
	switch c.DefaultPolicy {
	case "auto", "manual", "pinned":
		// valid
	default:
		errs = append(errs, fmt.Errorf("SENTINEL_DEFAULT_POLICY must be auto, manual, or pinned, got %q", c.DefaultPolicy))
	}
	return errors.Join(errs...)
}

// Values returns all configuration as a string map for display.
func (c *Config) Values() map[string]string {
	return map[string]string{
		"SENTINEL_DOCKER_SOCK":    c.DockerSock,
		"SENTINEL_POLL_INTERVAL":  c.PollInterval.String(),
		"SENTINEL_GRACE_PERIOD":   c.GracePeriod.String(),
		"SENTINEL_DEFAULT_POLICY": c.DefaultPolicy,
		"SENTINEL_DB_PATH":        c.DBPath,
		"SENTINEL_LOG_JSON":       fmt.Sprintf("%t", c.LogJSON),
		"SENTINEL_GOTIFY_URL":     c.GotifyURL,
		"SENTINEL_WEBHOOK_URL":    c.WebhookURL,
		"SENTINEL_DOCKER_CONFIG":  c.DockerConfigPath,
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

// SetPollInterval updates the poll interval at runtime.
func (c *Config) SetPollInterval(d time.Duration) {
	c.PollInterval = d
}
