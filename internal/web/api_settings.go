package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
)

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
