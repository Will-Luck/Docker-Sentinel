package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
)

// ConfigExport is the top-level structure for a full configuration export.
type ConfigExport struct {
	Version       string               `json:"version"`
	ExportedAt    string               `json:"exported_at"`
	Settings      map[string]string    `json:"settings"`
	Notifications []notify.Channel     `json:"notifications"`
	Registries    []RegistryCredential `json:"registries"`
}

// ConfigImportResult summarises what was imported.
type ConfigImportResult struct {
	Message       string   `json:"message"`
	Settings      int      `json:"settings_imported"`
	Notifications int      `json:"notifications_imported"`
	Registries    int      `json:"registries_imported"`
	Skipped       int      `json:"redacted_skipped"`
	Warnings      []string `json:"warnings,omitempty"`
}

// redactedPlaceholder is the value used to mask secrets in exports.
const redactedPlaceholder = "***REDACTED***"

// sensitiveKeys is an explicit whitelist of setting keys that hold secrets.
// Using a whitelist instead of substring matching avoids false positives
// (e.g. docker_tls_key is a file path, not a secret).
var sensitiveKeys = map[string]bool{
	"portainer_token":       true,
	"webhook_secret":        true,
	"notification_config":   true, // contains provider credentials
	"notification_channels": true, // channel settings may contain tokens
	"oidc_client_secret":    true,
}

// validSettingKeys is an allowlist of all setting keys that may be stored.
// The config import handler rejects any key not in this set to prevent
// arbitrary writes to the settings store.
var validSettingKeys = map[string]bool{
	// Scanning & scheduling.
	"poll_interval":    true,
	"schedule":         true,
	"grace_period":     true,
	"paused":           true,
	"scan_concurrency": true,
	"filters":          true,
	"update_delay":     true,

	// Update behaviour.
	"default_policy":     true,
	"latest_auto_update": true,
	"image_cleanup":      true,
	"image_backup":       true,
	"remove_volumes":     true,
	"dry_run":            true,
	"pull_only":          true,
	"rollback_policy":    true,
	"version_scope":      true,
	"dependency_aware":   true,
	"compose_sync":       true,
	"maintenance_window": true,
	"show_stopped":       true,

	// Hooks.
	"hooks_enabled":      true,
	"hooks_write_labels": true,

	// Notifications & digest.
	"notification_config":   true,
	"notification_channels": true,
	"digest_enabled":        true,
	"digest_time":           true,
	"digest_interval":       true,
	"default_notify_mode":   true,

	// Webhook.
	"webhook_enabled": true,
	"webhook_secret":  true,

	// Portainer.
	"portainer_enabled": true,
	"portainer_url":     true,
	"portainer_token":   true,

	// Docker TLS.
	"docker_tls_ca":   true,
	"docker_tls_cert": true,
	"docker_tls_key":  true,

	// Cluster.
	"cluster_enabled":            true,
	"cluster_port":               true,
	"cluster_grace_period":       true,
	"cluster_remote_policy":      true,
	"cluster_auto_update_agents": true,

	// Instance.
	"instance_role":       true,
	"auth_setup_complete": true,
	"auth_enabled":        true,

	// OIDC.
	"oidc_enabled":       true,
	"oidc_issuer_url":    true,
	"oidc_client_id":     true,
	"oidc_client_secret": true,
	"oidc_redirect_url":  true,
	"oidc_auto_create":   true,
	"oidc_default_role":  true,

	// Agent/server.
	"server_addr":  true,
	"enroll_token": true,
	"host_name":    true,

	// HA discovery.
	"ha_discovery_enabled": true,
	"ha_discovery_prefix":  true,

	// UI state.
	"stack_order":             true,
	"digest_banner_dismissed": true,

	// General (restart-required).
	"web_port":   true,
	"tls_mode":   true,
	"log_format": true,
}

// isSensitiveSetting returns true if the key holds a secret value.
func isSensitiveSetting(key string) bool {
	return sensitiveKeys[key]
}

// apiConfigExport builds a full configuration backup and sends it as a
// downloadable JSON file.
func (s *Server) apiConfigExport(w http.ResponseWriter, r *http.Request) {
	includeSecrets := r.URL.Query().Get("secrets") == "true"

	export := ConfigExport{
		Version:    "1",
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// --- Settings ---
	if s.deps.SettingsStore != nil {
		settings, err := s.deps.SettingsStore.GetAllSettings()
		if err != nil {
			s.deps.Log.Error("config export: failed to load settings", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load settings")
			return
		}
		if !includeSecrets {
			for k, v := range settings {
				if isSensitiveSetting(k) && v != "" {
					settings[k] = redactedPlaceholder
				}
			}
		}
		export.Settings = settings
	}
	if export.Settings == nil {
		export.Settings = make(map[string]string)
	}

	// --- Notification channels ---
	if s.deps.NotifyConfig != nil {
		channels, err := s.deps.NotifyConfig.GetNotificationChannels()
		if err != nil {
			s.deps.Log.Error("config export: failed to load notification channels", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load notification channels")
			return
		}
		if !includeSecrets {
			masked := make([]notify.Channel, len(channels))
			for i, ch := range channels {
				masked[i] = notify.MaskSecrets(ch)
			}
			channels = masked
		}
		if channels == nil {
			channels = []notify.Channel{}
		}
		export.Notifications = channels
	} else {
		export.Notifications = []notify.Channel{}
	}

	// --- Registry credentials ---
	if s.deps.RegistryCredentials != nil {
		creds, err := s.deps.RegistryCredentials.GetRegistryCredentials()
		if err != nil {
			s.deps.Log.Error("config export: failed to load registry credentials", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load registry credentials")
			return
		}
		if !includeSecrets {
			for i := range creds {
				if creds[i].Secret != "" {
					creds[i].Secret = redactedPlaceholder
				}
			}
		}
		if creds == nil {
			creds = []RegistryCredential{}
		}
		export.Registries = creds
	} else {
		export.Registries = []RegistryCredential{}
	}

	// Marshal with indentation for readability.
	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to serialise config")
		return
	}

	filename := fmt.Sprintf("sentinel-config-%s.json", time.Now().UTC().Format("20060102"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// apiConfigImport reads a JSON config backup and merges it into the current state.
func (s *Server) apiConfigImport(w http.ResponseWriter, r *http.Request) {
	// Limit upload to 5 MB.
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body (max 5 MB)")
		return
	}

	var imported ConfigExport
	if err := json.Unmarshal(body, &imported); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Validate structure and version.
	if imported.Version == "" {
		writeError(w, http.StatusBadRequest, "missing 'version' field — not a valid Sentinel config export")
		return
	}
	if imported.Version != "1" {
		writeError(w, http.StatusBadRequest, "unsupported config version: "+imported.Version+" (this instance supports version 1)")
		return
	}

	result := ConfigImportResult{}

	// --- Settings ---
	if s.deps.SettingsStore != nil && len(imported.Settings) > 0 {
		for k, v := range imported.Settings {
			if v == redactedPlaceholder {
				result.Skipped++
				continue
			}
			if !validSettingKeys[k] {
				result.Warnings = append(result.Warnings, "unknown setting key rejected: "+k)
				continue
			}
			if err := s.deps.SettingsStore.SaveSetting(k, v); err != nil {
				s.deps.Log.Warn("config import: failed to save setting", "key", k, "error", err)
				continue
			}
			result.Settings++
		}

		// Apply key in-memory settings so changes take effect immediately.
		s.applyImportedSettings(imported.Settings)
	}

	// --- Notification channels ---
	if s.deps.NotifyConfig != nil && len(imported.Notifications) > 0 {
		if err := s.deps.NotifyConfig.SetNotificationChannels(imported.Notifications); err != nil {
			s.deps.Log.Error("config import: failed to save notification channels", "error", err)
			result.Warnings = append(result.Warnings, "failed to import notification channels: "+err.Error())
		} else {
			result.Notifications = len(imported.Notifications)

			// Rebuild the live notification chain (same pattern as apiSaveNotifications).
			if s.deps.NotifyReconfigurer != nil {
				var notifiers []notify.Notifier
				notifiers = append(notifiers, notify.NewLogNotifier(s.deps.Log))
				for _, ch := range imported.Notifications {
					if !ch.Enabled {
						continue
					}
					n, buildErr := notify.BuildFilteredNotifier(ch)
					if buildErr != nil {
						s.deps.Log.Warn("config import: failed to build notifier", "channel", ch.Name, "error", buildErr)
						continue
					}
					notifiers = append(notifiers, n)
				}
				s.deps.NotifyReconfigurer.Reconfigure(notifiers...)
			}
		}
	}

	// --- Registry credentials ---
	if s.deps.RegistryCredentials != nil && len(imported.Registries) > 0 {
		// Filter out any registries where the secret is redacted.
		clean := make([]RegistryCredential, 0, len(imported.Registries))
		for _, c := range imported.Registries {
			if c.Secret == redactedPlaceholder {
				result.Skipped++
				continue
			}
			clean = append(clean, c)
		}

		if len(clean) > 0 {
			// Merge with existing: replace matching registries, keep others.
			existing, _ := s.deps.RegistryCredentials.GetRegistryCredentials()
			merged := mergeRegistries(existing, clean)
			if err := s.deps.RegistryCredentials.SetRegistryCredentials(merged); err != nil {
				s.deps.Log.Error("config import: failed to save registry credentials", "error", err)
			} else {
				result.Registries = len(clean)
			}
		}
	}

	s.logEvent(r, "config-import", "", fmt.Sprintf(
		"Configuration imported: %d settings, %d notification channels, %d registries (%d redacted values skipped)",
		result.Settings, result.Notifications, result.Registries, result.Skipped,
	))

	result.Message = fmt.Sprintf(
		"Imported %d settings, %d notification channels, %d registry credentials",
		result.Settings, result.Notifications, result.Registries,
	)
	if result.Skipped > 0 {
		result.Message += fmt.Sprintf(" (%d redacted values skipped)", result.Skipped)
	}

	writeJSON(w, http.StatusOK, result)
}

// applyImportedSettings pushes key settings into the running in-memory config
// so they take effect without a restart.
func (s *Server) applyImportedSettings(settings map[string]string) {
	if s.deps.ConfigWriter == nil {
		return
	}

	if v, ok := settings["default_policy"]; ok && v != redactedPlaceholder {
		s.deps.ConfigWriter.SetDefaultPolicy(v)
	}

	if v, ok := settings["grace_period"]; ok && v != redactedPlaceholder {
		if d, err := time.ParseDuration(v); err == nil {
			s.deps.ConfigWriter.SetGracePeriod(d)
		}
	}

	if v, ok := settings["latest_auto_update"]; ok && v != redactedPlaceholder {
		s.deps.ConfigWriter.SetLatestAutoUpdate(v == "true")
	}

	if v, ok := settings["image_cleanup"]; ok && v != redactedPlaceholder {
		s.deps.ConfigWriter.SetImageCleanup(v == "true")
	}

	if v, ok := settings["hooks_enabled"]; ok && v != redactedPlaceholder {
		s.deps.ConfigWriter.SetHooksEnabled(v == "true")
	}

	if v, ok := settings["hooks_write_labels"]; ok && v != redactedPlaceholder {
		s.deps.ConfigWriter.SetHooksWriteLabels(v == "true")
	}

	if v, ok := settings["dependency_aware"]; ok && v != redactedPlaceholder {
		s.deps.ConfigWriter.SetDependencyAware(v == "true")
	}

	if v, ok := settings["rollback_policy"]; ok && v != redactedPlaceholder {
		s.deps.ConfigWriter.SetRollbackPolicy(v)
	}

	if v, ok := settings["version_scope"]; ok && v != redactedPlaceholder {
		if s.deps.VersionScope != nil {
			s.deps.VersionScope.SetDefaultScope(v)
		}
	}

	if v, ok := settings["remove_volumes"]; ok && v != redactedPlaceholder {
		s.deps.ConfigWriter.SetRemoveVolumes(v == "true")
	}

	if v, ok := settings["scan_concurrency"]; ok && v != redactedPlaceholder {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 1 {
			s.deps.ConfigWriter.SetScanConcurrency(n)
		}
	}

	// Poll interval needs the scheduler.
	if v, ok := settings["poll_interval"]; ok && v != redactedPlaceholder {
		if d, err := time.ParseDuration(v); err == nil && s.deps.Scheduler != nil {
			s.deps.Scheduler.SetPollInterval(d)
		}
	}

	// Schedule also needs the scheduler.
	if v, ok := settings["schedule"]; ok && v != redactedPlaceholder {
		if s.deps.Scheduler != nil {
			s.deps.Scheduler.SetSchedule(v)
		}
	}
}

// mergeRegistries combines existing and imported registries. Imported entries
// replace existing ones with the same ID; existing entries without a matching
// import are preserved.
func mergeRegistries(existing, imported []RegistryCredential) []RegistryCredential {
	byID := make(map[string]RegistryCredential, len(existing))
	var order []string
	for _, c := range existing {
		byID[c.ID] = c
		order = append(order, c.ID)
	}
	for _, c := range imported {
		if _, exists := byID[c.ID]; !exists {
			order = append(order, c.ID)
		}
		byID[c.ID] = c
	}
	result := make([]RegistryCredential, 0, len(order))
	for _, id := range order {
		result = append(result, byID[id])
	}
	return result
}
