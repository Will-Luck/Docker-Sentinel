package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// Unset all sentinel env vars to get defaults.
	for _, k := range []string{
		"SENTINEL_DOCKER_SOCK", "SENTINEL_POLL_INTERVAL", "SENTINEL_GRACE_PERIOD",
		"SENTINEL_DEFAULT_POLICY", "SENTINEL_DB_PATH", "SENTINEL_LOG_JSON",
	} {
		os.Unsetenv(k)
	}

	cfg := Load()
	if cfg.DockerSock != "/var/run/docker.sock" {
		t.Errorf("DockerSock = %q, want /var/run/docker.sock", cfg.DockerSock)
	}
	if cfg.PollInterval != 6*time.Hour {
		t.Errorf("PollInterval = %s, want 6h", cfg.PollInterval)
	}
	if cfg.GracePeriod != 30*time.Second {
		t.Errorf("GracePeriod = %s, want 30s", cfg.GracePeriod)
	}
	if cfg.DefaultPolicy != "manual" {
		t.Errorf("DefaultPolicy = %q, want manual", cfg.DefaultPolicy)
	}
	if cfg.DBPath != "/data/sentinel.db" {
		t.Errorf("DBPath = %q, want /data/sentinel.db", cfg.DBPath)
	}
	if !cfg.LogJSON {
		t.Error("LogJSON = false, want true")
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("SENTINEL_POLL_INTERVAL", "1h")
	t.Setenv("SENTINEL_GRACE_PERIOD", "10s")
	t.Setenv("SENTINEL_DEFAULT_POLICY", "auto")
	t.Setenv("SENTINEL_LOG_JSON", "false")

	cfg := Load()
	if cfg.PollInterval != time.Hour {
		t.Errorf("PollInterval = %s, want 1h", cfg.PollInterval)
	}
	if cfg.GracePeriod != 10*time.Second {
		t.Errorf("GracePeriod = %s, want 10s", cfg.GracePeriod)
	}
	if cfg.DefaultPolicy != "auto" {
		t.Errorf("DefaultPolicy = %q, want auto", cfg.DefaultPolicy)
	}
	if cfg.LogJSON {
		t.Error("LogJSON = true, want false")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*Config)
		wantErr bool
	}{
		{"valid defaults", func(_ *Config) {}, false},
		{"zero poll interval", func(c *Config) { c.PollInterval = 0 }, true},
		{"negative grace period", func(c *Config) { c.GracePeriod = -1 }, true},
		{"invalid policy", func(c *Config) { c.DefaultPolicy = "yolo" }, true},
		{"pinned policy valid", func(c *Config) { c.DefaultPolicy = "pinned" }, false},
		{"auto policy valid", func(c *Config) { c.DefaultPolicy = "auto" }, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				PollInterval:  6 * time.Hour,
				GracePeriod:   30 * time.Second,
				DefaultPolicy: "manual",
			}
			tt.modify(cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestEnvStr(t *testing.T) {
	const key = "DS_TEST_ENV_STR"
	t.Setenv(key, "custom")

	if got := envStr(key, "default"); got != "custom" {
		t.Errorf("got %q, want %q", got, "custom")
	}
	if got := envStr("DS_TEST_MISSING", "fallback"); got != "fallback" {
		t.Errorf("got %q, want %q", got, "fallback")
	}
}

func TestEnvInt(t *testing.T) {
	const key = "DS_TEST_ENV_INT"

	t.Setenv(key, "42")
	if got := envInt(key, 0); got != 42 {
		t.Errorf("got %d, want 42", got)
	}

	t.Setenv(key, "notanumber")
	if got := envInt(key, 99); got != 99 {
		t.Errorf("got %d, want 99 (default on parse failure)", got)
	}
}

func TestEnvBool(t *testing.T) {
	const key = "DS_TEST_ENV_BOOL"

	t.Setenv(key, "true")
	if got := envBool(key, false); !got {
		t.Errorf("got false, want true")
	}

	t.Setenv(key, "invalid")
	if got := envBool(key, true); !got {
		t.Errorf("got false, want true (default on parse failure)")
	}
}

func TestEnvDuration(t *testing.T) {
	const key = "DS_TEST_ENV_DUR"

	t.Setenv(key, "5m")
	if got := envDuration(key, time.Hour); got != 5*time.Minute {
		t.Errorf("got %s, want 5m", got)
	}

	t.Setenv(key, "notaduration")
	if got := envDuration(key, time.Hour); got != time.Hour {
		t.Errorf("got %s, want 1h (default on parse failure)", got)
	}
}
