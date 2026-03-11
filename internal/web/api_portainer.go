package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// handlePortainer renders the Portainer connectors page.
func (s *Server) handlePortainer(w http.ResponseWriter, r *http.Request) {
	data := pageData{Page: "portainer"}
	s.withAuth(r, &data)
	s.withCluster(&data)
	s.withPortainer(&data)
	s.renderTemplate(w, "portainer.html", data)
}

// ---------------------------------------------------------------------------
// Instance CRUD
// ---------------------------------------------------------------------------

// apiListPortainerInstances returns all configured Portainer instances
// with tokens redacted.
func (s *Server) apiListPortainerInstances(w http.ResponseWriter, _ *http.Request) {
	if s.deps.PortainerInstances == nil {
		writeJSON(w, http.StatusOK, []PortainerInstanceConfig{})
		return
	}
	instances, err := s.deps.PortainerInstances.ListPortainerInstances()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list instances: "+err.Error())
		return
	}
	// Redact tokens before sending to the client.
	for i := range instances {
		if instances[i].Token != "" {
			instances[i].Token = "***"
		}
	}
	if instances == nil {
		instances = []PortainerInstanceConfig{}
	}
	writeJSON(w, http.StatusOK, instances)
}

// apiCreatePortainerInstance adds a new Portainer instance.
func (s *Server) apiCreatePortainerInstance(w http.ResponseWriter, r *http.Request) {
	if s.deps.PortainerInstances == nil {
		writeError(w, http.StatusInternalServerError, "instance store not available")
		return
	}
	var body struct {
		Name  string `json:"name"`
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.URL = strings.TrimRight(body.URL, "/")
	if body.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	if err := validateServiceURL(body.URL); err != nil {
		writeError(w, http.StatusBadRequest, "invalid URL: "+err.Error())
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	id, err := s.deps.PortainerInstances.NextPortainerID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate ID: "+err.Error())
		return
	}

	inst := PortainerInstanceConfig{
		ID:      id,
		Name:    body.Name,
		URL:     body.URL,
		Token:   body.Token,
		Enabled: true,
	}
	if err := s.deps.PortainerInstances.SavePortainerInstance(inst); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save instance: "+err.Error())
		return
	}

	// Create a live scanner for the new instance.
	if s.deps.Portainer != nil && inst.Token != "" && inst.URL != "" {
		_ = s.deps.Portainer.ConnectInstance(inst.ID, inst.URL, inst.Token)
	}

	s.logEvent(r, "settings", "", "Portainer instance created: "+body.Name)
	// Return with redacted token.
	if inst.Token != "" {
		inst.Token = "***"
	}
	writeJSON(w, http.StatusCreated, inst)
}

// apiUpdatePortainerInstance updates an existing Portainer instance.
func (s *Server) apiUpdatePortainerInstance(w http.ResponseWriter, r *http.Request) {
	if s.deps.PortainerInstances == nil {
		writeError(w, http.StatusInternalServerError, "instance store not available")
		return
	}
	id := r.PathValue("id")
	existing, err := s.deps.PortainerInstances.GetPortainerInstance(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "instance not found: "+err.Error())
		return
	}

	var body struct {
		Name    *string `json:"name"`
		URL     *string `json:"url"`
		Token   *string `json:"token"`
		Enabled *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if body.Name != nil {
		existing.Name = *body.Name
	}
	if body.URL != nil {
		u := strings.TrimRight(*body.URL, "/")
		if u != "" {
			if err := validateServiceURL(u); err != nil {
				writeError(w, http.StatusBadRequest, "invalid URL: "+err.Error())
				return
			}
		}
		existing.URL = u
	}
	if body.Token != nil {
		existing.Token = *body.Token
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}

	if err := s.deps.PortainerInstances.SavePortainerInstance(existing); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save instance: "+err.Error())
		return
	}

	// Reconnect scanner with updated credentials.
	if s.deps.Portainer != nil && existing.Token != "" && existing.URL != "" && existing.Enabled {
		_ = s.deps.Portainer.ConnectInstance(existing.ID, existing.URL, existing.Token)
	} else if s.deps.Portainer != nil && !existing.Enabled {
		s.deps.Portainer.DisconnectInstance(existing.ID)
	}

	s.logEvent(r, "settings", "", "Portainer instance updated: "+existing.Name)
	if existing.Token != "" {
		existing.Token = "***"
	}
	writeJSON(w, http.StatusOK, existing)
}

// apiDeletePortainerInstance removes a Portainer instance.
func (s *Server) apiDeletePortainerInstance(w http.ResponseWriter, r *http.Request) {
	if s.deps.PortainerInstances == nil {
		writeError(w, http.StatusInternalServerError, "instance store not available")
		return
	}
	id := r.PathValue("id")

	// Verify it exists first for a meaningful log message.
	inst, err := s.deps.PortainerInstances.GetPortainerInstance(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "instance not found: "+err.Error())
		return
	}

	if err := s.deps.PortainerInstances.DeletePortainerInstance(id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete instance: "+err.Error())
		return
	}

	// Remove the live scanner.
	if s.deps.Portainer != nil {
		s.deps.Portainer.DisconnectInstance(id)
	}

	s.logEvent(r, "settings", "", "Portainer instance deleted: "+inst.Name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// apiTestPortainerInstance tests connectivity and populates endpoint list.
func (s *Server) apiTestPortainerInstance(w http.ResponseWriter, r *http.Request) {
	if s.deps.PortainerInstances == nil || s.deps.Portainer == nil {
		writeError(w, http.StatusBadRequest, "Portainer not available")
		return
	}
	id := r.PathValue("id")

	inst, err := s.deps.PortainerInstances.GetPortainerInstance(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "instance not found: "+err.Error())
		return
	}

	// Test connectivity through the provider.
	if err := s.deps.Portainer.TestConnection(r.Context(), id); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// On success, fetch all endpoints to populate the instance config.
	endpoints, err := s.deps.Portainer.AllEndpoints(r.Context(), id)
	if err != nil {
		// Connection worked but listing failed; still report success with a warning.
		writeJSON(w, http.StatusOK, map[string]any{
			"success":   true,
			"warning":   "connected but failed to list endpoints: " + err.Error(),
			"endpoints": []PortainerEndpoint{},
		})
		return
	}

	// Populate endpoint config for newly discovered endpoints.
	if inst.Endpoints == nil {
		inst.Endpoints = make(map[string]EndpointCfg)
	}
	for _, ep := range endpoints {
		epKey := strconv.Itoa(ep.ID)
		if _, exists := inst.Endpoints[epKey]; !exists {
			inst.Endpoints[epKey] = EndpointCfg{Enabled: true}
		}
	}

	// Auto-enable on successful test.
	inst.Enabled = true
	_ = s.deps.PortainerInstances.SavePortainerInstance(inst)

	s.logEvent(r, "settings", "", "Portainer instance tested successfully: "+inst.Name)
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"endpoints": endpoints,
	})
}

// apiPortainerInstanceEndpoints returns all endpoints for a specific instance.
func (s *Server) apiPortainerInstanceEndpoints(w http.ResponseWriter, r *http.Request) {
	if s.deps.Portainer == nil {
		writeJSON(w, http.StatusOK, []PortainerEndpoint{})
		return
	}
	id := r.PathValue("id")
	endpoints, err := s.deps.Portainer.AllEndpoints(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if endpoints == nil {
		endpoints = []PortainerEndpoint{}
	}
	writeJSON(w, http.StatusOK, endpoints)
}

// apiUpdatePortainerEndpoint toggles an endpoint's enabled state within an instance.
func (s *Server) apiUpdatePortainerEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.deps.PortainerInstances == nil {
		writeError(w, http.StatusInternalServerError, "instance store not available")
		return
	}
	id := r.PathValue("id")
	epIDStr := r.PathValue("epid")

	inst, err := s.deps.PortainerInstances.GetPortainerInstance(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "instance not found: "+err.Error())
		return
	}

	var body struct {
		Enabled *bool   `json:"enabled"`
		Blocked *bool   `json:"blocked"`
		Reason  *string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if inst.Endpoints == nil {
		inst.Endpoints = make(map[string]EndpointCfg)
	}
	cfg := inst.Endpoints[epIDStr]
	if body.Enabled != nil {
		cfg.Enabled = *body.Enabled
	}
	if body.Blocked != nil {
		cfg.Blocked = *body.Blocked
	}
	if body.Reason != nil {
		cfg.Reason = *body.Reason
	}
	inst.Endpoints[epIDStr] = cfg

	if err := s.deps.PortainerInstances.SavePortainerInstance(inst); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save instance: "+err.Error())
		return
	}

	s.logEvent(r, "settings", "", "Portainer endpoint "+epIDStr+" updated on instance "+inst.Name)
	writeJSON(w, http.StatusOK, cfg)
}

// apiPortainerContainers returns containers for a specific endpoint.
// Accepts instance_id as a query parameter to route to the correct Portainer instance.
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

	instanceID := r.URL.Query().Get("instance_id")
	containers, err := s.deps.Portainer.EndpointContainers(r.Context(), instanceID, endpointID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if containers == nil {
		containers = []PortainerContainerInfo{}
	}
	writeJSON(w, http.StatusOK, containers)
}
