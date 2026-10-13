package web

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
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
	dbValues := map[string]string{}
	if s.deps.SettingsStore != nil {
		dbSettings, err := s.deps.SettingsStore.GetAllSettings()
		if err != nil {
			s.deps.Log.Warn("failed to load runtime settings", "error", err)
		} else {
			for k, v := range dbSettings {
				values[k] = v
			}
			dbValues = dbSettings
		}
	}

	// Environment values can carry credentials inside URLs.
	for k, v := range values {
		values[k] = sanitiseEnvValue(k, v)
	}

	// Never return a credential: the Viewer role and settings.view tokens can
	// read this endpoint. Report only whether each one is set.
	for k := range sensitiveKeys {
		if v, ok := values[k]; ok {
			delete(values, k)
			if v != "" {
				values[k+"_set"] = "true"
			}
		}
	}

	// These three are read by the engine from the runtime config. A value
	// saved in the UI replaces the environment default, so the config holds
	// the effective value; the source says which one is in effect.
	for _, k := range []string{"image_backup", "remove_volumes", "rollback_policy"} {
		source := "env"
		if _, saved := dbValues[k]; saved {
			source = "saved"
		}
		values[k+"_source"] = source
	}
	values["image_backup"] = strconv.FormatBool(s.deps.Config.ImageBackup())
	values["remove_volumes"] = strconv.FormatBool(s.deps.Config.RemoveVolumes())
	values["rollback_policy"] = s.deps.Config.RollbackPolicy()

	// The rest of the Scanning, Update and Process settings that the
	// environment can set. A saved value wins; otherwise the page gets the
	// environment's, so a form never shows blank or default for a value that
	// is really in effect, and saving it back changes nothing.
	envBacked := map[string]string{
		"poll_interval":      "SENTINEL_POLL_INTERVAL",
		"grace_period":       "SENTINEL_GRACE_PERIOD",
		"default_policy":     "SENTINEL_DEFAULT_POLICY",
		"schedule":           "SENTINEL_SCHEDULE",
		"show_stopped":       "SENTINEL_SHOW_STOPPED",
		"scan_concurrency":   "SENTINEL_SCAN_CONCURRENCY",
		"maintenance_window": "SENTINEL_MAINTENANCE_WINDOW",
	}
	envValues := s.deps.Config.Values()
	for key, envKey := range envBacked {
		source := "env"
		if v, ok := dbValues[key]; ok && v != "" {
			values[key] = v
			source = "saved"
		} else {
			values[key] = envValues[envKey]
		}
		values[key+"_source"] = source
	}

	// History retention: a saved value replaces SENTINEL_HISTORY_RETENTION_DAYS.
	retention, retentionSource := "90", "env"
	if v := s.deps.Config.Values()["SENTINEL_HISTORY_RETENTION_DAYS"]; v != "" {
		retention = v
	}
	if v, ok := dbValues["history_retention_days"]; ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			retention, retentionSource = strconv.Itoa(n), "saved"
		}
	}
	values["history_retention_days"] = retention
	values["history_retention_days_source"] = retentionSource

	// These toggles are only stored once saved in the UI, so the stored key
	// alone cannot show an env-var value. Report what the engine uses.
	cfg := s.deps.Config
	values["latest_auto_update"] = strconv.FormatBool(cfg.LatestAutoUpdate())
	values["image_cleanup"] = strconv.FormatBool(cfg.ImageCleanup())
	values["dependency_aware"] = strconv.FormatBool(cfg.DependencyAware())
	values["hooks_enabled"] = strconv.FormatBool(cfg.HooksEnabled())
	values["hooks_write_labels"] = strconv.FormatBool(cfg.HooksWriteLabels())

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

// apiSetVersionScope sets the global version scope at runtime.
// Scope controls how tag precision determines which updates are shown.
func (s *Server) apiSetVersionScope(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	switch body.Scope {
	case "", "default", "strict":
		// valid
	default:
		writeError(w, http.StatusBadRequest, "scope must be default or strict")
		return
	}

	// Normalise: "default" and "" both mean relaxed.
	if body.Scope == "" {
		body.Scope = "default"
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	if err := s.deps.SettingsStore.SaveSetting("version_scope", body.Scope); err != nil {
		s.deps.Log.Error("failed to save version scope", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save version scope")
		return
	}

	// Apply to the checker at runtime.
	if s.deps.VersionScope != nil {
		s.deps.VersionScope.SetDefaultScope(body.Scope)
	}

	msg := "Version scope set to " + body.Scope
	s.logEvent(r, "settings", "", msg)

	writeJSON(w, http.StatusOK, map[string]string{
		"message": msg,
	})
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
	// Validate duration format when non-empty.
	if req.Duration != "" {
		if _, err := time.ParseDuration(req.Duration); err != nil {
			writeError(w, http.StatusBadRequest, "invalid duration format: "+req.Duration)
			return
		}
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
		"web_port": true, "tls_mode": true, "log_format": true, "self_update_mode": true,
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
	case "self_update_mode":
		switch body.Value {
		case "manual", "auto":
		default:
			writeError(w, http.StatusBadRequest, "self_update_mode must be manual or auto")
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

// sanitiseEnvValue removes credentials from an environment-sourced value
// that is a URL: userinfo, query and fragment always, and the path too
// unless the setting is a plain service address (SENTINEL_NPM_URL). Webhook
// and Gotify URLs often carry the secret in the path, so they keep only the
// scheme and host.
func sanitiseEnvValue(key, v string) string {
	if !strings.HasPrefix(key, "SENTINEL_") || !strings.Contains(v, "://") {
		return v
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return "****"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	if key != "SENTINEL_NPM_URL" {
		u.Path = ""
		u.RawPath = ""
	}
	return u.String()
}

// apiSetHistoryRetention sets how many days of history and activity log are
// kept. 0 keeps everything. The daily prune reads the saved value each time,
// so no restart is needed.
func (s *Server) apiSetHistoryRetention(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Days int `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Days < 0 || body.Days > 3650 {
		writeError(w, http.StatusBadRequest, "days must be between 0 (keep everything) and 3650")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	if err := s.deps.SettingsStore.SaveSetting("history_retention_days", strconv.Itoa(body.Days)); err != nil {
		s.deps.Log.Error("failed to save history retention", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	msg := "History kept for " + strconv.Itoa(body.Days) + " days"
	if body.Days == 0 {
		msg = "History kept forever"
	}
	s.logEvent(r, "settings", "", msg)
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}
