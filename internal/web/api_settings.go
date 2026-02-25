package web

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

//go:embed grafana-dashboard.json
var grafanaDashboard []byte

func (s *Server) apiGrafanaDashboard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="sentinel-grafana-dashboard.json"`)
	_, _ = w.Write(grafanaDashboard)
}

// apiSettings returns the current configuration values, merged with runtime overrides from BoltDB.
func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) {
	values := s.deps.Config.Values()

	// Overlay runtime settings from BoltDB (these take precedence over env config).
	if s.deps.SettingsStore != nil {
		dbSettings, err := s.deps.SettingsStore.GetAllSettings()
		if err != nil {
			s.deps.Log.Warn("failed to load runtime settings", "error", err)
		} else {
			for k, v := range dbSettings {
				values[k] = v
			}
		}
	}

	writeJSON(w, http.StatusOK, values)
}

// apiSetPollInterval updates the poll interval at runtime.
func (s *Server) apiSetPollInterval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Interval string `json:"interval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	d, err := time.ParseDuration(body.Interval)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid duration format: "+body.Interval)
		return
	}

	if d < 5*time.Minute {
		writeError(w, http.StatusBadRequest, "poll interval must be at least 5 minutes")
		return
	}
	if d > 24*time.Hour {
		writeError(w, http.StatusBadRequest, "poll interval must be at most 24 hours")
		return
	}

	if s.deps.Scheduler != nil {
		s.deps.Scheduler.SetPollInterval(d)
	}

	// Persist to BoltDB.
	if s.deps.SettingsStore != nil {
		if err := s.deps.SettingsStore.SaveSetting("poll_interval", d.String()); err != nil {
			s.deps.Log.Warn("failed to persist poll interval setting", "error", err)
		}
	}

	s.logEvent(r, "settings", "", "Poll interval changed to "+d.String())

	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"interval": d.String(),
		"message":  "poll interval updated to " + d.String(),
	})
}

// apiSetGracePeriod sets the grace period at runtime.
func (s *Server) apiSetGracePeriod(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Duration string `json:"duration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	d, err := time.ParseDuration(body.Duration)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid duration format: "+body.Duration)
		return
	}

	if d < 0 {
		writeError(w, http.StatusBadRequest, "grace period must not be negative")
		return
	}
	if d > 10*time.Minute {
		writeError(w, http.StatusBadRequest, "grace period must be at most 10 minutes")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	if err := s.deps.SettingsStore.SaveSetting("grace_period", d.String()); err != nil {
		s.deps.Log.Error("failed to save grace period", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save grace period")
		return
	}

	// Apply to in-memory config so the engine uses the new value immediately.
	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetGracePeriod(d)
	}

	s.logEvent(r, "settings", "", "Grace period changed to "+d.String())

	writeJSON(w, http.StatusOK, map[string]string{
		"message":  "grace period set to " + d.String(),
		"duration": d.String(),
	})
}

// apiSetPause pauses or unpauses the scan scheduler.
func (s *Server) apiSetPause(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Paused bool `json:"paused"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	value := "false"
	if body.Paused {
		value = "true"
	}

	if err := s.deps.SettingsStore.SaveSetting("paused", value); err != nil {
		s.deps.Log.Error("failed to save pause state", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save pause state")
		return
	}

	action := "unpaused"
	if body.Paused {
		action = "paused"
	}
	s.logEvent(r, "settings", "", "Scheduler "+action)

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "scheduler " + action,
		"paused":  value,
	})
}

// apiSetLatestAutoUpdate enables/disables auto-update for :latest containers.
func (s *Server) apiSetLatestAutoUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	value := "false"
	if body.Enabled {
		value = "true"
	}

	if err := s.deps.SettingsStore.SaveSetting("latest_auto_update", value); err != nil {
		s.deps.Log.Error("failed to save latest_auto_update", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}

	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetLatestAutoUpdate(body.Enabled)
	}

	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Auto-update :latest containers "+label)

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "latest auto-update " + label,
	})
}

// apiSetFilters sets container name filter patterns for scan exclusion.
func (s *Server) apiSetFilters(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Patterns []string `json:"patterns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	value := strings.Join(body.Patterns, "\n")

	if err := s.deps.SettingsStore.SaveSetting("filters", value); err != nil {
		s.deps.Log.Error("failed to save filters", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save filters")
		return
	}

	s.logEvent(r, "settings", "", "Scan filters updated")

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "filters updated",
	})
}

// apiSetImageCleanup toggles old image cleanup.
func (s *Server) apiSetImageCleanup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	value := "false"
	if body.Enabled {
		value = "true"
	}
	if err := s.deps.SettingsStore.SaveSetting("image_cleanup", value); err != nil {
		s.deps.Log.Error("failed to save image_cleanup", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetImageCleanup(body.Enabled)
	}
	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Image cleanup "+label)
	writeJSON(w, http.StatusOK, map[string]string{"message": "image cleanup " + label})
}

// apiSetSchedule sets a cron schedule expression.
func (s *Server) apiSetSchedule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Schedule string `json:"schedule"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// Validate cron expression (empty = disable cron, use poll interval).
	if body.Schedule != "" {
		parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(body.Schedule); err != nil {
			writeError(w, http.StatusBadRequest, "invalid cron expression: "+err.Error())
			return
		}
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	if err := s.deps.SettingsStore.SaveSetting("schedule", body.Schedule); err != nil {
		s.deps.Log.Error("failed to save schedule", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	if s.deps.Scheduler != nil {
		s.deps.Scheduler.SetSchedule(body.Schedule)
	}
	msg := "Schedule cleared (using poll interval)"
	if body.Schedule != "" {
		msg = "Schedule set to " + body.Schedule
	}
	s.logEvent(r, "settings", "", msg)
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// apiSetHooksEnabled toggles lifecycle hooks.
func (s *Server) apiSetHooksEnabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	value := "false"
	if body.Enabled {
		value = "true"
	}
	if err := s.deps.SettingsStore.SaveSetting("hooks_enabled", value); err != nil {
		s.deps.Log.Error("failed to save hooks_enabled", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetHooksEnabled(body.Enabled)
	}
	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Lifecycle hooks "+label)
	writeJSON(w, http.StatusOK, map[string]string{"message": "lifecycle hooks " + label})
}

// apiSetHooksWriteLabels toggles hook label writing.
func (s *Server) apiSetHooksWriteLabels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	value := "false"
	if body.Enabled {
		value = "true"
	}
	if err := s.deps.SettingsStore.SaveSetting("hooks_write_labels", value); err != nil {
		s.deps.Log.Error("failed to save hooks_write_labels", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetHooksWriteLabels(body.Enabled)
	}
	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Hook label writing "+label)
	writeJSON(w, http.StatusOK, map[string]string{"message": "hook label writing " + label})
}

// apiSetDependencyAware toggles dependency-aware updates.
func (s *Server) apiSetDependencyAware(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	value := "false"
	if body.Enabled {
		value = "true"
	}
	if err := s.deps.SettingsStore.SaveSetting("dependency_aware", value); err != nil {
		s.deps.Log.Error("failed to save dependency_aware", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetDependencyAware(body.Enabled)
	}
	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Dependency-aware updates "+label)
	writeJSON(w, http.StatusOK, map[string]string{"message": "dependency-aware updates " + label})
}

// apiSetRollbackPolicy sets the automatic policy change on rollback.
func (s *Server) apiSetRollbackPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Policy string `json:"policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	switch body.Policy {
	case "", "manual", "pinned":
		// valid
	default:
		writeError(w, http.StatusBadRequest, "rollback policy must be empty, manual, or pinned")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	if err := s.deps.SettingsStore.SaveSetting("rollback_policy", body.Policy); err != nil {
		s.deps.Log.Error("failed to save rollback policy", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save rollback policy")
		return
	}

	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetRollbackPolicy(body.Policy)
	}

	msg := "Rollback policy: no change"
	if body.Policy != "" {
		msg = "Rollback policy: set to " + body.Policy
	}
	s.logEvent(r, "settings", "", msg)

	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// apiSetDryRun enables or disables dry-run mode.
// When enabled, updates are detected and notified but never executed.
func (s *Server) apiSetDryRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	value := "false"
	if body.Enabled {
		value = "true"
	}

	if err := s.deps.SettingsStore.SaveSetting("dry_run", value); err != nil {
		s.deps.Log.Error("failed to save dry_run", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}

	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Dry-run mode "+label)
	writeJSON(w, http.StatusOK, map[string]string{"message": "dry-run mode " + label})
}

// apiSetUpdateDelay sets the global update delay. Updates are only applied
// after the delay has elapsed since first detection.
func (s *Server) apiSetUpdateDelay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Duration string `json:"duration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	if err := s.deps.SettingsStore.SaveSetting("update_delay", req.Duration); err != nil {
		s.deps.Log.Error("failed to save update_delay", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	msg := "Update delay cleared"
	if req.Duration != "" {
		msg = "Update delay set to " + req.Duration
	}
	s.logEvent(r, "settings", "", msg)
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// apiSetPullOnly enables or disables pull-only mode.
// When enabled, the new image is pulled but containers are not restarted.
func (s *Server) apiSetPullOnly(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	value := "false"
	if body.Enabled {
		value = "true"
	}

	if err := s.deps.SettingsStore.SaveSetting("pull_only", value); err != nil {
		s.deps.Log.Error("failed to save pull_only", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}

	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Pull-only mode "+label)
	writeJSON(w, http.StatusOK, map[string]string{"message": "pull-only mode " + label})
}

// apiClusterSettings returns the current cluster configuration with defaults
// for any keys not yet saved to BoltDB.
func (s *Server) apiClusterSettings(w http.ResponseWriter, _ *http.Request) {
	result := map[string]string{
		"enabled":       "false",
		"port":          "9443",
		"grace_period":  "30m",
		"remote_policy": "manual",
	}

	if s.deps.SettingsStore != nil {
		keys := map[string]string{
			"enabled":       store.SettingClusterEnabled,
			"port":          store.SettingClusterPort,
			"grace_period":  store.SettingClusterGracePeriod,
			"remote_policy": store.SettingClusterRemotePolicy,
		}
		for field, dbKey := range keys {
			if v, err := s.deps.SettingsStore.LoadSetting(dbKey); err == nil && v != "" {
				result[field] = v
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// apiClusterSettingsSave validates and persists cluster configuration changes.
// When the enabled state changes, it calls the ClusterLifecycle callback to
// start or stop the gRPC server dynamically.
func (s *Server) apiClusterSettingsSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled      *bool  `json:"enabled"`
		Port         string `json:"port"`
		GracePeriod  string `json:"grace_period"`
		RemotePolicy string `json:"remote_policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusInternalServerError, "settings store not available")
		return
	}

	// Validate port: must be an integer in 1024-65535.
	if req.Port != "" {
		p, err := strconv.Atoi(req.Port)
		if err != nil || p < 1024 || p > 65535 {
			writeError(w, http.StatusBadRequest, "port must be 1024-65535")
			return
		}
	}

	// Validate grace period against whitelist.
	if req.GracePeriod != "" {
		allowed := map[string]bool{"5m": true, "15m": true, "30m": true, "1h": true, "2h": true}
		if !allowed[req.GracePeriod] {
			writeError(w, http.StatusBadRequest, "invalid grace period")
			return
		}
	}

	// Validate remote policy against whitelist.
	if req.RemotePolicy != "" {
		switch req.RemotePolicy {
		case "auto", "manual", "pinned":
			// valid
		default:
			writeError(w, http.StatusBadRequest, "invalid remote policy — must be auto, manual, or pinned")
			return
		}
	}

	// Save each provided field, checking for errors.
	if req.Enabled != nil {
		val := "false"
		if *req.Enabled {
			val = "true"
		}
		if err := s.deps.SettingsStore.SaveSetting(store.SettingClusterEnabled, val); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	if req.Port != "" {
		if err := s.deps.SettingsStore.SaveSetting(store.SettingClusterPort, req.Port); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	if req.GracePeriod != "" {
		if err := s.deps.SettingsStore.SaveSetting(store.SettingClusterGracePeriod, req.GracePeriod); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	if req.RemotePolicy != "" {
		if err := s.deps.SettingsStore.SaveSetting(store.SettingClusterRemotePolicy, req.RemotePolicy); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}

	// Dynamic start/stop via ClusterLifecycle callback.
	if req.Enabled != nil && s.clusterLifecycle != nil {
		if *req.Enabled {
			if err := s.clusterLifecycle.Start(); err != nil {
				// Rollback: save disabled state since start failed.
				if err := s.deps.SettingsStore.SaveSetting(store.SettingClusterEnabled, "false"); err != nil {
					s.deps.Log.Warn("failed to rollback cluster enabled setting", "error", err)
				}
				writeError(w, http.StatusInternalServerError, "failed to start cluster: "+err.Error())
				return
			}
		} else {
			s.clusterLifecycle.Stop()
		}
	}

	s.logEvent(r, "cluster-settings", "", "Cluster settings updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) apiSaveGeneralSetting(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}

	allowed := map[string]bool{
		"web_port": true, "tls_mode": true, "log_format": true,
	}
	if !allowed[body.Key] {
		writeError(w, http.StatusBadRequest, "unknown setting: "+body.Key)
		return
	}

	switch body.Key {
	case "web_port":
		p, err := strconv.Atoi(body.Value)
		if err != nil || p < 1 || p > 65535 {
			writeError(w, http.StatusBadRequest, "invalid port number")
			return
		}
	case "tls_mode":
		switch body.Value {
		case "off", "auto", "manual":
		default:
			writeError(w, http.StatusBadRequest, "tls_mode must be off, auto, or manual")
			return
		}
	case "log_format":
		switch body.Value {
		case "json", "text":
		default:
			writeError(w, http.StatusBadRequest, "log_format must be json or text")
			return
		}
	}

	if err := s.deps.SettingsStore.SaveSetting(body.Key, body.Value); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}

	s.logEvent(r, "settings", "", "General setting changed: "+body.Key+"="+body.Value)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "restart_required": "true"})
}

func (s *Server) apiSwitchRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Role != "server" && body.Role != "agent" {
		writeError(w, http.StatusBadRequest, "role must be 'server' or 'agent'")
		return
	}

	if err := s.deps.SettingsStore.SaveSetting("instance_role", body.Role); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save role")
		return
	}

	s.logEvent(r, "settings", "", "Instance role changed to "+body.Role)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "restart_required": "true"})
}

// apiSetComposeSync enables or disables Docker Compose file sync after updates.
func (s *Server) apiSetComposeSync(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	value := "false"
	if body.Enabled {
		value = "true"
	}
	if err := s.deps.SettingsStore.SaveSetting("compose_sync", value); err != nil {
		s.deps.Log.Error("failed to save compose_sync", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Compose file sync "+label)
	writeJSON(w, http.StatusOK, map[string]string{"message": "compose file sync " + label})
}

// apiSetShowStopped enables or disables showing stopped containers in the dashboard.
func (s *Server) apiSetShowStopped(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	value := "false"
	if body.Enabled {
		value = "true"
	}
	if err := s.deps.SettingsStore.SaveSetting("show_stopped", value); err != nil {
		s.deps.Log.Error("failed to save show_stopped", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Show stopped containers "+label)
	writeJSON(w, http.StatusOK, map[string]string{"message": "show stopped containers " + label})
}

// apiSetImageBackup enables or disables image retag backup before updates.
func (s *Server) apiSetImageBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	value := "false"
	if body.Enabled {
		value = "true"
	}
	if err := s.deps.SettingsStore.SaveSetting("image_backup", value); err != nil {
		s.deps.Log.Error("failed to save image_backup", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Image backup "+label)
	writeJSON(w, http.StatusOK, map[string]string{"message": "image backup " + label})
}

// apiSetRemoveVolumes enables or disables anonymous volume removal during updates.
func (s *Server) apiSetRemoveVolumes(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	value := "false"
	if body.Enabled {
		value = "true"
	}
	if err := s.deps.SettingsStore.SaveSetting("remove_volumes", value); err != nil {
		s.deps.Log.Error("failed to save remove_volumes", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetRemoveVolumes(body.Enabled)
	}
	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Remove volumes on update "+label)
	writeJSON(w, http.StatusOK, map[string]string{"message": "remove volumes on update " + label})
}

// apiSetScanConcurrency sets the number of parallel registry checks during a scan.
func (s *Server) apiSetScanConcurrency(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Concurrency int `json:"concurrency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Concurrency < 1 || body.Concurrency > 20 {
		writeError(w, http.StatusBadRequest, "concurrency must be between 1 and 20")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	val := strconv.Itoa(body.Concurrency)
	if err := s.deps.SettingsStore.SaveSetting("scan_concurrency", val); err != nil {
		s.deps.Log.Error("failed to save scan_concurrency", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetScanConcurrency(body.Concurrency)
	}
	s.logEvent(r, "settings", "", "Scan concurrency set to "+val)
	writeJSON(w, http.StatusOK, map[string]string{"message": "scan concurrency set to " + val})
}

// apiSetHADiscovery enables or disables Home Assistant MQTT auto-discovery.
func (s *Server) apiSetHADiscovery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool   `json:"enabled"`
		Prefix  string `json:"prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	value := "false"
	if req.Enabled {
		value = "true"
	}
	if err := s.deps.SettingsStore.SaveSetting("ha_discovery_enabled", value); err != nil {
		s.deps.Log.Error("failed to save ha_discovery_enabled", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	if req.Prefix != "" {
		if err := s.deps.SettingsStore.SaveSetting("ha_discovery_prefix", req.Prefix); err != nil {
			s.deps.Log.Warn("failed to save ha_discovery_prefix", "error", err)
		}
	}
	label := "disabled"
	if req.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Home Assistant discovery "+label)
	w.WriteHeader(http.StatusNoContent)
}

// apiSetDockerTLS saves Docker TLS certificate paths for mTLS connections.
// All three paths must be provided together, or all empty to disable.
func (s *Server) apiSetDockerTLS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CA   string `json:"ca"`
		Cert string `json:"cert"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusInternalServerError, "settings store not available")
		return
	}

	// Trim whitespace from paths.
	req.CA = strings.TrimSpace(req.CA)
	req.Cert = strings.TrimSpace(req.Cert)
	req.Key = strings.TrimSpace(req.Key)

	// All-or-nothing: either all three are provided, or all are empty.
	has := req.CA != "" || req.Cert != "" || req.Key != ""
	complete := req.CA != "" && req.Cert != "" && req.Key != ""
	if has && !complete {
		writeError(w, http.StatusBadRequest, "all three certificate paths must be provided, or leave all empty to disable")
		return
	}

	// Validate that files exist when paths are provided.
	if complete {
		for label, path := range map[string]string{"CA certificate": req.CA, "client certificate": req.Cert, "client key": req.Key} {
			if _, err := os.Stat(path); err != nil {
				writeError(w, http.StatusBadRequest, label+" not found: "+path)
				return
			}
		}
	}

	// Save all three settings atomically — roll back on partial failure to
	// prevent inconsistent TLS config (e.g. CA saved but cert/key missing).
	tlsPairs := []struct{ key, val string }{
		{store.SettingDockerTLSCA, req.CA},
		{store.SettingDockerTLSCert, req.Cert},
		{store.SettingDockerTLSKey, req.Key},
	}
	for _, p := range tlsPairs {
		if err := s.deps.SettingsStore.SaveSetting(p.key, p.val); err != nil {
			// Roll back: clear all three to avoid partial config.
			for _, rb := range tlsPairs {
				_ = s.deps.SettingsStore.SaveSetting(rb.key, "")
			}
			writeError(w, http.StatusInternalServerError, "failed to save TLS config — rolled back")
			return
		}
	}

	msg := "Docker TLS certificates cleared"
	if complete {
		msg = "Docker TLS certificates saved"
	}
	s.logEvent(r, "settings", "", msg)
	writeJSON(w, http.StatusOK, map[string]string{
		"message":          msg,
		"restart_required": "true",
	})
}

// apiSetMaintenanceWindow sets the maintenance window expression for auto-updates.
func (s *Server) apiSetMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Validate the expression if non-empty.
	if req.Value != "" {
		if _, err := engine.ParseWindow(req.Value); err != nil {
			writeError(w, http.StatusBadRequest, "invalid maintenance window: "+err.Error())
			return
		}
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	if err := s.deps.SettingsStore.SaveSetting("maintenance_window", req.Value); err != nil {
		s.deps.Log.Error("failed to save maintenance_window", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}

	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetMaintenanceWindow(req.Value)
	}

	msg := "Maintenance window cleared"
	if req.Value != "" {
		msg = "Maintenance window set to " + req.Value
	}
	s.logEvent(r, "settings", "", msg)
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// apiTestDockerTLS attempts to connect to the Docker daemon using the provided TLS certificates.
func (s *Server) apiTestDockerTLS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CA   string `json:"ca"`
		Cert string `json:"cert"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	req.CA = strings.TrimSpace(req.CA)
	req.Cert = strings.TrimSpace(req.Cert)
	req.Key = strings.TrimSpace(req.Key)

	if req.CA == "" || req.Cert == "" || req.Key == "" {
		writeError(w, http.StatusBadRequest, "all three certificate paths are required for testing")
		return
	}

	// Read the Docker host from config values.
	dockerHost := "/var/run/docker.sock"
	if vals := s.deps.Config.Values(); vals["SENTINEL_DOCKER_SOCK"] != "" {
		dockerHost = vals["SENTINEL_DOCKER_SOCK"]
	}

	// TLS only applies to TCP connections — testing against a Unix socket is meaningless.
	if !strings.HasPrefix(dockerHost, "tcp://") && !strings.HasPrefix(dockerHost, "tcps://") {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   "TLS certificates require a TCP Docker host (tcp:// or tcps://). Current host is a Unix socket.",
		})
		return
	}

	tlsCfg := &docker.TLSConfig{CACert: req.CA, ClientCert: req.Cert, ClientKey: req.Key}
	testClient, err := docker.NewClient(dockerHost, tlsCfg)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   "failed to create client: " + err.Error(),
		})
		return
	}
	defer testClient.Close()

	if err := testClient.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   "Docker ping failed: " + err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// apiGetOIDCSettings returns the current OIDC configuration (client_secret masked).
func (s *Server) apiGetOIDCSettings(w http.ResponseWriter, _ *http.Request) {
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	load := func(key string) string {
		v, _ := s.deps.SettingsStore.LoadSetting(key)
		return v
	}

	secret := load("oidc_client_secret")
	maskedSecret := ""
	if secret != "" {
		if len(secret) > 8 {
			maskedSecret = secret[:4] + "****" + secret[len(secret)-4:]
		} else {
			maskedSecret = "****"
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":       load("oidc_enabled") == "true",
		"issuer_url":    load("oidc_issuer_url"),
		"client_id":     load("oidc_client_id"),
		"client_secret": maskedSecret,
		"redirect_url":  load("oidc_redirect_url"),
		"auto_create":   load("oidc_auto_create") == "true",
		"default_role":  load("oidc_default_role"),
	})
}

// apiSaveOIDCSettings saves OIDC configuration and reinitialises the provider.
func (s *Server) apiSaveOIDCSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	var req struct {
		Enabled      bool   `json:"enabled"`
		IssuerURL    string `json:"issuer_url"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RedirectURL  string `json:"redirect_url"`
		AutoCreate   bool   `json:"auto_create"`
		DefaultRole  string `json:"default_role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate role.
	if req.DefaultRole != "" {
		switch req.DefaultRole {
		case "admin", "operator", "viewer":
			// valid
		default:
			writeError(w, http.StatusBadRequest, "invalid default role")
			return
		}
	}

	enabledVal := "false"
	if req.Enabled {
		enabledVal = "true"
	}
	autoCreateVal := "false"
	if req.AutoCreate {
		autoCreateVal = "true"
	}

	// If client_secret contains the masked pattern, preserve the existing secret.
	if strings.Contains(req.ClientSecret, "****") {
		existing, _ := s.deps.SettingsStore.LoadSetting("oidc_client_secret")
		req.ClientSecret = existing
	}

	pairs := []struct{ key, val string }{
		{"oidc_enabled", enabledVal},
		{"oidc_issuer_url", req.IssuerURL},
		{"oidc_client_id", req.ClientID},
		{"oidc_client_secret", req.ClientSecret},
		{"oidc_redirect_url", req.RedirectURL},
		{"oidc_auto_create", autoCreateVal},
		{"oidc_default_role", req.DefaultRole},
	}

	for _, p := range pairs {
		if err := s.deps.SettingsStore.SaveSetting(p.key, p.val); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save OIDC settings")
			return
		}
	}

	// Reinitialise the OIDC provider with the new settings.
	if req.Enabled && req.IssuerURL != "" && req.ClientID != "" {
		oidcCfg := auth.OIDCConfig{
			Enabled:      true,
			IssuerURL:    req.IssuerURL,
			ClientID:     req.ClientID,
			ClientSecret: req.ClientSecret,
			RedirectURL:  req.RedirectURL,
			AutoCreate:   req.AutoCreate,
			DefaultRole:  req.DefaultRole,
		}
		provider, err := auth.NewOIDCProvider(r.Context(), oidcCfg)
		if err != nil {
			s.deps.Log.Warn("OIDC provider reinitialisation failed", "error", err)
			writeJSON(w, http.StatusOK, map[string]any{
				"status":  "saved_with_warning",
				"warning": "Settings saved but OIDC provider failed to initialise: " + err.Error(),
			})
			return
		}
		s.SetOIDCProvider(provider)
	} else {
		// Disabled or incomplete — clear the provider.
		s.SetOIDCProvider(nil)
	}

	s.logEvent(r, "settings", "", "OIDC settings updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
