package web

import (
	"context"
	"encoding/json"
	"net/http"
)

// apiContainers returns all monitored containers with policy and maintenance status.
func (s *Server) apiContainers(w http.ResponseWriter, r *http.Request) {
	containers, err := s.deps.Docker.ListContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	type containerInfo struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Image       string `json:"image"`
		Policy      string `json:"policy"`
		State       string `json:"state"`
		Maintenance bool   `json:"maintenance"`
	}

	result := make([]containerInfo, 0, len(containers))
	for _, c := range containers {
		name := containerName(c)
		policy := containerPolicy(c.Labels)
		if s.deps.Policy != nil {
			if p, ok := s.deps.Policy.GetPolicyOverride(name); ok {
				policy = p
			}
		}

		maintenance, err := s.deps.Store.GetMaintenance(name)
		if err != nil {
			s.deps.Log.Warn("failed to read maintenance state", "name", name, "error", err)
		}

		result = append(result, containerInfo{
			ID:          c.ID,
			Name:        name,
			Image:       c.Image,
			Policy:      policy,
			State:       c.State,
			Maintenance: maintenance,
		})
	}

	writeJSON(w, http.StatusOK, result)
}

// apiHistory returns the most recent update records.
func (s *Server) apiHistory(w http.ResponseWriter, r *http.Request) {
	records, err := s.deps.Store.ListHistory(100)
	if err != nil {
		s.deps.Log.Error("failed to list history", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list history")
		return
	}

	if records == nil {
		records = []UpdateRecord{}
	}
	writeJSON(w, http.StatusOK, records)
}

// apiQueue returns all pending manual approvals.
func (s *Server) apiQueue(w http.ResponseWriter, r *http.Request) {
	items := s.deps.Queue.List()
	if items == nil {
		items = []PendingUpdate{}
	}
	writeJSON(w, http.StatusOK, items)
}

// apiApprove approves a pending update and triggers the update.
func (s *Server) apiApprove(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	if s.isProtectedContainer(r.Context(), name) {
		writeError(w, http.StatusForbidden, "cannot approve updates for sentinel itself")
		return
	}

	update, ok := s.deps.Queue.Approve(name)
	if !ok {
		writeError(w, http.StatusNotFound, "no pending update for "+name)
		return
	}

	// Trigger the update in background — don't block the HTTP response.
	// Use a detached context because r.Context() is cancelled when the handler returns.
	go func() {
		err := s.deps.Updater.UpdateContainer(context.Background(), update.ContainerID, update.ContainerName)
		if err != nil {
			s.deps.Log.Error("approved update failed", "name", name, "error", err)
		}
	}()

	s.logEvent("approve", name, "Update approved and started")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "approved",
		"name":    name,
		"message": "update started for " + name,
	})
}

// apiReject rejects and removes a pending update from the queue.
func (s *Server) apiReject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	s.deps.Queue.Remove(name)
	s.logEvent("reject", name, "Update rejected")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "rejected",
		"name":    name,
		"message": "update rejected for " + name,
	})
}

// apiUpdate triggers an immediate update for a container by name.
func (s *Server) apiUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	if s.isProtectedContainer(r.Context(), name) {
		writeError(w, http.StatusForbidden, "cannot update sentinel itself via the dashboard")
		return
	}

	// Find the container by name to get its ID.
	containers, err := s.deps.Docker.ListContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers for update", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	var containerID string
	for _, c := range containers {
		if containerName(c) == name {
			containerID = c.ID
			break
		}
	}

	if containerID == "" {
		writeError(w, http.StatusNotFound, "container not found: "+name)
		return
	}

	// Trigger update in background — detached context since r.Context() dies with the response.
	go func() {
		err := s.deps.Updater.UpdateContainer(context.Background(), containerID, name)
		if err != nil {
			s.deps.Log.Error("manual update failed", "name", name, "error", err)
		}
	}()

	s.logEvent("update", name, "Manual update triggered")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "started",
		"name":    name,
		"message": "update started for " + name,
	})
}

// apiSettings returns the current configuration values.
func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.Config.Values())
}

// apiContainerDetail returns per-container detail as JSON.
func (s *Server) apiContainerDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	// Find container by name.
	containers, err := s.deps.Docker.ListContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	var found *ContainerSummary
	for _, c := range containers {
		if containerName(c) == name {
			found = &c
			break
		}
	}
	if found == nil {
		writeError(w, http.StatusNotFound, "container not found: "+name)
		return
	}

	// Gather history.
	history, err := s.deps.Store.ListHistoryByContainer(name, 50)
	if err != nil {
		s.deps.Log.Warn("failed to list history for container", "name", name, "error", err)
	}
	if history == nil {
		history = []UpdateRecord{}
	}

	// Gather snapshots (nil-check the dependency).
	var snapshots []SnapshotEntry
	if s.deps.Snapshots != nil {
		storeEntries, err := s.deps.Snapshots.ListSnapshots(name)
		if err != nil {
			s.deps.Log.Warn("failed to list snapshots", "name", name, "error", err)
		}
		snapshots = append(snapshots, storeEntries...)
	}
	if snapshots == nil {
		snapshots = []SnapshotEntry{}
	}

	maintenance, _ := s.deps.Store.GetMaintenance(name)

	type detailResponse struct {
		ID          string          `json:"id"`
		Name        string          `json:"name"`
		Image       string          `json:"image"`
		Policy      string          `json:"policy"`
		State       string          `json:"state"`
		Maintenance bool            `json:"maintenance"`
		History     []UpdateRecord  `json:"history"`
		Snapshots   []SnapshotEntry `json:"snapshots"`
	}

	writeJSON(w, http.StatusOK, detailResponse{
		ID:          found.ID,
		Name:        containerName(*found),
		Image:       found.Image,
		Policy:      s.resolvedPolicy(found.Labels, containerName(*found)),
		State:       found.State,
		Maintenance: maintenance,
		History:     history,
		Snapshots:   snapshots,
	})
}

// apiContainerVersions returns available image versions from the registry.
func (s *Server) apiContainerVersions(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	if s.deps.Registry == nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}

	// Find container to extract its image reference.
	containers, err := s.deps.Docker.ListContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	var imageRef string
	for _, c := range containers {
		if containerName(c) == name {
			imageRef = c.Image
			break
		}
	}
	if imageRef == "" {
		writeError(w, http.StatusNotFound, "container not found: "+name)
		return
	}

	versions, err := s.deps.Registry.ListVersions(r.Context(), imageRef)
	if err != nil {
		s.deps.Log.Warn("failed to list versions", "name", name, "image", imageRef, "error", err)
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	if versions == nil {
		versions = []string{}
	}

	writeJSON(w, http.StatusOK, versions)
}

// apiRollback triggers a rollback to the most recent snapshot.
func (s *Server) apiRollback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	if s.isProtectedContainer(r.Context(), name) {
		writeError(w, http.StatusForbidden, "cannot rollback sentinel itself via the dashboard")
		return
	}

	if s.deps.Rollback == nil {
		writeError(w, http.StatusNotImplemented, "rollback not available")
		return
	}

	go func() {
		if err := s.deps.Rollback.RollbackContainer(context.Background(), name); err != nil {
			s.deps.Log.Error("rollback failed", "name", name, "error", err)
		}
	}()

	s.logEvent("rollback", name, "Rollback triggered")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "started",
		"name":    name,
		"message": "rollback started for " + name,
	})
}

// apiRestart restarts a container by name.
func (s *Server) apiRestart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	if s.isProtectedContainer(r.Context(), name) {
		writeError(w, http.StatusForbidden, "cannot restart sentinel itself via the dashboard")
		return
	}

	if s.deps.Restarter == nil {
		writeError(w, http.StatusNotImplemented, "restart not available")
		return
	}

	containers, err := s.deps.Docker.ListContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers for restart", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	var containerID string
	for _, c := range containers {
		if containerName(c) == name {
			containerID = c.ID
			break
		}
	}

	if containerID == "" {
		writeError(w, http.StatusNotFound, "container not found: "+name)
		return
	}

	go func() {
		if err := s.deps.Restarter.RestartContainer(context.Background(), containerID); err != nil {
			s.deps.Log.Error("restart failed", "name", name, "error", err)
		}
	}()

	s.logEvent("restart", name, "Container restarted")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "restarting",
		"name":    name,
		"message": "restart initiated for " + name,
	})
}

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

	if err := s.deps.Policy.SetPolicyOverride(name, body.Policy); err != nil {
		s.deps.Log.Error("policy change failed", "name", name, "policy", body.Policy, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to set policy override")
		return
	}

	s.deps.Log.Info("policy override set", "name", name, "policy", body.Policy)
	s.logEvent("policy_set", name, "Policy set to "+body.Policy)

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

	if _, ok := s.deps.Policy.GetPolicyOverride(name); !ok {
		writeError(w, http.StatusNotFound, "no policy override for "+name)
		return
	}

	if err := s.deps.Policy.DeletePolicyOverride(name); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete policy override")
		return
	}

	s.logEvent("policy_delete", name, "Policy override removed")

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

	for _, name := range body.Containers {
		// Self-protection check.
		if s.isProtectedContainer(r.Context(), name) {
			blocked = append(blocked, blockedEntry{Name: name, Reason: "self-protected"})
			continue
		}

		current := containerPolicy(s.getContainerLabels(r.Context(), name))
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
		s.logEvent("policy_set", c.Name, "Bulk policy set to "+body.Policy)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"mode":      "executed",
		"applied":   applied,
		"blocked":   len(blocked),
		"unchanged": len(unchanged),
	})
}

// isProtectedContainer checks if a container has the sentinel.self=true label.
func (s *Server) isProtectedContainer(ctx context.Context, name string) bool {
	labels := s.getContainerLabels(ctx, name)
	return labels["sentinel.self"] == "true"
}

// resolvedPolicy returns the effective policy: DB override → label fallback.
func (s *Server) resolvedPolicy(labels map[string]string, name string) string {
	if s.deps.Policy != nil {
		if p, ok := s.deps.Policy.GetPolicyOverride(name); ok {
			return p
		}
	}
	return containerPolicy(labels)
}

// getContainerLabels fetches labels for a named container.
func (s *Server) getContainerLabels(ctx context.Context, name string) map[string]string {
	containers, err := s.deps.Docker.ListContainers(ctx)
	if err != nil {
		return nil
	}
	for _, c := range containers {
		if containerName(c) == name {
			return c.Labels
		}
	}
	return nil
}

// containerName extracts a clean container name from a summary.
func containerName(c ContainerSummary) string {
	if len(c.Names) > 0 {
		name := c.Names[0]
		if len(name) > 0 && name[0] == '/' {
			return name[1:]
		}
		return name
	}
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

// containerPolicy reads the sentinel.policy label, defaulting to "manual".
func containerPolicy(labels map[string]string) string {
	if v, ok := labels["sentinel.policy"]; ok {
		switch v {
		case "auto", "manual", "pinned":
			return v
		}
	}
	return "manual"
}

// logEvent appends a log entry if the EventLog dependency is available.
func (s *Server) logEvent(eventType, container, message string) {
	if s.deps.EventLog == nil {
		return
	}
	_ = s.deps.EventLog.AppendLog(LogEntry{
		Type:      eventType,
		Message:   message,
		Container: container,
	})
}
