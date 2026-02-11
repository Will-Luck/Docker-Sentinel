package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
)

// apiContainers returns all monitored containers with policy and maintenance status.
func (s *Server) apiContainers(w http.ResponseWriter, r *http.Request) {
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
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
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
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

// apiCheck triggers a registry check for a single container.
// If an update is found, it gets added to the queue (triggering SSE events).
func (s *Server) apiCheck(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	if s.isProtectedContainer(r.Context(), name) {
		writeError(w, http.StatusForbidden, "cannot check sentinel itself")
		return
	}

	if s.deps.RegistryChecker == nil {
		writeError(w, http.StatusNotImplemented, "registry checker not available")
		return
	}

	// Find the container to get its image reference and ID.
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers for check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	var containerID, imageRef string
	for _, c := range containers {
		if containerName(c) == name {
			containerID = c.ID
			imageRef = c.Image
			break
		}
	}
	if containerID == "" {
		writeError(w, http.StatusNotFound, "container not found: "+name)
		return
	}

	// Run the check in a background goroutine — respond immediately.
	go func() {
		updateAvailable, newerVersions, err := s.deps.RegistryChecker.CheckForUpdate(context.Background(), imageRef)
		if err != nil {
			s.deps.Log.Warn("registry check failed", "name", name, "error", err)
			return
		}
		if updateAvailable {
			s.deps.Queue.Add(PendingUpdate{
				ContainerID:   containerID,
				ContainerName: name,
				CurrentImage:  imageRef,
				NewerVersions: newerVersions,
			})
			s.deps.Log.Info("update found via manual check", "name", name)
		}
	}()

	s.logEvent("check", name, "Manual registry check triggered")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "checking",
		"name":    name,
		"message": "registry check started for " + name,
	})
}

// apiSettings returns the current configuration values, merged with runtime overrides from BoltDB.
func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) {
	values := s.deps.Config.Values()

	// Overlay runtime settings from BoltDB (these take precedence over env config).
	if s.deps.SettingsStore != nil {
		dbSettings, err := s.deps.SettingsStore.GetAllSettings()
		if err != nil {
			s.deps.Log.Warn("failed to load runtime settings", "error", err)
		} else {
			for k, v := range dbSettings {
				values[k] = v
			}
		}
	}

	writeJSON(w, http.StatusOK, values)
}

// apiContainerDetail returns per-container detail as JSON.
func (s *Server) apiContainerDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	// Find container by name.
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
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
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
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

	containers, err := s.deps.Docker.ListAllContainers(r.Context())
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
			return
		}
		s.deps.EventBus.Publish(events.SSEEvent{
			Type:          events.EventContainerState,
			ContainerName: name,
			Message:       "Container restarted: " + name,
			Timestamp:     time.Now(),
		})
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
	containers, err := s.deps.Docker.ListAllContainers(ctx)
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

// apiSetPollInterval updates the poll interval at runtime.
func (s *Server) apiSetPollInterval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Interval string `json:"interval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	d, err := time.ParseDuration(body.Interval)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid duration format: "+body.Interval)
		return
	}

	if d < 5*time.Minute {
		writeError(w, http.StatusBadRequest, "poll interval must be at least 5 minutes")
		return
	}
	if d > 24*time.Hour {
		writeError(w, http.StatusBadRequest, "poll interval must be at most 24 hours")
		return
	}

	if s.deps.Scheduler != nil {
		s.deps.Scheduler.SetPollInterval(d)
	}

	// Persist to BoltDB.
	if s.deps.SettingsStore != nil {
		_ = s.deps.SettingsStore.SaveSetting("poll_interval", d.String())
	}

	s.logEvent("settings", "", "Poll interval changed to "+d.String())

	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"interval": d.String(),
		"message":  "poll interval updated to " + d.String(),
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

	s.logEvent("settings", "", "Default policy changed to "+body.Policy)

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "default policy set to " + body.Policy,
	})
}

// apiSetGracePeriod sets the grace period at runtime.
func (s *Server) apiSetGracePeriod(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Duration string `json:"duration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	d, err := time.ParseDuration(body.Duration)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid duration format: "+body.Duration)
		return
	}

	if d < 0 {
		writeError(w, http.StatusBadRequest, "grace period must not be negative")
		return
	}
	if d > 10*time.Minute {
		writeError(w, http.StatusBadRequest, "grace period must be at most 10 minutes")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	if err := s.deps.SettingsStore.SaveSetting("grace_period", d.String()); err != nil {
		s.deps.Log.Error("failed to save grace period", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save grace period")
		return
	}

	s.logEvent("settings", "", "Grace period changed to "+d.String())

	writeJSON(w, http.StatusOK, map[string]string{
		"message":  "grace period set to " + d.String(),
		"duration": d.String(),
	})
}

// apiSetPause pauses or unpauses the scan scheduler.
func (s *Server) apiSetPause(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Paused bool `json:"paused"`
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
	if body.Paused {
		value = "true"
	}

	if err := s.deps.SettingsStore.SaveSetting("paused", value); err != nil {
		s.deps.Log.Error("failed to save pause state", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save pause state")
		return
	}

	action := "unpaused"
	if body.Paused {
		action = "paused"
	}
	s.logEvent("settings", "", "Scheduler "+action)

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "scheduler " + action,
		"paused":  value,
	})
}

// apiSetFilters sets container name filter patterns for scan exclusion.
func (s *Server) apiSetFilters(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Patterns []string `json:"patterns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	value := strings.Join(body.Patterns, "\n")

	if err := s.deps.SettingsStore.SaveSetting("filters", value); err != nil {
		s.deps.Log.Error("failed to save filters", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save filters")
		return
	}

	s.logEvent("settings", "", "Scan filters updated")

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "filters updated",
	})
}

// apiSelfUpdate triggers a self-update via an ephemeral helper container.
func (s *Server) apiSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if s.deps.SelfUpdater == nil {
		writeError(w, http.StatusNotImplemented, "self-update not available")
		return
	}

	go func() {
		if err := s.deps.SelfUpdater.Update(context.Background()); err != nil {
			s.deps.Log.Error("self-update failed", "error", err)
		}
	}()

	s.logEvent("self_update", "sentinel", "Self-update initiated")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "started",
		"message": "Self-update initiated — Sentinel will restart shortly",
	})
}

// apiStop stops a container by name.
func (s *Server) apiStop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	if s.isProtectedContainer(r.Context(), name) {
		writeError(w, http.StatusForbidden, "cannot stop sentinel itself via the dashboard")
		return
	}

	if s.deps.Stopper == nil {
		writeError(w, http.StatusNotImplemented, "stop not available")
		return
	}

	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers for stop", "error", err)
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
		if err := s.deps.Stopper.StopContainer(context.Background(), containerID); err != nil {
			s.deps.Log.Error("stop failed", "name", name, "error", err)
			return
		}
		s.deps.EventBus.Publish(events.SSEEvent{
			Type:          events.EventContainerState,
			ContainerName: name,
			Message:       "Container stopped: " + name,
			Timestamp:     time.Now(),
		})
	}()

	s.logEvent("stop", name, "Container stopped")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "stopping",
		"name":    name,
		"message": "stop initiated for " + name,
	})
}

// apiStart starts a container by name.
func (s *Server) apiStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	if s.isProtectedContainer(r.Context(), name) {
		writeError(w, http.StatusForbidden, "cannot start sentinel itself via the dashboard")
		return
	}

	if s.deps.Starter == nil {
		writeError(w, http.StatusNotImplemented, "start not available")
		return
	}

	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers for start", "error", err)
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
		if err := s.deps.Starter.StartContainer(context.Background(), containerID); err != nil {
			s.deps.Log.Error("start failed", "name", name, "error", err)
			return
		}
		s.deps.EventBus.Publish(events.SSEEvent{
			Type:          events.EventContainerState,
			ContainerName: name,
			Message:       "Container started: " + name,
			Timestamp:     time.Now(),
		})
	}()

	s.logEvent("start", name, "Container started")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "starting",
		"name":    name,
		"message": "start initiated for " + name,
	})
}

// apiGetNotifications returns the current notification channels with secrets masked.
func (s *Server) apiGetNotifications(w http.ResponseWriter, r *http.Request) {
	if s.deps.NotifyConfig == nil {
		writeJSON(w, http.StatusOK, []notify.Channel{})
		return
	}

	channels, err := s.deps.NotifyConfig.GetNotificationChannels()
	if err != nil {
		s.deps.Log.Error("failed to load notification channels", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load notification channels")
		return
	}
	if channels == nil {
		channels = []notify.Channel{}
	}

	// Mask secrets for API response.
	masked := make([]notify.Channel, len(channels))
	for i, ch := range channels {
		masked[i] = notify.MaskSecrets(ch)
	}
	writeJSON(w, http.StatusOK, masked)
}

// apiSaveNotifications saves notification channels and reconfigures the notifier chain.
func (s *Server) apiSaveNotifications(w http.ResponseWriter, r *http.Request) {
	var channels []notify.Channel
	if err := json.NewDecoder(r.Body).Decode(&channels); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.deps.NotifyConfig == nil {
		writeError(w, http.StatusNotImplemented, "notification config not available")
		return
	}

	// Restore masked secrets from existing saved channels.
	existing, _ := s.deps.NotifyConfig.GetNotificationChannels()
	existingMap := make(map[string]notify.Channel)
	for _, ch := range existing {
		existingMap[ch.ID] = ch
	}
	for i, ch := range channels {
		if old, ok := existingMap[ch.ID]; ok {
			channels[i].Settings = restoreSecrets(ch, old)
		}
	}

	if err := s.deps.NotifyConfig.SetNotificationChannels(channels); err != nil {
		s.deps.Log.Error("failed to save notification channels", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save notification channels")
		return
	}

	// Rebuild notifier chain.
	if s.deps.NotifyReconfigurer != nil {
		var notifiers []notify.Notifier
		notifiers = append(notifiers, notify.NewLogNotifier(s.deps.Log))
		for _, ch := range channels {
			if !ch.Enabled {
				continue
			}
			n, err := notify.BuildFilteredNotifier(ch)
			if err != nil {
				s.deps.Log.Warn("failed to build notifier", "channel", ch.Name, "type", string(ch.Type), "error", err)
				continue
			}
			notifiers = append(notifiers, n)
		}
		s.deps.NotifyReconfigurer.Reconfigure(notifiers...)
	}

	s.logEvent("settings", "", "Notification configuration updated")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "notification settings saved",
	})
}

// apiTestNotification sends a test event through the notification chain or a single channel.
func (s *Server) apiTestNotification(w http.ResponseWriter, r *http.Request) {
	if s.deps.NotifyReconfigurer == nil {
		writeError(w, http.StatusNotImplemented, "notifications not available")
		return
	}

	var body struct {
		ID string `json:"id"`
	}
	// Try to decode body -- if empty, test entire chain (backward compat).
	_ = json.NewDecoder(r.Body).Decode(&body)

	testEvent := notify.Event{
		Type:          notify.EventUpdateAvailable,
		ContainerName: "sentinel-test",
		OldImage:      "test:latest",
		Timestamp:     time.Now(),
	}

	if body.ID != "" && s.deps.NotifyConfig != nil {
		// Test single channel.
		channels, err := s.deps.NotifyConfig.GetNotificationChannels()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load channels")
			return
		}
		for _, ch := range channels {
			if ch.ID == body.ID {
				n, err := notify.BuildNotifier(ch)
				if err != nil {
					writeError(w, http.StatusBadRequest, "failed to build notifier: "+err.Error())
					return
				}
				if err := n.Send(r.Context(), testEvent); err != nil {
					writeError(w, http.StatusBadGateway, "test failed: "+err.Error())
					return
				}
				writeJSON(w, http.StatusOK, map[string]string{
					"status":  "ok",
					"message": "test notification sent to " + ch.Name,
				})
				return
			}
		}
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}

	// Test entire chain.
	if multi, ok := s.deps.NotifyReconfigurer.(*notify.Multi); ok {
		multi.Notify(r.Context(), testEvent)
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "test notification sent",
	})
}

// apiNotificationEventTypes returns the list of event types available for per-channel filtering.
func (s *Server) apiNotificationEventTypes(w http.ResponseWriter, _ *http.Request) {
	types := notify.AllEventTypes()
	result := make([]string, len(types))
	for i, t := range types {
		result[i] = string(t)
	}
	writeJSON(w, http.StatusOK, result)
}

// restoreSecrets returns the saved settings if the incoming settings contain any
// masked values (strings ending in "****"). This prevents overwriting real secrets
// with their masked representations when the frontend sends back unchanged channels.
func restoreSecrets(incoming, saved notify.Channel) json.RawMessage {
	var fields map[string]interface{}
	if err := json.Unmarshal(incoming.Settings, &fields); err != nil {
		return incoming.Settings
	}

	for _, v := range fields {
		s, ok := v.(string)
		if ok && strings.HasSuffix(s, "****") {
			return saved.Settings
		}
	}
	return incoming.Settings
}
