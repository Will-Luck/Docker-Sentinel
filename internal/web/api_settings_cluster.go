package web

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// apiClusterSettings returns the current cluster configuration with defaults
// for any keys not yet saved to BoltDB.
func (s *Server) apiClusterSettings(w http.ResponseWriter, _ *http.Request) {
	result := map[string]string{
		"enabled":            "false",
		"port":               "9443",
		"grace_period":       "30m",
		"remote_policy":      "manual",
		"auto_update_agents": "false",
	}

	if s.deps.SettingsStore != nil {
		keys := map[string]string{
			"enabled":            store.SettingClusterEnabled,
			"port":               store.SettingClusterPort,
			"grace_period":       store.SettingClusterGracePeriod,
			"remote_policy":      store.SettingClusterRemotePolicy,
			"auto_update_agents": store.SettingClusterAutoUpdateAgents,
		}
		for field, dbKey := range keys {
			if v, err := s.deps.SettingsStore.LoadSetting(dbKey); err == nil && v != "" {
				result[field] = v
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// apiClusterSettingsSave validates and persists cluster configuration changes.
// When the enabled state changes, it calls the ClusterLifecycle callback to
// start or stop the gRPC server dynamically.
func (s *Server) apiClusterSettingsSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled          *bool  `json:"enabled"`
		Port             string `json:"port"`
		GracePeriod      string `json:"grace_period"`
		RemotePolicy     string `json:"remote_policy"`
		AutoUpdateAgents *bool  `json:"auto_update_agents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusInternalServerError, "settings store not available")
		return
	}

	// Validate port: must be an integer in 1024-65535.
	if req.Port != "" {
		p, err := strconv.Atoi(req.Port)
		if err != nil || p < 1024 || p > 65535 {
			writeError(w, http.StatusBadRequest, "port must be 1024-65535")
			return
		}
	}

	// Validate grace period against whitelist.
	if req.GracePeriod != "" {
		allowed := map[string]bool{"5m": true, "15m": true, "30m": true, "1h": true, "2h": true}
		if !allowed[req.GracePeriod] {
			writeError(w, http.StatusBadRequest, "invalid grace period")
			return
		}
	}

	// Validate remote policy against whitelist.
	if req.RemotePolicy != "" {
		switch req.RemotePolicy {
		case "auto", "manual", "pinned":
			// valid
		default:
			writeError(w, http.StatusBadRequest, "invalid remote policy — must be auto, manual, or pinned")
			return
		}
	}

	// Save each provided field, checking for errors.
	if req.Enabled != nil {
		val := "false"
		if *req.Enabled {
			val = "true"
		}
		if err := s.deps.SettingsStore.SaveSetting(store.SettingClusterEnabled, val); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	if req.Port != "" {
		if err := s.deps.SettingsStore.SaveSetting(store.SettingClusterPort, req.Port); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	if req.GracePeriod != "" {
		if err := s.deps.SettingsStore.SaveSetting(store.SettingClusterGracePeriod, req.GracePeriod); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	if req.RemotePolicy != "" {
		if err := s.deps.SettingsStore.SaveSetting(store.SettingClusterRemotePolicy, req.RemotePolicy); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}
	if req.AutoUpdateAgents != nil {
		val := "false"
		if *req.AutoUpdateAgents {
			val = "true"
		}
		if err := s.deps.SettingsStore.SaveSetting(store.SettingClusterAutoUpdateAgents, val); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save setting")
			return
		}
	}

	// Dynamic start/stop via ClusterLifecycle callback.
	if req.Enabled != nil && s.clusterLifecycle != nil {
		if *req.Enabled {
			if err := s.clusterLifecycle.Start(); err != nil {
				// Rollback: save disabled state since start failed.
				if err := s.deps.SettingsStore.SaveSetting(store.SettingClusterEnabled, "false"); err != nil {
					s.deps.Log.Warn("failed to rollback cluster enabled setting", "error", err)
				}
				writeError(w, http.StatusInternalServerError, "failed to start cluster: "+err.Error())
				return
			}
		} else {
			s.clusterLifecycle.Stop()
		}
	}

	s.logEvent(r, "cluster-settings", "", "Cluster settings updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) apiSwitchRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Role != "server" && body.Role != "agent" {
		writeError(w, http.StatusBadRequest, "role must be 'server' or 'agent'")
		return
	}

	if err := s.deps.SettingsStore.SaveSetting("instance_role", body.Role); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save role")
		return
	}

	s.logEvent(r, "settings", "", "Instance role changed to "+body.Role)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "restart_required": "true"})
}

// apiSetDockerTLS saves Docker TLS certificate paths for mTLS connections.
// All three paths must be provided together, or all empty to disable.
func (s *Server) apiSetDockerTLS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CA   string `json:"ca"`
		Cert string `json:"cert"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusInternalServerError, "settings store not available")
		return
	}

	// Trim whitespace from paths.
	req.CA = strings.TrimSpace(req.CA)
	req.Cert = strings.TrimSpace(req.Cert)
	req.Key = strings.TrimSpace(req.Key)

	// All-or-nothing: either all three are provided, or all are empty.
	has := req.CA != "" || req.Cert != "" || req.Key != ""
	complete := req.CA != "" && req.Cert != "" && req.Key != ""
	if has && !complete {
		writeError(w, http.StatusBadRequest, "all three certificate paths must be provided, or leave all empty to disable")
		return
	}

	// Validate that files exist when paths are provided.
	if complete {
		for label, path := range map[string]string{"CA certificate": req.CA, "client certificate": req.Cert, "client key": req.Key} {
			if _, err := os.Stat(path); err != nil {
				writeError(w, http.StatusBadRequest, label+" not found: "+path)
				return
			}
		}
	}

	// Save all three settings atomically — roll back on partial failure to
	// prevent inconsistent TLS config (e.g. CA saved but cert/key missing).
	tlsPairs := []struct{ key, val string }{
		{store.SettingDockerTLSCA, req.CA},
		{store.SettingDockerTLSCert, req.Cert},
		{store.SettingDockerTLSKey, req.Key},
	}
	for _, p := range tlsPairs {
		if err := s.deps.SettingsStore.SaveSetting(p.key, p.val); err != nil {
			// Roll back: clear all three to avoid partial config.
			for _, rb := range tlsPairs {
				_ = s.deps.SettingsStore.SaveSetting(rb.key, "")
			}
			writeError(w, http.StatusInternalServerError, "failed to save TLS config — rolled back")
			return
		}
	}

	msg := "Docker TLS certificates cleared"
	if complete {
		msg = "Docker TLS certificates saved"
	}
	s.logEvent(r, "settings", "", msg)
	writeJSON(w, http.StatusOK, map[string]string{
		"message":          msg,
		"restart_required": "true",
	})
}

// apiTestDockerTLS attempts to connect to the Docker daemon using the provided TLS certificates.
func (s *Server) apiTestDockerTLS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CA   string `json:"ca"`
		Cert string `json:"cert"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	req.CA = strings.TrimSpace(req.CA)
	req.Cert = strings.TrimSpace(req.Cert)
	req.Key = strings.TrimSpace(req.Key)

	if req.CA == "" || req.Cert == "" || req.Key == "" {
		writeError(w, http.StatusBadRequest, "all three certificate paths are required for testing")
		return
	}

	// Read the Docker host from config values.
	dockerHost := "/var/run/docker.sock"
	if vals := s.deps.Config.Values(); vals["SENTINEL_DOCKER_SOCK"] != "" {
		dockerHost = vals["SENTINEL_DOCKER_SOCK"]
	}

	// TLS only applies to TCP connections — testing against a Unix socket is meaningless.
	if !strings.HasPrefix(dockerHost, "tcp://") && !strings.HasPrefix(dockerHost, "tcps://") {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   "TLS certificates require a TCP Docker host (tcp:// or tcps://). Current host is a Unix socket.",
		})
		return
	}

	tlsCfg := &docker.TLSConfig{CACert: req.CA, ClientCert: req.Cert, ClientKey: req.Key}
	testClient, err := docker.NewClient(dockerHost, tlsCfg)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   "failed to create client: " + err.Error(),
		})
		return
	}
	defer testClient.Close()

	if err := testClient.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   "Docker ping failed: " + err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// apiSetComposeSync enables or disables Docker Compose file sync after updates.
func (s *Server) apiSetComposeSync(w http.ResponseWriter, r *http.Request) {
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
	if err := s.deps.SettingsStore.SaveSetting("compose_sync", value); err != nil {
		s.deps.Log.Error("failed to save compose_sync", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save setting")
		return
	}
	label := "disabled"
	if body.Enabled {
		label = "enabled"
	}
	s.logEvent(r, "settings", "", "Compose file sync "+label)
	writeJSON(w, http.StatusOK, map[string]string{"message": "compose file sync " + label})
}
