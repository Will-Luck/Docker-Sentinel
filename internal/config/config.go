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
}

// Load reads all configuration from environment variables with defaults.
func Load() *Config {
	return &Config{
		DockerSock:    envStr("SENTINEL_DOCKER_SOCK", "/var/run/docker.sock"),
		PollInterval:  envDuration("SENTINEL_POLL_INTERVAL", 6*time.Hour),
		GracePeriod:   envDuration("SENTINEL_GRACE_PERIOD", 30*time.Second),
		DefaultPolicy: envStr("SENTINEL_DEFAULT_POLICY", "manual"),
		DBPath:        envStr("SENTINEL_DB_PATH", "/data/sentinel.db"),
		LogJSON:       envBool("SENTINEL_LOG_JSON", true),
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
