package web

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"

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
