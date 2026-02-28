package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func (s *Server) handlePortainer(w http.ResponseWriter, r *http.Request) {
	data := pageData{Page: "portainer"}
	s.withAuth(r, &data)
	s.withCluster(&data)
	s.withPortainer(&data)
	s.renderTemplate(w, "portainer.html", data)
}

func (s *Server) apiPortainerEndpoints(w http.ResponseWriter, r *http.Request) {
	if s.deps.Portainer == nil {
		writeJSON(w, http.StatusOK, []PortainerEndpoint{})
		return
	}
	endpoints, err := s.deps.Portainer.AllEndpoints(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if endpoints == nil {
		endpoints = []PortainerEndpoint{}
	}
	writeJSON(w, http.StatusOK, endpoints)
}

func (s *Server) apiPortainerContainers(w http.ResponseWriter, r *http.Request) {
	if s.deps.Portainer == nil {
		writeJSON(w, http.StatusOK, []PortainerContainerInfo{})
		return
	}
	idStr := r.PathValue("id")
	endpointID, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}
	containers, err := s.deps.Portainer.EndpointContainers(r.Context(), endpointID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if containers == nil {
		containers = []PortainerContainerInfo{}
	}
	writeJSON(w, http.StatusOK, containers)
}

func (s *Server) apiSetPortainerEnabled(w http.ResponseWriter, r *http.Request) {
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
	if err := s.deps.SettingsStore.SaveSetting(store.SettingPortainerEnabled, val); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Portainer integration "+label)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) apiSetPortainerURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.URL = strings.TrimRight(body.URL, "/")
	if s.deps.SettingsStore != nil {
		if err := s.deps.SettingsStore.SaveSetting(store.SettingPortainerURL, body.URL); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	s.logEvent(r, "settings", "", "Portainer URL changed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) apiSetPortainerToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore != nil {
		if err := s.deps.SettingsStore.SaveSetting(store.SettingPortainerToken, body.Token); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	s.logEvent(r, "settings", "", "Portainer token updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) apiTestPortainerConnection(w http.ResponseWriter, r *http.Request) {
	if s.deps.Portainer == nil {
		writeError(w, http.StatusBadRequest, "Portainer not configured - save URL and token, then restart")
		return
	}
	if err := s.deps.Portainer.TestConnection(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	// Auto-enable on successful connection test.
	if s.deps.SettingsStore != nil {
		_ = s.deps.SettingsStore.SaveSetting(store.SettingPortainerEnabled, "true")
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
