package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func (s *Server) handleConnectors(w http.ResponseWriter, r *http.Request) {
	data := pageData{Page: "connectors"}
	s.withAuth(r, &data)
	s.withCluster(&data)
	s.withPortainer(&data)
	s.renderTemplate(w, "connectors.html", data)
}

func (s *Server) apiSetNPMEnabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusInternalServerError, "settings store not available")
		return
	}
	val := "false"
	if body.Enabled {
		val = "true"
	}
	if err := s.deps.SettingsStore.SaveSetting(store.SettingNPMEnabled, val); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "NPM integration "+label)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) apiSetNPMURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.URL = strings.TrimRight(body.URL, "/")
	if body.URL != "" {
		if err := validateExternalURL(body.URL); err != nil {
			writeError(w, http.StatusBadRequest, "invalid NPM URL: "+err.Error())
			return
		}
	}
	if s.deps.SettingsStore != nil {
		if err := s.deps.SettingsStore.SaveSetting(store.SettingNPMURL, body.URL); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	s.logEvent(r, "settings", "", "NPM URL changed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) apiSetNPMCredentials(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore != nil {
		if err := s.deps.SettingsStore.SaveSetting(store.SettingNPMEmail, body.Email); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save email")
			return
		}
		if err := s.deps.SettingsStore.SaveSetting(store.SettingNPMPassword, body.Password); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save password")
			return
		}
	}
	s.logEvent(r, "settings", "", "NPM credentials updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) apiTestNPMConnection(w http.ResponseWriter, r *http.Request) {
	if s.deps.NPM == nil {
		writeError(w, http.StatusBadRequest, "NPM not configured - save URL and credentials, then restart")
		return
	}
	if err := s.deps.NPM.TestConnection(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	if s.deps.SettingsStore != nil {
		_ = s.deps.SettingsStore.SaveSetting(store.SettingNPMEnabled, "true")
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *Server) apiSyncNPM(w http.ResponseWriter, r *http.Request) {
	if s.deps.NPM == nil {
		writeError(w, http.StatusBadRequest, "NPM not configured")
		return
	}
	if err := s.deps.NPM.Sync(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"mappings": s.deps.NPM.AllMappings(),
	})
}

func (s *Server) apiGetNPMMappings(w http.ResponseWriter, r *http.Request) {
	if s.deps.NPM == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"mappings":  map[string]interface{}{},
			"last_sync": nil,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mappings":  s.deps.NPM.AllMappings(),
		"last_sync": s.deps.NPM.LastSync(),
	})
}

// --- Port config per-container ---

func (s *Server) apiGetPortConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.deps.PortConfigs == nil {
		writeJSON(w, http.StatusOK, &PortConfig{Ports: map[string]PortOverride{}})
		return
	}
	pc, err := s.deps.PortConfigs.GetPortConfig(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pc == nil {
		pc = &PortConfig{Ports: map[string]PortOverride{}}
	}
	writeJSON(w, http.StatusOK, pc)
}

func (s *Server) apiSetPortOverride(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	portStr := r.PathValue("port")
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid port number")
		return
	}
	var body PortOverride
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.PortConfigs == nil {
		writeError(w, http.StatusInternalServerError, "port config store not available")
		return
	}
	if err := s.deps.PortConfigs.SetPortOverride(name, uint16(port), body); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logEvent(r, "port_config", name, "Port "+portStr+" URL override set")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) apiDeletePortOverride(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	portStr := r.PathValue("port")
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid port number")
		return
	}
	if s.deps.PortConfigs == nil {
		writeError(w, http.StatusInternalServerError, "port config store not available")
		return
	}
	if err := s.deps.PortConfigs.DeletePortOverride(name, uint16(port)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logEvent(r, "port_config", name, "Port "+portStr+" URL override removed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
