package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
)

// apiGetNotifications returns the current notification channels with secrets masked.
func (s *Server) apiGetNotifications(w http.ResponseWriter, r *http.Request) {
	if s.deps.NotifyConfig == nil {
		writeJSON(w, http.StatusOK, []notify.Channel{})
		return
	}

	channels, err := s.deps.NotifyConfig.GetNotificationChannels()
	if err != nil {
		s.deps.Log.Error("failed to load notification channels", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load notification channels")
		return
	}
	if channels == nil {
		channels = []notify.Channel{}
	}

	// Mask secrets for API response.
	masked := make([]notify.Channel, len(channels))
	for i, ch := range channels {
		masked[i] = notify.MaskSecrets(ch)
	}
	writeJSON(w, http.StatusOK, masked)
}

// apiSaveNotifications saves notification channels and reconfigures the notifier chain.
func (s *Server) apiSaveNotifications(w http.ResponseWriter, r *http.Request) {
	var channels []notify.Channel
	if err := json.NewDecoder(r.Body).Decode(&channels); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.deps.NotifyConfig == nil {
		writeError(w, http.StatusNotImplemented, "notification config not available")
		return
	}

	// Restore masked secrets from existing saved channels.
	existing, _ := s.deps.NotifyConfig.GetNotificationChannels()
	existingMap := make(map[string]notify.Channel)
	for _, ch := range existing {
		existingMap[ch.ID] = ch
	}
	for i, ch := range channels {
		if old, ok := existingMap[ch.ID]; ok {
			channels[i].Settings = restoreSecrets(ch, old)
		}
	}

	if err := s.deps.NotifyConfig.SetNotificationChannels(channels); err != nil {
		s.deps.Log.Error("failed to save notification channels", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save notification channels")
		return
	}

	// Rebuild notifier chain.
	if s.deps.NotifyReconfigurer != nil {
		var notifiers []notify.Notifier
		notifiers = append(notifiers, notify.NewLogNotifier(s.deps.Log))
		for _, ch := range channels {
			if !ch.Enabled {
				continue
			}
			n, err := notify.BuildFilteredNotifier(ch)
			if err != nil {
				s.deps.Log.Warn("failed to build notifier", "channel", ch.Name, "type", string(ch.Type), "error", err)
				continue
			}
			notifiers = append(notifiers, n)
		}
		s.deps.NotifyReconfigurer.Reconfigure(notifiers...)
	}

	s.logEvent(r, "settings", "", "Notification configuration updated")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "notification settings saved",
	})
}

// apiTestNotification sends a test event through the notification chain or a single channel.
func (s *Server) apiTestNotification(w http.ResponseWriter, r *http.Request) {
	if s.deps.NotifyReconfigurer == nil {
		writeError(w, http.StatusNotImplemented, "notifications not available")
		return
	}

	var body struct {
		ID string `json:"id"`
	}
	// Try to decode body -- if empty, test entire chain (backward compat).
	_ = json.NewDecoder(r.Body).Decode(&body)

	testEvent := notify.Event{
		Type:          notify.EventUpdateAvailable,
		ContainerName: "sentinel-test",
		OldImage:      "test:latest",
		Timestamp:     time.Now(),
	}

	if body.ID != "" && s.deps.NotifyConfig != nil {
		// Test single channel.
		channels, err := s.deps.NotifyConfig.GetNotificationChannels()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load channels")
			return
		}
		for _, ch := range channels {
			if ch.ID == body.ID {
				n, err := notify.BuildNotifier(ch)
				if err != nil {
					writeError(w, http.StatusBadRequest, "failed to build notifier: "+err.Error())
					return
				}
				if err := n.Send(r.Context(), testEvent); err != nil {
					writeError(w, http.StatusBadGateway, "test failed: "+err.Error())
					return
				}
				writeJSON(w, http.StatusOK, map[string]string{
					"status":  "ok",
					"message": "test notification sent to " + ch.Name,
				})
				return
			}
		}
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}

	// Test entire chain.
	if multi, ok := s.deps.NotifyReconfigurer.(*notify.Multi); ok {
		multi.Notify(r.Context(), testEvent)
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "test notification sent",
	})
}

// apiNotificationEventTypes returns the list of event types available for per-channel filtering.
func (s *Server) apiNotificationEventTypes(w http.ResponseWriter, _ *http.Request) {
	types := notify.AllEventTypes()
	result := make([]string, len(types))
	for i, t := range types {
		result[i] = string(t)
	}
	writeJSON(w, http.StatusOK, result)
}

// apiGetNotifyPref returns the notification preference for a container.
func (s *Server) apiGetNotifyPref(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}
	if s.deps.NotifyState == nil {
		writeJSON(w, http.StatusOK, map[string]string{"mode": "default"})
		return
	}
	pref, err := s.deps.NotifyState.GetNotifyPref(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load notification preference")
		return
	}
	if pref == nil {
		writeJSON(w, http.StatusOK, map[string]string{"mode": "default"})
		return
	}
	writeJSON(w, http.StatusOK, pref)
}

// apiSetNotifyPref sets the notification preference for a container.
func (s *Server) apiSetNotifyPref(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	switch body.Mode {
	case "default", "every_scan", "digest_only", "muted":
		// valid
	default:
		writeError(w, http.StatusBadRequest, "mode must be default, every_scan, digest_only, or muted")
		return
	}
	if s.deps.NotifyState == nil {
		writeError(w, http.StatusNotImplemented, "notification preferences not available")
		return
	}
	if body.Mode == "default" {
		// "default" means remove override — fall back to global setting.
		if err := s.deps.NotifyState.DeleteNotifyPref(name); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete notification preference")
			return
		}
	} else {
		if err := s.deps.NotifyState.SetNotifyPref(name, &NotifyPref{Mode: body.Mode}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save notification preference")
			return
		}
	}
	s.logEvent(r, "notify_pref", name, "Notification mode set to "+body.Mode)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "mode": body.Mode})
}

// apiClearAllNotifyStates resets all notification dedup states, allowing
// the next scan to re-trigger notifications for pending updates.
func (s *Server) apiClearAllNotifyStates(w http.ResponseWriter, r *http.Request) {
	if s.deps.NotifyState == nil {
		writeError(w, http.StatusNotImplemented, "notification state not available")
		return
	}
	states, err := s.deps.NotifyState.AllNotifyStates()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load notification states")
		return
	}
	cleared := 0
	for name := range states {
		if err := s.deps.NotifyState.ClearNotifyState(name); err == nil {
			cleared++
		}
	}
	s.logEvent(r, "notify_states_cleared", "", fmt.Sprintf("Cleared %d notification states", cleared))
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "cleared": cleared})
}

// apiGetDigestSettings returns the digest configuration.
func (s *Server) apiGetDigestSettings(w http.ResponseWriter, r *http.Request) {
	settings := map[string]string{
		"digest_enabled":      "true",
		"digest_time":         "09:00",
		"digest_interval":     "24h",
		"default_notify_mode": "default",
	}
	if s.deps.SettingsStore != nil {
		for key := range settings {
			if val, err := s.deps.SettingsStore.LoadSetting(key); err == nil && val != "" {
				settings[key] = val
			}
		}
	}
	writeJSON(w, http.StatusOK, settings)
}

// apiSaveDigestSettings saves digest configuration and reconfigures the scheduler.
func (s *Server) apiSaveDigestSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled           *bool  `json:"digest_enabled,omitempty"`
		Time              string `json:"digest_time,omitempty"`
		Interval          string `json:"digest_interval,omitempty"`
		DefaultNotifyMode string `json:"default_notify_mode,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	// Validate all fields before saving any.
	if body.Time != "" {
		if _, err := time.Parse("15:04", body.Time); err != nil {
			writeError(w, http.StatusBadRequest, "invalid time format, use HH:MM")
			return
		}
	}
	if body.Interval != "" {
		if _, err := time.ParseDuration(body.Interval); err != nil {
			writeError(w, http.StatusBadRequest, "invalid interval duration")
			return
		}
	}
	if body.DefaultNotifyMode != "" {
		switch body.DefaultNotifyMode {
		case "default", "every_scan", "digest_only", "muted":
			// valid
		default:
			writeError(w, http.StatusBadRequest, "invalid default_notify_mode")
			return
		}
	}

	// All valid — save atomically.
	if body.Enabled != nil {
		val := "true"
		if !*body.Enabled {
			val = "false"
		}
		if err := s.deps.SettingsStore.SaveSetting("digest_enabled", val); err != nil {
			s.deps.Log.Warn("failed to save digest setting", "key", "digest_enabled", "error", err)
		}
	}
	if body.Time != "" {
		if err := s.deps.SettingsStore.SaveSetting("digest_time", body.Time); err != nil {
			s.deps.Log.Warn("failed to save digest setting", "key", "digest_time", "error", err)
		}
	}
	if body.Interval != "" {
		if err := s.deps.SettingsStore.SaveSetting("digest_interval", body.Interval); err != nil {
			s.deps.Log.Warn("failed to save digest setting", "key", "digest_interval", "error", err)
		}
	}
	if body.DefaultNotifyMode != "" {
		if err := s.deps.SettingsStore.SaveSetting("default_notify_mode", body.DefaultNotifyMode); err != nil {
			s.deps.Log.Warn("failed to save digest setting", "key", "default_notify_mode", "error", err)
		}
	}

	// Signal digest scheduler to reconfigure.
	if s.deps.Digest != nil {
		s.deps.Digest.SetDigestConfig()
	}

	s.logEvent(r, "settings", "", "Digest settings updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "digest settings saved"})
}

// apiGetAllNotifyPrefs returns all per-container notification preferences.
func (s *Server) apiGetAllNotifyPrefs(w http.ResponseWriter, r *http.Request) {
	if s.deps.NotifyState == nil {
		writeJSON(w, http.StatusOK, map[string]*NotifyPref{})
		return
	}
	prefs, err := s.deps.NotifyState.AllNotifyPrefs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load notification preferences")
		return
	}
	// Convert to web types.
	result := make(map[string]map[string]string, len(prefs))
	for name, p := range prefs {
		result[name] = map[string]string{"mode": p.Mode}
	}
	writeJSON(w, http.StatusOK, result)
}

// apiTriggerDigest triggers an immediate digest notification.
func (s *Server) apiTriggerDigest(w http.ResponseWriter, r *http.Request) {
	if s.deps.Digest == nil {
		writeError(w, http.StatusNotImplemented, "digest scheduler not available")
		return
	}
	go s.deps.Digest.TriggerDigest(context.Background())
	s.logEvent(r, "digest", "", "Manual digest triggered")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "digest triggered"})
}

// apiGetDigestBanner returns pending update info for the dashboard banner.
func (s *Server) apiGetDigestBanner(w http.ResponseWriter, r *http.Request) {
	if s.deps.NotifyState == nil {
		writeJSON(w, http.StatusOK, map[string]any{"pending": []string{}, "count": 0})
		return
	}
	states, err := s.deps.NotifyState.AllNotifyStates()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load notification states")
		return
	}

	// Load dismissed state.
	var dismissed []string
	if s.deps.SettingsStore != nil {
		if val, loadErr := s.deps.SettingsStore.LoadSetting("digest_banner_dismissed"); loadErr == nil && val != "" {
			if err := json.Unmarshal([]byte(val), &dismissed); err != nil {
				s.deps.Log.Debug("failed to parse dismissed banners", "error", err)
			}
		}
	}
	dismissedSet := make(map[string]bool, len(dismissed))
	for _, d := range dismissed {
		dismissedSet[d] = true
	}

	var names []string
	for name, state := range states {
		key := name + "::" + state.LastDigest
		if !dismissedSet[key] {
			names = append(names, name)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pending": names,
		"count":   len(names),
	})
}

// apiDismissDigestBanner dismisses the digest banner for current updates.
func (s *Server) apiDismissDigestBanner(w http.ResponseWriter, r *http.Request) {
	if s.deps.NotifyState == nil || s.deps.SettingsStore == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	states, err := s.deps.NotifyState.AllNotifyStates()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load notification states")
		return
	}

	dismissed := make([]string, 0, len(states))
	for name, state := range states {
		dismissed = append(dismissed, name+"::"+state.LastDigest)
	}
	data, _ := json.Marshal(dismissed)
	if err := s.deps.SettingsStore.SaveSetting("digest_banner_dismissed", string(data)); err != nil {
		s.deps.Log.Warn("failed to save banner dismissed state", "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "banner dismissed"})
}
