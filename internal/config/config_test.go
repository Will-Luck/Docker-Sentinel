package config

import (
	"os"
	"sync"
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
	if cfg.PollInterval() != 6*time.Hour {
		t.Errorf("PollInterval = %s, want 6h", cfg.PollInterval())
	}
	if cfg.GracePeriod() != 30*time.Second {
		t.Errorf("GracePeriod = %s, want 30s", cfg.GracePeriod())
	}
	if cfg.DefaultPolicy() != "manual" {
		t.Errorf("DefaultPolicy = %q, want manual", cfg.DefaultPolicy())
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
	if cfg.PollInterval() != time.Hour {
		t.Errorf("PollInterval = %s, want 1h", cfg.PollInterval())
	}
	if cfg.GracePeriod() != 10*time.Second {
		t.Errorf("GracePeriod = %s, want 10s", cfg.GracePeriod())
	}
	if cfg.DefaultPolicy() != "auto" {
		t.Errorf("DefaultPolicy = %q, want auto", cfg.DefaultPolicy())
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
		{"zero poll interval", func(c *Config) { c.SetPollInterval(0) }, true},
		{"negative grace period", func(c *Config) { c.SetGracePeriod(-1) }, true},
		{"invalid policy", func(c *Config) { c.SetDefaultPolicy("yolo") }, true},
		{"pinned policy valid", func(c *Config) { c.SetDefaultPolicy("pinned") }, false},
		{"auto policy valid", func(c *Config) { c.SetDefaultPolicy("auto") }, false},
		{"TLS cert without key", func(c *Config) { c.TLSCert = "/tmp/cert.pem" }, true},
		{"TLS key without cert", func(c *Config) { c.TLSKey = "/tmp/key.pem" }, true},
		{"TLS cert and key both set", func(c *Config) { c.TLSCert = "/tmp/cert.pem"; c.TLSKey = "/tmp/key.pem" }, false},
		{"TLS auto only", func(c *Config) { c.TLSAuto = true }, false},
		{"WebAuthn RPID without origins", func(c *Config) { c.WebAuthnRPID = "example.com" }, true},
		{"WebAuthn origins without RPID", func(c *Config) { c.WebAuthnOrigins = "https://example.com" }, true},
		{"WebAuthn RPID and origins both set", func(c *Config) {
			c.WebAuthnRPID = "example.com"
			c.WebAuthnOrigins = "https://example.com"
		}, false},
		{"WebAuthn both empty", func(c *Config) {}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewTestConfig()
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

func TestSettersAndGetters(t *testing.T) {
	cfg := NewTestConfig()

	cfg.SetPollInterval(15 * time.Minute)
	if got := cfg.PollInterval(); got != 15*time.Minute {
		t.Errorf("PollInterval = %s, want 15m", got)
	}

	cfg.SetGracePeriod(45 * time.Second)
	if got := cfg.GracePeriod(); got != 45*time.Second {
		t.Errorf("GracePeriod = %s, want 45s", got)
	}

	cfg.SetDefaultPolicy("auto")
	if got := cfg.DefaultPolicy(); got != "auto" {
		t.Errorf("DefaultPolicy = %q, want auto", got)
	}
}

func TestTLSEnabled(t *testing.T) {
	tests := []struct {
		name string
		cert string
		key  string
		auto bool
		want bool
	}{
		{"no TLS", "", "", false, false},
		{"cert+key", "/cert.pem", "/key.pem", false, true},
		{"auto", "", "", true, true},
		{"cert+key+auto", "/cert.pem", "/key.pem", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewTestConfig()
			cfg.TLSCert = tt.cert
			cfg.TLSKey = tt.key
			cfg.TLSAuto = tt.auto
			if got := cfg.TLSEnabled(); got != tt.want {
				t.Errorf("TLSEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBoolSettersAndGetters(t *testing.T) {
	tests := []struct {
		name   string
		set    func(*Config, bool)
		get    func(*Config) bool
		defVal bool // NewTestConfig default
	}{
		{"LatestAutoUpdate", (*Config).SetLatestAutoUpdate, (*Config).LatestAutoUpdate, true},
		{"ImageCleanup", (*Config).SetImageCleanup, (*Config).ImageCleanup, true},
		{"HooksEnabled", (*Config).SetHooksEnabled, (*Config).HooksEnabled, false},
		{"HooksWriteLabels", (*Config).SetHooksWriteLabels, (*Config).HooksWriteLabels, false},
		{"DependencyAware", (*Config).SetDependencyAware, (*Config).DependencyAware, true},
		{"ImageBackup", (*Config).SetImageBackup, (*Config).ImageBackup, false},
		{"ShowStopped", (*Config).SetShowStopped, (*Config).ShowStopped, false},
		{"RemoveVolumes", (*Config).SetRemoveVolumes, (*Config).RemoveVolumes, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewTestConfig()
			if got := tt.get(cfg); got != tt.defVal {
				t.Fatalf("default = %v, want %v", got, tt.defVal)
			}
			tt.set(cfg, !tt.defVal)
			if got := tt.get(cfg); got != !tt.defVal {
				t.Fatalf("after Set(%v) = %v, want %v", !tt.defVal, got, !tt.defVal)
			}
		})
	}
}

func TestStringSettersAndGetters(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Config, string)
		get  func(*Config) string
	}{
		{"Schedule", (*Config).SetSchedule, (*Config).Schedule},
		{"RollbackPolicy", (*Config).SetRollbackPolicy, (*Config).RollbackPolicy},
		{"MaintenanceWindow", (*Config).SetMaintenanceWindow, (*Config).MaintenanceWindow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewTestConfig()
			if got := tt.get(cfg); got != "" {
				t.Fatalf("default = %q, want empty", got)
			}
			tt.set(cfg, "test-value")
			if got := tt.get(cfg); got != "test-value" {
				t.Fatalf("after Set = %q, want %q", got, "test-value")
			}
		})
	}
}

func TestScanConcurrencyClamping(t *testing.T) {
	tests := []struct {
		name string
		set  int
		want int
	}{
		{"normal", 10, 10},
		{"clamped", 100, 50},
		{"boundary", 50, 50},
		{"zero", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewTestConfig()
			cfg.SetScanConcurrency(tt.set)
			if got := cfg.ScanConcurrency(); got != tt.want {
				t.Errorf("SetScanConcurrency(%d) -> ScanConcurrency() = %d, want %d", tt.set, got, tt.want)
			}
		})
	}
}

func TestWebAuthnEnabled(t *testing.T) {
	cfg := NewTestConfig()
	if cfg.WebAuthnEnabled() {
		t.Error("WebAuthnEnabled() = true with empty RPID, want false")
	}
	cfg.WebAuthnRPID = "example.com"
	if !cfg.WebAuthnEnabled() {
		t.Error("WebAuthnEnabled() = false with non-empty RPID, want true")
	}
}

func TestWebAuthnOriginList(t *testing.T) {
	tests := []struct {
		name    string
		origins string
		want    []string
	}{
		{"empty", "", nil},
		{"single", "https://a.com", []string{"https://a.com"}},
		{"multiple with spaces", "https://a.com, https://b.com", []string{"https://a.com", "https://b.com"}},
		{"skip empty segments", "https://a.com,,https://b.com", []string{"https://a.com", "https://b.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewTestConfig()
			cfg.WebAuthnOrigins = tt.origins
			got := cfg.WebAuthnOriginList()
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d; got %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsAgentIsServer(t *testing.T) {
	tests := []struct {
		mode     string
		isAgent  bool
		isServer bool
	}{
		{"", false, true},
		{"server", false, true},
		{"agent", true, false},
	}
	for _, tt := range tests {
		t.Run("mode="+tt.mode, func(t *testing.T) {
			cfg := NewTestConfig()
			cfg.Mode = tt.mode
			if got := cfg.IsAgent(); got != tt.isAgent {
				t.Errorf("IsAgent() = %v, want %v", got, tt.isAgent)
			}
			if got := cfg.IsServer(); got != tt.isServer {
				t.Errorf("IsServer() = %v, want %v", got, tt.isServer)
			}
		})
	}
}

func TestValuesCorrectness(t *testing.T) {
	cfg := NewTestConfig()
	cfg.SetSchedule("0 2 * * *")
	cfg.SetRollbackPolicy("manual")
	cfg.SetScanConcurrency(5)
	cfg.SetMaintenanceWindow("02:00-06:00")

	vals := cfg.Values()

	checks := map[string]string{
		"SENTINEL_POLL_INTERVAL":      "6h0m0s",
		"SENTINEL_GRACE_PERIOD":       "30s",
		"SENTINEL_DEFAULT_POLICY":     "manual",
		"SENTINEL_IMAGE_CLEANUP":      "true",
		"SENTINEL_SCHEDULE":           "0 2 * * *",
		"SENTINEL_HOOKS":              "false",
		"SENTINEL_HOOKS_WRITE_LABELS": "false",
		"SENTINEL_DEPS":               "true",
		"SENTINEL_ROLLBACK_POLICY":    "manual",
		"SENTINEL_IMAGE_BACKUP":       "false",
		"SENTINEL_SHOW_STOPPED":       "false",
		"SENTINEL_REMOVE_VOLUMES":     "false",
		"SENTINEL_SCAN_CONCURRENCY":   "5",
		"SENTINEL_MAINTENANCE_WINDOW": "02:00-06:00",
	}
	for key, want := range checks {
		if got := vals[key]; got != want {
			t.Errorf("Values()[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestConcurrentAccess(t *testing.T) {
	cfg := NewTestConfig()

	var wg sync.WaitGroup
	wg.Add(3)

	// Writer goroutine for PollInterval.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			cfg.SetPollInterval(time.Duration(i) * time.Minute)
		}
	}()

	// Writer goroutine for DefaultPolicy.
	go func() {
		defer wg.Done()
		policies := []string{"auto", "manual", "pinned"}
		for i := 0; i < 100; i++ {
			cfg.SetDefaultPolicy(policies[i%3])
		}
	}()

	// Reader goroutine.
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = cfg.PollInterval()
			_ = cfg.GracePeriod()
			_ = cfg.DefaultPolicy()
			_ = cfg.Values()
		}
	}()

	wg.Wait()
}
