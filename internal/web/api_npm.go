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
	if !body.Enabled {
		s.swapNPM(nil)
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
		if err := validateServiceURL(body.URL); err != nil {
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
	s.swapNPM(nil) // Test connection builds a new one from the saved URL
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
	s.swapNPM(nil) // Test connection builds a new one from the saved login
	s.logEvent(r, "settings", "", "NPM credentials updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) apiTestNPMConnection(w http.ResponseWriter, r *http.Request) {
	// Build the provider from the saved settings every time, so a changed URL
	// or login takes effect and the test is against what is saved, not the
	// target the running provider was built with.
	if s.deps.NPMInitFunc != nil {
		provider, err := s.deps.NPMInitFunc(r.Context())
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}
		s.swapNPM(provider)
	}
	npm := s.npmProvider()
	if npm == nil {
		writeError(w, http.StatusBadRequest, "NPM not available")
		return
	}
	if err := npm.TestConnection(r.Context()); err != nil {
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

// npmProvider returns the running NPM provider, or nil.
func (s *Server) npmProvider() NPMProvider {
	s.npmMu.RLock()
	defer s.npmMu.RUnlock()
	return s.deps.NPM
}

// swapNPM replaces the NPM provider and stops the one it replaces.
func (s *Server) swapNPM(p NPMProvider) {
	s.npmMu.Lock()
	old := s.deps.NPM
	s.deps.NPM = p
	s.npmMu.Unlock()
	if st, ok := old.(interface{ Stop() }); ok && old != p {
		st.Stop()
	}
}

func (s *Server) apiSyncNPM(w http.ResponseWriter, r *http.Request) {
	npm := s.npmProvider()
	if npm == nil {
		writeError(w, http.StatusBadRequest, "NPM not configured - test connection first")
		return
	}
	if err := npm.Sync(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"mappings": npm.AllMappings(),
	})
}

func (s *Server) apiGetNPMMappings(w http.ResponseWriter, r *http.Request) {
	npm := s.npmProvider()
	if npm == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"mappings":  map[string]interface{}{},
			"last_sync": nil,
		})
		return
	}
	// Grouped response: mappings keyed by ForwardHost, one table per host.
	if r.URL.Query().Get("grouped") == "true" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"grouped":   npm.AllMappingsGrouped(),
			"last_sync": npm.LastSync(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mappings":  npm.AllMappings(),
		"last_sync": npm.LastSync(),
	})
}

// --- Port config per-container ---

func (s *Server) apiGetPortConfig(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isValidContainerName(name) {
		writeError(w, http.StatusBadRequest, "invalid container name")
		return
	}
	hostID := r.URL.Query().Get("host")
	key := portConfigKey(hostID, name)
	if s.deps.PortConfigs == nil {
		writeJSON(w, http.StatusOK, &PortConfig{Ports: map[string]PortOverride{}})
		return
	}
	pc, err := s.deps.PortConfigs.GetPortConfig(key)
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
	if !isValidContainerName(name) {
		writeError(w, http.StatusBadRequest, "invalid container name")
		return
	}
	portStr := r.PathValue("port")
	hostID := r.URL.Query().Get("host")
	key := portConfigKey(hostID, name)
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
	if err := s.deps.PortConfigs.SetPortOverride(key, uint16(port), body); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logEvent(r, "port_config", name, "Port "+portStr+" URL override set")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) apiDeletePortOverride(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isValidContainerName(name) {
		writeError(w, http.StatusBadRequest, "invalid container name")
		return
	}
	portStr := r.PathValue("port")
	hostID := r.URL.Query().Get("host")
	key := portConfigKey(hostID, name)
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid port number")
		return
	}
	if s.deps.PortConfigs == nil {
		writeError(w, http.StatusInternalServerError, "port config store not available")
		return
	}
	if err := s.deps.PortConfigs.DeletePortOverride(key, uint16(port)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logEvent(r, "port_config", name, "Port "+portStr+" URL override removed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
