package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// apiChangePolicy sets a policy override for a container in BoltDB.
// No container restart — instant DB write.
func (s *Server) apiChangePolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	var body struct {
		Policy string `json:"policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	switch body.Policy {
	case "auto", "manual", "pinned":
		// valid
	default:
		writeError(w, http.StatusBadRequest, "policy must be auto, manual, or pinned")
		return
	}

	if s.deps.Policy == nil {
		writeError(w, http.StatusNotImplemented, "policy change not available")
		return
	}

	// Self-protection: refuse to change policy on Sentinel itself.
	if s.isProtectedContainer(r.Context(), name) {
		writeError(w, http.StatusForbidden, "cannot change policy on sentinel itself")
		return
	}

	// For remote containers, key the policy override by hostID::name
	// to avoid collisions with local containers of the same name.
	policyKey := name
	if hostID := r.URL.Query().Get("host"); hostID != "" {
		policyKey = hostID + "::" + name
	}

	if err := s.deps.Policy.SetPolicyOverride(policyKey, body.Policy); err != nil {
		s.deps.Log.Error("policy change failed", "name", name, "policy", body.Policy, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to set policy override")
		return
	}

	s.deps.Log.Info("policy override set", "name", name, "policy", body.Policy)
	s.logEvent(r, "policy_set", name, "Policy set to "+body.Policy)

	s.deps.EventBus.Publish(events.SSEEvent{
		Type:          events.EventPolicyChange,
		ContainerName: name,
		Message:       "Policy set to " + body.Policy + " for " + name,
		Timestamp:     time.Now(),
	})

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"name":    name,
		"policy":  body.Policy,
		"message": "policy set to " + body.Policy + " for " + name,
	})
}

// apiDeletePolicy removes the policy override, falling back to Docker label.
func (s *Server) apiDeletePolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	if s.deps.Policy == nil {
		writeError(w, http.StatusNotImplemented, "policy change not available")
		return
	}

	policyKey := name
	if hostID := r.URL.Query().Get("host"); hostID != "" {
		policyKey = hostID + "::" + name
	}

	if _, ok := s.deps.Policy.GetPolicyOverride(policyKey); !ok {
		writeError(w, http.StatusNotFound, "no policy override for "+name)
		return
	}

	if err := s.deps.Policy.DeletePolicyOverride(policyKey); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete policy override")
		return
	}

	s.logEvent(r, "policy_delete", name, "Policy override removed")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"name":    name,
		"message": "policy override removed for " + name,
	})
}

// apiBulkPolicy sets policy overrides for multiple containers.
// Supports preview mode (default) and confirm mode.
func (s *Server) apiBulkPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Containers []string `json:"containers"`
		Policy     string   `json:"policy"`
		Confirm    bool     `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	switch body.Policy {
	case "auto", "manual", "pinned":
		// valid
	default:
		writeError(w, http.StatusBadRequest, "policy must be auto, manual, or pinned")
		return
	}

	if len(body.Containers) == 0 {
		writeError(w, http.StatusBadRequest, "containers list must not be empty")
		return
	}

	if s.deps.Policy == nil {
		writeError(w, http.StatusNotImplemented, "policy change not available")
		return
	}

	type changeEntry struct {
		Name string `json:"name"`
		From string `json:"from"`
		To   string `json:"to"`
	}
	type blockedEntry struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	type unchangedEntry struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}

	var changes []changeEntry
	var blocked []blockedEntry
	var unchanged []unchangedEntry

	// Build label cache once from all sources (local, Swarm, cluster)
	// so we don't re-query per container.
	allLabels := s.allContainerLabels(r.Context())

	for _, name := range body.Containers {
		labels := allLabels[name]

		// Self-protection check.
		if labels != nil && labels["sentinel.self"] == "true" {
			blocked = append(blocked, blockedEntry{Name: name, Reason: "self-protected"})
			continue
		}

		current := containerPolicy(labels)
		if p, ok := s.deps.Policy.GetPolicyOverride(name); ok {
			current = p
		}

		if current == body.Policy {
			unchanged = append(unchanged, unchangedEntry{Name: name, Reason: "already " + body.Policy})
			continue
		}

		changes = append(changes, changeEntry{Name: name, From: current, To: body.Policy})
	}

	// Preview mode: show what would happen.
	if !body.Confirm {
		writeJSON(w, http.StatusOK, map[string]any{
			"mode":      "preview",
			"changes":   changes,
			"blocked":   blocked,
			"unchanged": unchanged,
		})
		return
	}

	// Confirm mode: apply all changes.
	applied := 0
	for _, c := range changes {
		if err := s.deps.Policy.SetPolicyOverride(c.Name, body.Policy); err != nil {
			s.deps.Log.Error("bulk policy change failed", "name", c.Name, "error", err)
			continue
		}
		applied++
	}

	s.deps.Log.Info("bulk policy change applied",
		"policy", body.Policy, "applied", applied,
		"blocked", len(blocked), "unchanged", len(unchanged))

	for _, c := range changes {
		s.logEvent(r, "policy_set", c.Name, "Bulk policy set to "+body.Policy)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"mode":      "executed",
		"applied":   applied,
		"blocked":   len(blocked),
		"unchanged": len(unchanged),
	})
}

// apiSetDefaultPolicy sets the default policy at runtime.
func (s *Server) apiSetDefaultPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Policy string `json:"policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	switch body.Policy {
	case "auto", "manual", "pinned":
		// valid
	default:
		writeError(w, http.StatusBadRequest, "policy must be auto, manual, or pinned")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	if err := s.deps.SettingsStore.SaveSetting("default_policy", body.Policy); err != nil {
		s.deps.Log.Error("failed to save default policy", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save default policy")
		return
	}

	// Apply to in-memory config so the engine uses the new value immediately.
	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetDefaultPolicy(body.Policy)
	}

	s.logEvent(r, "settings", "", "Default policy changed to "+body.Policy)

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "default policy set to " + body.Policy,
	})
}
