package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// apiRetrySettings returns the current notification retry configuration.
func (s *Server) apiRetrySettings(w http.ResponseWriter, _ *http.Request) {
	result := map[string]string{
		"count":   "0",
		"backoff": "2s",
	}

	if s.deps.SettingsStore != nil {
		if v, err := s.deps.SettingsStore.LoadSetting(store.SettingNotifyRetryCount); err == nil && v != "" {
			result["count"] = v
		}
		if v, err := s.deps.SettingsStore.LoadSetting(store.SettingNotifyRetryBackoff); err == nil && v != "" {
			result["backoff"] = v
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// apiRetrySettingsSave persists notification retry configuration changes.
func (s *Server) apiRetrySettingsSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Count   string `json:"count"`
		Backoff string `json:"backoff"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusInternalServerError, "settings store not available")
		return
	}

	// Validate and save retry count (0-3).
	count, err := strconv.Atoi(req.Count)
	if err != nil || count < 0 || count > 3 {
		writeError(w, http.StatusBadRequest, "retry count must be 0-3")
		return
	}
	if err := s.deps.SettingsStore.SaveSetting(store.SettingNotifyRetryCount, req.Count); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}

	// Validate and save backoff duration.
	backoff, err := time.ParseDuration(req.Backoff)
	if err != nil || backoff < 0 {
		writeError(w, http.StatusBadRequest, "invalid backoff duration")
		return
	}
	if err := s.deps.SettingsStore.SaveSetting(store.SettingNotifyRetryBackoff, req.Backoff); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}

	// Apply to the live notifier.
	if s.deps.NotifyReconfigurer != nil {
		s.deps.NotifyReconfigurer.SetRetry(count, backoff)
	}

	s.logEvent(r, "settings", "", "Notification retry settings updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}
