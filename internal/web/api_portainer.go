package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
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
	// Auto-block local socket endpoints only when the Portainer instance
	// is on the same host as Sentinel (otherwise unix:// just means
	// "local to that Portainer", which is a valid remote Docker host).
	sameHost := isLocalPortainerInstance(inst.URL)
	if inst.Endpoints == nil {
		inst.Endpoints = make(map[string]EndpointCfg)
	}

	// Collect Engine IDs from each endpoint for source deduplication.
	endpointEngineIDs := make(map[int]string)
	if s.deps.Portainer != nil {
		for _, ep := range endpoints {
			eid, err := s.deps.Portainer.EndpointEngineID(r.Context(), id, ep.ID)
			if err != nil {
				s.deps.Log.Warn("failed to get engine ID for endpoint",
					"instance", id, "endpoint", ep.ID, "error", err)
				continue
			}
			endpointEngineIDs[ep.ID] = eid
		}
	}

	var overlapBlocked []string
	for _, ep := range endpoints {
		epKey := strconv.Itoa(ep.ID)
		cfg, found := inst.Endpoints[epKey]
		isNew := !found
		// Always update Engine ID if we got one.
		if eid, ok := endpointEngineIDs[ep.ID]; ok {
			cfg.EngineID = eid
		}
		if isNew {
			cfg.Enabled = true
		}
		// Auto-block: local socket on same host (existing logic).
		if isNew && sameHost && isLocalSocketEndpoint(ep) {
			cfg.Enabled = false
			cfg.Blocked = true
			cfg.Reason = "local Docker socket (duplicates direct monitoring)"
		}
		// Auto-block: Engine ID overlap with higher-priority source.
		if !cfg.ForceAllow {
			if overlap := s.findEngineOverlap(cfg.EngineID); overlap != nil {
				cfg.Blocked = true
				cfg.Enabled = false
				cfg.Reason = fmt.Sprintf("monitored by %s (%s)", overlap.Type, overlap.Name)
				overlapBlocked = append(overlapBlocked, fmt.Sprintf("%s (endpoint %s)", ep.Name, epKey))
			}
		}
		inst.Endpoints[epKey] = cfg
	}

	// Notify dashboard about auto-blocked endpoints.
	if len(overlapBlocked) > 0 {
		s.deps.EventBus.Publish(events.SSEEvent{
			Type:      events.EventSourceOverlap,
			Message:   fmt.Sprintf("auto-blocked %d endpoint(s) due to source overlap: %s", len(overlapBlocked), strings.Join(overlapBlocked, ", ")),
			Timestamp: time.Now(),
		})
	}

	// Auto-enable on successful test.
	inst.Enabled = true
	_ = s.deps.PortainerInstances.SavePortainerInstance(inst)

	// Reconnect so the engine picks up the updated endpoint config.
	if s.deps.Portainer != nil && inst.Token != "" && inst.URL != "" {
		_ = s.deps.Portainer.ConnectInstance(inst.ID, inst.URL, inst.Token)
	}

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
		Enabled    *bool   `json:"enabled"`
		Blocked    *bool   `json:"blocked"`
		Reason     *string `json:"reason"`
		ForceAllow *bool   `json:"force_allow"`
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
	if body.ForceAllow != nil {
		cfg.ForceAllow = *body.ForceAllow
		if *body.ForceAllow {
			// User is overriding the auto-block.
			cfg.Blocked = false
			cfg.Reason = ""
			cfg.Enabled = true
		}
	}
	inst.Endpoints[epIDStr] = cfg

	if err := s.deps.PortainerInstances.SavePortainerInstance(inst); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save instance: "+err.Error())
		return
	}

	// Refresh engine's endpoint config so scan respects the change.
	if s.deps.Portainer != nil && inst.Token != "" && inst.URL != "" {
		_ = s.deps.Portainer.ConnectInstance(inst.ID, inst.URL, inst.Token)
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

// overlapSource describes a higher-priority source monitoring the same Docker daemon.
type overlapSource struct {
	Type string // "local", "cluster"
	Name string // human-readable name (e.g. "this host", "test-server-2")
}

// findEngineOverlap checks whether the given Engine ID is already monitored
// by a higher-priority source (local > cluster > Portainer).
// Returns nil if no overlap is found.
func (s *Server) findEngineOverlap(engineID string) *overlapSource {
	if engineID == "" {
		return nil
	}

	// Check local Engine ID.
	if s.deps.SettingsStore != nil {
		if localEID, err := s.deps.SettingsStore.LoadSetting("local_engine_id"); err == nil && localEID == engineID {
			return &overlapSource{Type: "local", Name: "this host"}
		}
	}

	// Check cluster agent Engine IDs.
	if s.deps.Cluster != nil {
		for _, host := range s.deps.Cluster.AllHosts() {
			if host.EngineID == engineID {
				return &overlapSource{Type: "cluster", Name: host.Name}
			}
		}
	}

	return nil
}

// isLocalSocketEndpoint returns true if the endpoint connects via the local
// Docker socket. These endpoints duplicate what Sentinel monitors directly.
// Mirrors portainer.Endpoint.IsLocalSocket() without importing the package.
func isLocalSocketEndpoint(ep PortainerEndpoint) bool {
	if strings.HasPrefix(ep.URL, "unix://") {
		return true
	}
	// Empty URL with Docker environment type (1) means local socket mount.
	return ep.URL == "" && ep.Type == 1
}

// isLocalPortainerInstance checks whether the Portainer instance URL points to
// the same machine Sentinel is running on. We extract the hostname from the
// URL, resolve it to IP addresses, and compare against all local network
// interface addresses. This lets us auto-block only the local Docker socket
// endpoint for co-located Portainer instances while leaving remote ones alone.
func isLocalPortainerInstance(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}

	// Fast path for common loopback addresses.
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}

	// Resolve the Portainer host to IP addresses with a timeout so a slow
	// or unreachable DNS server doesn't block the HTTP handler indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		// If DNS fails, fall back to treating the host as a literal IP.
		ips = []string{host}
	}

	// Gather all local interface addresses.
	localAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	localIPs := make(map[string]bool)
	for _, addr := range localAddrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			localIPs[ipNet.IP.String()] = true
		}
	}

	// Also treat common loopback names as local.
	localIPs["127.0.0.1"] = true
	localIPs["::1"] = true

	for _, ip := range ips {
		if localIPs[ip] {
			return true
		}
	}
	return false
}
