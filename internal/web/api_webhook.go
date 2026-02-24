package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/webhook"
)

// apiWebhook handles inbound webhook triggers from Docker Hub, GHCR, or
// generic CI/CD pipelines. It uses its own secret-based authentication
// instead of the normal session/CSRF middleware.
func (s *Server) apiWebhook(w http.ResponseWriter, r *http.Request) {
	// Check if webhooks are enabled.
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusServiceUnavailable, "settings store not available")
		return
	}

	enabled, _ := s.deps.SettingsStore.LoadSetting(store.SettingWebhookEnabled)
	if enabled != "true" {
		writeError(w, http.StatusForbidden, "webhooks are disabled")
		return
	}

	// Validate webhook secret from query param or header.
	secret := r.URL.Query().Get("secret")
	if secret == "" {
		secret = r.Header.Get("X-Webhook-Secret")
	}

	storedSecret, _ := s.deps.SettingsStore.LoadSetting(store.SettingWebhookSecret)
	if storedSecret == "" || secret != storedSecret {
		writeError(w, http.StatusUnauthorized, "invalid or missing webhook secret")
		return
	}

	// Read body with a 1 MB limit.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	// Parse payload.
	payload, parseErr := webhook.Parse(body)

	// Build the log message and response.
	var logMsg string
	imageInfo := ""
	tagInfo := ""
	sourceInfo := "unknown"

	if parseErr != nil || payload == nil {
		// Parse failed — trigger a full scan as fallback.
		logMsg = "Webhook received (unparseable payload), triggering full scan"
	} else {
		sourceInfo = payload.Source
		imageInfo = payload.Image
		tagInfo = payload.Tag

		if payload.Image != "" {
			ref := payload.Image
			if payload.Tag != "" {
				ref += ":" + payload.Tag
			}
			logMsg = "Webhook received from " + payload.Source + ": " + ref
		} else {
			logMsg = "Webhook received from " + payload.Source + " (no image specified), triggering full scan"
		}
	}

	// Log the webhook event (use nil request to avoid extracting auth user,
	// since webhook requests are not session-authenticated).
	s.logEvent(nil, "webhook", imageInfo, logMsg)
	s.deps.Log.Info("webhook received",
		"source", sourceInfo,
		"image", imageInfo,
		"tag", tagInfo,
	)

	// Trigger scan.
	if s.deps.Scheduler != nil {
		go s.deps.Scheduler.TriggerScan(context.Background())
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "accepted",
		"image":  imageInfo,
		"tag":    tagInfo,
		"source": sourceInfo,
	})
}

// apiSetWebhookEnabled toggles the webhook endpoint on or off.
func (s *Server) apiSetWebhookEnabled(w http.ResponseWriter, r *http.Request) {
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

	if err := s.deps.SettingsStore.SaveSetting(store.SettingWebhookEnabled, value); err != nil {
		s.deps.Log.Error("failed to save webhook_enabled", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}

	// If enabling and no secret exists yet, auto-generate one.
	if body.Enabled {
		existing, _ := s.deps.SettingsStore.LoadSetting(store.SettingWebhookSecret)
		if existing == "" {
			secret, err := generateWebhookSecret()
			if err != nil {
				s.deps.Log.Error("failed to generate webhook secret", "error", err)
				writeError(w, http.StatusInternalServerError, "failed to generate webhook secret")
				return
			}
			if err := s.deps.SettingsStore.SaveSetting(store.SettingWebhookSecret, secret); err != nil {
				s.deps.Log.Error("failed to save webhook secret", "error", err)
				writeError(w, http.StatusInternalServerError, "failed to save webhook secret")
				return
			}
		}
	}

	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Inbound webhooks "+label)

	writeJSON(w, http.StatusOK, map[string]string{"message": "webhooks " + label})
}

// apiGenerateWebhookSecret creates a new random webhook secret, replacing any existing one.
func (s *Server) apiGenerateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	secret, err := generateWebhookSecret()
	if err != nil {
		s.deps.Log.Error("failed to generate webhook secret", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to generate secret")
		return
	}

	if err := s.deps.SettingsStore.SaveSetting(store.SettingWebhookSecret, secret); err != nil {
		s.deps.Log.Error("failed to save webhook secret", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save secret")
		return
	}

	s.logEvent(r, "settings", "", "Webhook secret regenerated")

	writeJSON(w, http.StatusOK, map[string]string{
		"secret": secret,
	})
}

// apiGetWebhookInfo returns the current webhook configuration for the settings page.
func (s *Server) apiGetWebhookInfo(w http.ResponseWriter, _ *http.Request) {
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	enabled, _ := s.deps.SettingsStore.LoadSetting(store.SettingWebhookEnabled)
	secret, _ := s.deps.SettingsStore.LoadSetting(store.SettingWebhookSecret)

	writeJSON(w, http.StatusOK, map[string]string{
		"enabled": enabled,
		"secret":  secret,
	})
}

// generateWebhookSecret produces a cryptographically random 32-byte hex string.
func generateWebhookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
