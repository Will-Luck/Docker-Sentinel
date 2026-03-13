package web

import (
	"strings"
	"testing"
)

func TestValidateServiceURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "empty string", url: "", wantErr: true},
		{name: "ftp scheme rejected", url: "ftp://example.com", wantErr: true},
		{name: "no hostname", url: "http://", wantErr: true},
		{name: "loopback IPv4", url: "http://127.0.0.1:8080", wantErr: true},
		{name: "loopback IPv6", url: "http://[::1]:8080", wantErr: true},
		{name: "unspecified address", url: "http://0.0.0.0:8080", wantErr: true},
		{name: "private LAN allowed", url: "http://192.168.1.57:62453", wantErr: false},
		{name: "private 10.x allowed", url: "https://10.0.0.1:9443", wantErr: false},
		{name: "no scheme or host", url: "not-a-url", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateServiceURL(tt.url)
			if tt.wantErr && err == nil {
				t.Errorf("validateServiceURL(%q) = nil, want error", tt.url)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validateServiceURL(%q) = %v, want nil", tt.url, err)
			}
		})
	}
}

func TestValidTag(t *testing.T) {
	tests := []struct {
		name  string
		tag   string
		match bool
	}{
		{name: "semver tag", tag: "v2.0.1", match: true},
		{name: "latest", tag: "latest", match: true},
		{name: "version with alpine suffix", tag: "1.25-alpine", match: true},
		{name: "sha prefix", tag: "sha-abc123", match: true},
		{name: "empty string", tag: "", match: false},
		{name: "leading dot", tag: ".leading-dot", match: false},
		{name: "leading dash", tag: "-leading-dash", match: false},
		{name: "contains spaces", tag: "has spaces", match: false},
		{name: "contains colon", tag: "has:colon", match: false},
		{name: "129 chars exceeds limit", tag: "a" + strings.Repeat("b", 128), match: false},
		{name: "128 chars fits", tag: "a" + strings.Repeat("b", 127), match: true},
		{name: "underscores and dots", tag: "v1.0_build.123", match: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validTag.MatchString(tt.tag)
			if got != tt.match {
				t.Errorf("validTag.MatchString(%q) = %v, want %v", tt.tag, got, tt.match)
			}
		})
	}
}

func TestValidSettingKeys(t *testing.T) {
	// Verify known keys are in the allowlist.
	knownKeys := []string{
		// Scanning & scheduling.
		"poll_interval", "schedule", "grace_period", "paused",
		"scan_concurrency", "filters", "update_delay",
		// Update behaviour.
		"default_policy", "latest_auto_update", "image_cleanup",
		"image_backup", "remove_volumes", "dry_run", "pull_only",
		"rollback_policy", "version_scope", "dependency_aware",
		"compose_sync", "maintenance_window", "show_stopped",
		// Hooks.
		"hooks_enabled", "hooks_write_labels",
		// Notifications & digest.
		"notification_config", "notification_channels",
		"digest_enabled", "digest_time", "digest_interval",
		"default_notify_mode",
		// Webhook.
		"webhook_enabled", "webhook_secret",
		// Docker TLS.
		"docker_tls_ca", "docker_tls_cert", "docker_tls_key",
		// Cluster.
		"cluster_enabled", "cluster_port", "cluster_grace_period",
		"cluster_remote_policy", "cluster_auto_update_agents",
		// Instance.
		"instance_role", "auth_setup_complete", "auth_enabled",
		// OIDC.
		"oidc_enabled", "oidc_issuer_url", "oidc_client_id",
		"oidc_client_secret", "oidc_redirect_url", "oidc_auto_create",
		"oidc_default_role",
		// Agent/server.
		"server_addr", "enroll_token", "host_name",
		// HA discovery.
		"ha_discovery_enabled", "ha_discovery_prefix",
		// UI state.
		"stack_order", "digest_banner_dismissed", "dashboard_columns",
		// General (restart-required).
		"web_port", "tls_mode", "log_format",
	}
	for _, key := range knownKeys {
		if !validSettingKeys[key] {
			t.Errorf("expected %q to be in validSettingKeys", key)
		}
	}

	// Verify unknown keys are rejected.
	unknownKeys := []string{"admin_password", "db_path", "secret_key", ""}
	for _, key := range unknownKeys {
		if validSettingKeys[key] {
			t.Errorf("expected %q to NOT be in validSettingKeys", key)
		}
	}
}
