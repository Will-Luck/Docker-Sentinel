package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
)

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

// apiSetNotifyBatchWindow configures the notification batching window.
// When set to a non-zero duration, rapid-fire notifications during bulk updates
// are buffered and sent as a single summary instead of N individual alerts.
func (s *Server) apiSetNotifyBatchWindow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Window string `json:"window"` // duration string e.g. "30s", "2m", "0" to disable
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Parse and validate. Empty or "0" means disabled.
	var d time.Duration
	if body.Window != "" && body.Window != "0" {
		var err error
		d, err = time.ParseDuration(body.Window)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid duration: "+err.Error())
			return
		}
		if d < 0 {
			writeError(w, http.StatusBadRequest, "batch window must be non-negative")
			return
		}
		if d > 10*time.Minute {
			writeError(w, http.StatusBadRequest, "batch window must not exceed 10 minutes")
			return
		}
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}
	val := d.String()
	if d == 0 {
		val = "0"
	}
	if err := s.deps.SettingsStore.SaveSetting("notification_batch_window", val); err != nil {
		s.deps.Log.Error("failed to save notification_batch_window", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	if s.deps.NotifyReconfigurer != nil {
		s.deps.NotifyReconfigurer.SetBatchWindow(d)
	}

	label := "disabled"
	if d > 0 {
		label = val
	}
	s.logEvent(r, "settings", "", "Notification batch window set to "+label)
	writeJSON(w, http.StatusOK, map[string]string{"message": "notification batch window set to " + label})
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Home Assistant discovery " + label})
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

	// Load group mapping configuration.
	groupClaim := load("oidc_group_claim")
	if groupClaim == "" {
		groupClaim = "groups"
	}
	var groupMappings map[string]string
	if raw := load("oidc_group_mappings"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &groupMappings)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":        load("oidc_enabled") == "true",
		"issuer_url":     load("oidc_issuer_url"),
		"client_id":      load("oidc_client_id"),
		"client_secret":  maskedSecret,
		"redirect_url":   load("oidc_redirect_url"),
		"auto_create":    load("oidc_auto_create") == "true",
		"default_role":   load("oidc_default_role"),
		"group_claim":    groupClaim,
		"group_mappings": groupMappings,
	})
}

// apiSaveOIDCSettings saves OIDC configuration and reinitialises the provider.
func (s *Server) apiSaveOIDCSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	var req struct {
		Enabled       bool              `json:"enabled"`
		IssuerURL     string            `json:"issuer_url"`
		ClientID      string            `json:"client_id"`
		ClientSecret  string            `json:"client_secret"`
		RedirectURL   string            `json:"redirect_url"`
		AutoCreate    bool              `json:"auto_create"`
		DefaultRole   string            `json:"default_role"`
		GroupClaim    string            `json:"group_claim"`
		GroupMappings map[string]string `json:"group_mappings"`
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

	// Serialise group mappings for storage.
	groupMappingsJSON := ""
	if len(req.GroupMappings) > 0 {
		if data, err := json.Marshal(req.GroupMappings); err == nil {
			groupMappingsJSON = string(data)
		}
	}
	groupClaim := req.GroupClaim
	if groupClaim == "" {
		groupClaim = "groups"
	}

	pairs := []struct{ key, val string }{
		{"oidc_enabled", enabledVal},
		{"oidc_issuer_url", req.IssuerURL},
		{"oidc_client_id", req.ClientID},
		{"oidc_client_secret", req.ClientSecret},
		{"oidc_redirect_url", req.RedirectURL},
		{"oidc_auto_create", autoCreateVal},
		{"oidc_default_role", req.DefaultRole},
		{"oidc_group_claim", groupClaim},
		{"oidc_group_mappings", groupMappingsJSON},
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
			Enabled:       true,
			IssuerURL:     req.IssuerURL,
			ClientID:      req.ClientID,
			ClientSecret:  req.ClientSecret,
			RedirectURL:   req.RedirectURL,
			AutoCreate:    req.AutoCreate,
			DefaultRole:   req.DefaultRole,
			GroupClaim:    groupClaim,
			GroupMappings: req.GroupMappings,
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

// apiSetDashboardColumns saves the user's preferred dashboard column visibility and order.
func (s *Server) apiSetDashboardColumns(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Columns []string `json:"columns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	allowed := map[string]bool{"image": true, "policy": true, "status": true, "ports": true}
	for _, col := range body.Columns {
		if !allowed[col] {
			writeError(w, http.StatusBadRequest, "unknown column: "+col)
			return
		}
	}

	data, err := json.Marshal(body.Columns)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode columns")
		return
	}

	if err := s.deps.SettingsStore.SaveSetting("dashboard_columns", string(data)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save column config")
		return
	}

	s.logEvent(r, "settings-change", "", "Dashboard columns updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
