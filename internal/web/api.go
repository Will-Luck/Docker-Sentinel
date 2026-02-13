package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
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
		Stack       string `json:"stack,omitempty"`
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
			Stack:       c.Labels["com.docker.compose.project"],
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

	// Build target image for semver version bumps.
	approveTarget := ""
	if len(update.NewerVersions) > 0 {
		approveTarget = webReplaceTag(update.CurrentImage, update.NewerVersions[0])
	}

	// Trigger the update in background — don't block the HTTP response.
	// Use a detached context because r.Context() is cancelled when the handler returns.
	go func() {
		err := s.deps.Updater.UpdateContainer(context.Background(), update.ContainerID, update.ContainerName, approveTarget)
		if errors.Is(err, engine.ErrUpdateInProgress) {
			// Re-enqueue the approved update so it's not lost.
			s.deps.Queue.Add(update)
			s.deps.Log.Warn("update busy, re-enqueued", "name", name)
			return
		}
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

// apiIgnoreVersion ignores a specific version for a container and removes it from the queue.
func (s *Server) apiIgnoreVersion(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	update, ok := s.deps.Queue.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, "no pending update for "+name)
		return
	}

	if len(update.NewerVersions) == 0 {
		writeError(w, http.StatusBadRequest, "no specific version to ignore (digest-only update)")
		return
	}

	ignoredVersion := update.NewerVersions[0]
	if s.deps.IgnoredVersions != nil {
		if err := s.deps.IgnoredVersions.AddIgnoredVersion(name, ignoredVersion); err != nil {
			s.deps.Log.Error("failed to save ignored version", "name", name, "version", ignoredVersion, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to save ignored version")
			return
		}
	}

	s.deps.Queue.Remove(name)
	s.logEvent("ignore", name, "Ignored version "+ignoredVersion)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ignored",
		"name":    name,
		"version": ignoredVersion,
		"message": "version " + ignoredVersion + " ignored for " + name,
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
		err := s.deps.Updater.UpdateContainer(context.Background(), containerID, name, "")
		if errors.Is(err, engine.ErrUpdateInProgress) {
			s.deps.Log.Warn("manual update skipped, already in progress", "name", name)
			s.deps.EventBus.Publish(events.SSEEvent{
				Type:          events.EventContainerUpdate,
				ContainerName: name,
				Message:       "update already in progress for " + name,
				Timestamp:     time.Now(),
			})
			return
		}
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

	s.logEvent("check", name, "Manual registry check triggered")

	// Run the check synchronously so the client spinner stays active.
	updateAvailable, newerVersions, checkErr := s.deps.RegistryChecker.CheckForUpdate(r.Context(), imageRef)
	if checkErr != nil {
		s.deps.Log.Warn("registry check failed", "name", name, "error", checkErr)
		writeError(w, http.StatusBadGateway, "registry check failed: "+checkErr.Error())
		return
	}

	if updateAvailable {
		// Filter out ignored versions before queuing.
		if len(newerVersions) > 0 && s.deps.IgnoredVersions != nil {
			ignored, _ := s.deps.IgnoredVersions.GetIgnoredVersions(name)
			if len(ignored) > 0 {
				ignoredSet := make(map[string]bool, len(ignored))
				for _, v := range ignored {
					ignoredSet[v] = true
				}
				var filtered []string
				for _, v := range newerVersions {
					if !ignoredSet[v] {
						filtered = append(filtered, v)
					}
				}
				newerVersions = filtered
			}
		}

		if len(newerVersions) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"status":  "up_to_date",
				"name":    name,
				"message": name + " is up to date (newer versions ignored)",
			})
			return
		}

		s.deps.Queue.Add(PendingUpdate{
			ContainerID:   containerID,
			ContainerName: name,
			CurrentImage:  imageRef,
			NewerVersions: newerVersions,
		})
		s.deps.Log.Info("update found via manual check", "name", name)
		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "update_available",
			"name":           name,
			"message":        "Update available for " + name,
			"newer_versions": newerVersions,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "up_to_date",
		"name":    name,
		"message": name + " is up to date",
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
	if err := s.deps.EventLog.AppendLog(LogEntry{
		Type:      eventType,
		Message:   message,
		Container: container,
	}); err != nil {
		s.deps.Log.Warn("failed to persist event log", "type", eventType, "container", container, "error", err)
	}
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
		if err := s.deps.SettingsStore.SaveSetting("poll_interval", d.String()); err != nil {
			s.deps.Log.Warn("failed to persist poll interval setting", "error", err)
		}
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

	// Apply to in-memory config so the engine uses the new value immediately.
	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetDefaultPolicy(body.Policy)
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

	// Apply to in-memory config so the engine uses the new value immediately.
	if s.deps.ConfigWriter != nil {
		s.deps.ConfigWriter.SetGracePeriod(d)
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

// apiTriggerScan triggers an immediate scan cycle.
func (s *Server) apiTriggerScan(w http.ResponseWriter, r *http.Request) {
	if s.deps.Scheduler == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}

	go s.deps.Scheduler.TriggerScan(context.Background())

	s.logEvent("scan", "", "Manual scan triggered")
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "Scan started",
	})
}

// apiLastScan returns the time of the last completed scan.
func (s *Server) apiLastScan(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Scheduler == nil {
		writeJSON(w, http.StatusOK, map[string]any{"last_scan": nil})
		return
	}

	t := s.deps.Scheduler.LastScanTime()
	if t.IsZero() {
		writeJSON(w, http.StatusOK, map[string]any{"last_scan": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"last_scan": t})
}

// webReplaceTag replaces the tag portion of an image reference.
// e.g. webReplaceTag("dxflrs/garage:v2.1.0", "v2.2.0") → "dxflrs/garage:v2.2.0"
func webReplaceTag(imageRef, newTag string) string {
	if i := strings.LastIndex(imageRef, ":"); i >= 0 {
		candidate := imageRef[i+1:]
		if !strings.Contains(candidate, "/") {
			return imageRef[:i+1] + newTag
		}
	}
	return imageRef + ":" + newTag
}

// apiGetNotifyPref returns the notification preference for a container.
func (s *Server) apiGetNotifyPref(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}
	if s.deps.NotifyState == nil {
		writeJSON(w, http.StatusOK, map[string]string{"mode": "default"})
		return
	}
	pref, err := s.deps.NotifyState.GetNotifyPref(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load notification preference")
		return
	}
	if pref == nil {
		writeJSON(w, http.StatusOK, map[string]string{"mode": "default"})
		return
	}
	writeJSON(w, http.StatusOK, pref)
}

// apiSetNotifyPref sets the notification preference for a container.
func (s *Server) apiSetNotifyPref(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	switch body.Mode {
	case "default", "every_scan", "digest_only", "muted":
		// valid
	default:
		writeError(w, http.StatusBadRequest, "mode must be default, every_scan, digest_only, or muted")
		return
	}
	if s.deps.NotifyState == nil {
		writeError(w, http.StatusNotImplemented, "notification preferences not available")
		return
	}
	if body.Mode == "default" {
		// "default" means remove override — fall back to global setting.
		if err := s.deps.NotifyState.DeleteNotifyPref(name); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete notification preference")
			return
		}
	} else {
		if err := s.deps.NotifyState.SetNotifyPref(name, &NotifyPref{Mode: body.Mode}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save notification preference")
			return
		}
	}
	s.logEvent("notify_pref", name, "Notification mode set to "+body.Mode)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "mode": body.Mode})
}

// apiClearAllNotifyStates resets all notification dedup states, allowing
// the next scan to re-trigger notifications for pending updates.
func (s *Server) apiClearAllNotifyStates(w http.ResponseWriter, r *http.Request) {
	if s.deps.NotifyState == nil {
		writeError(w, http.StatusNotImplemented, "notification state not available")
		return
	}
	states, err := s.deps.NotifyState.AllNotifyStates()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load notification states")
		return
	}
	cleared := 0
	for name := range states {
		if err := s.deps.NotifyState.ClearNotifyState(name); err == nil {
			cleared++
		}
	}
	s.logEvent("notify_states_cleared", "", fmt.Sprintf("Cleared %d notification states", cleared))
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "cleared": cleared})
}

// apiGetDigestSettings returns the digest configuration.
func (s *Server) apiGetDigestSettings(w http.ResponseWriter, r *http.Request) {
	settings := map[string]string{
		"digest_enabled":      "true",
		"digest_time":         "09:00",
		"digest_interval":     "24h",
		"default_notify_mode": "default",
	}
	if s.deps.SettingsStore != nil {
		for key := range settings {
			if val, err := s.deps.SettingsStore.LoadSetting(key); err == nil && val != "" {
				settings[key] = val
			}
		}
	}
	writeJSON(w, http.StatusOK, settings)
}

// apiSaveDigestSettings saves digest configuration and reconfigures the scheduler.
func (s *Server) apiSaveDigestSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled           *bool  `json:"digest_enabled,omitempty"`
		Time              string `json:"digest_time,omitempty"`
		Interval          string `json:"digest_interval,omitempty"`
		DefaultNotifyMode string `json:"default_notify_mode,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	// Validate all fields before saving any.
	if body.Time != "" {
		if _, err := time.Parse("15:04", body.Time); err != nil {
			writeError(w, http.StatusBadRequest, "invalid time format, use HH:MM")
			return
		}
	}
	if body.Interval != "" {
		if _, err := time.ParseDuration(body.Interval); err != nil {
			writeError(w, http.StatusBadRequest, "invalid interval duration")
			return
		}
	}
	if body.DefaultNotifyMode != "" {
		switch body.DefaultNotifyMode {
		case "default", "every_scan", "digest_only", "muted":
			// valid
		default:
			writeError(w, http.StatusBadRequest, "invalid default_notify_mode")
			return
		}
	}

	// All valid — save atomically.
	if body.Enabled != nil {
		val := "true"
		if !*body.Enabled {
			val = "false"
		}
		_ = s.deps.SettingsStore.SaveSetting("digest_enabled", val)
	}
	if body.Time != "" {
		_ = s.deps.SettingsStore.SaveSetting("digest_time", body.Time)
	}
	if body.Interval != "" {
		_ = s.deps.SettingsStore.SaveSetting("digest_interval", body.Interval)
	}
	if body.DefaultNotifyMode != "" {
		_ = s.deps.SettingsStore.SaveSetting("default_notify_mode", body.DefaultNotifyMode)
	}

	// Signal digest scheduler to reconfigure.
	if s.deps.Digest != nil {
		s.deps.Digest.SetDigestConfig()
	}

	s.logEvent("settings", "", "Digest settings updated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "digest settings saved"})
}

// apiGetAllNotifyPrefs returns all per-container notification preferences.
func (s *Server) apiGetAllNotifyPrefs(w http.ResponseWriter, r *http.Request) {
	if s.deps.NotifyState == nil {
		writeJSON(w, http.StatusOK, map[string]*NotifyPref{})
		return
	}
	prefs, err := s.deps.NotifyState.AllNotifyPrefs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load notification preferences")
		return
	}
	// Convert to web types.
	result := make(map[string]map[string]string, len(prefs))
	for name, p := range prefs {
		result[name] = map[string]string{"mode": p.Mode}
	}
	writeJSON(w, http.StatusOK, result)
}

// apiTriggerDigest triggers an immediate digest notification.
func (s *Server) apiTriggerDigest(w http.ResponseWriter, r *http.Request) {
	if s.deps.Digest == nil {
		writeError(w, http.StatusNotImplemented, "digest scheduler not available")
		return
	}
	go s.deps.Digest.TriggerDigest(context.Background())
	s.logEvent("digest", "", "Manual digest triggered")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "digest triggered"})
}

// apiGetDigestBanner returns pending update info for the dashboard banner.
func (s *Server) apiGetDigestBanner(w http.ResponseWriter, r *http.Request) {
	if s.deps.NotifyState == nil {
		writeJSON(w, http.StatusOK, map[string]any{"pending": []string{}, "count": 0})
		return
	}
	states, err := s.deps.NotifyState.AllNotifyStates()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load notification states")
		return
	}

	// Load dismissed state.
	var dismissed []string
	if s.deps.SettingsStore != nil {
		if val, loadErr := s.deps.SettingsStore.LoadSetting("digest_banner_dismissed"); loadErr == nil && val != "" {
			_ = json.Unmarshal([]byte(val), &dismissed)
		}
	}
	dismissedSet := make(map[string]bool, len(dismissed))
	for _, d := range dismissed {
		dismissedSet[d] = true
	}

	var names []string
	for name, state := range states {
		key := name + "::" + state.LastDigest
		if !dismissedSet[key] {
			names = append(names, name)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pending": names,
		"count":   len(names),
	})
}

// apiDismissDigestBanner dismisses the digest banner for current updates.
func (s *Server) apiDismissDigestBanner(w http.ResponseWriter, r *http.Request) {
	if s.deps.NotifyState == nil || s.deps.SettingsStore == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	states, err := s.deps.NotifyState.AllNotifyStates()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load notification states")
		return
	}

	dismissed := make([]string, 0, len(states))
	for name, state := range states {
		dismissed = append(dismissed, name+"::"+state.LastDigest)
	}
	data, _ := json.Marshal(dismissed)
	_ = s.deps.SettingsStore.SaveSetting("digest_banner_dismissed", string(data))

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "banner dismissed"})
}

// apiGetRegistryCredentials returns stored credentials (masked) merged with rate limit status.
func (s *Server) apiGetRegistryCredentials(w http.ResponseWriter, r *http.Request) {
	type registryInfo struct {
		Credential *RegistryCredential `json:"credential,omitempty"`
		RateLimit  *RateLimitStatus    `json:"rate_limit,omitempty"`
	}

	result := make(map[string]*registryInfo)

	// Load credentials.
	if s.deps.RegistryCredentials != nil {
		creds, err := s.deps.RegistryCredentials.GetRegistryCredentials()
		if err != nil {
			s.deps.Log.Error("failed to load registry credentials", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load registry credentials")
			return
		}
		for _, c := range creds {
			masked := c
			if len(c.Secret) > 4 {
				masked.Secret = c.Secret[:4] + "****"
			} else if c.Secret != "" {
				masked.Secret = "****"
			}
			info := &registryInfo{Credential: &masked}
			result[c.Registry] = info
		}
	}

	// Merge rate limit status.
	if s.deps.RateTracker != nil {
		for _, st := range s.deps.RateTracker.Status() {
			info, ok := result[st.Registry]
			if !ok {
				info = &registryInfo{}
				result[st.Registry] = info
			}
			stCopy := RateLimitStatus(st)
			info.RateLimit = &stCopy
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// apiSaveRegistryCredentials saves registry credentials.
func (s *Server) apiSaveRegistryCredentials(w http.ResponseWriter, r *http.Request) {
	if s.deps.RegistryCredentials == nil {
		writeError(w, http.StatusNotImplemented, "registry credentials not available")
		return
	}

	var creds []RegistryCredential
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Restore masked secrets from saved credentials.
	existing, _ := s.deps.RegistryCredentials.GetRegistryCredentials()
	savedMap := make(map[string]RegistryCredential, len(existing))
	for _, c := range existing {
		savedMap[c.ID] = c
	}
	for i, c := range creds {
		if strings.HasSuffix(c.Secret, "****") {
			if old, ok := savedMap[c.ID]; ok {
				creds[i].Secret = old.Secret
			}
		}
	}

	// Validate credentials.
	seen := make(map[string]bool, len(creds))
	for _, c := range creds {
		if strings.TrimSpace(c.Registry) == "" {
			writeError(w, http.StatusBadRequest, "registry cannot be empty")
			return
		}
		if strings.TrimSpace(c.Username) == "" {
			writeError(w, http.StatusBadRequest, "username cannot be empty for "+c.Registry)
			return
		}
		if strings.TrimSpace(c.Secret) == "" {
			writeError(w, http.StatusBadRequest, "secret cannot be empty for "+c.Registry)
			return
		}
		norm := registry.NormaliseRegistryHost(c.Registry)
		if seen[norm] {
			writeError(w, http.StatusBadRequest, "duplicate registry: "+c.Registry)
			return
		}
		seen[norm] = true
	}

	if err := s.deps.RegistryCredentials.SetRegistryCredentials(creds); err != nil {
		s.deps.Log.Error("failed to save registry credentials", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save registry credentials")
		return
	}

	s.logEvent("settings", "", "Registry credentials updated")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "registry credentials saved",
	})
}

// apiDeleteRegistryCredential removes a single credential by ID.
func (s *Server) apiDeleteRegistryCredential(w http.ResponseWriter, r *http.Request) {
	if s.deps.RegistryCredentials == nil {
		writeError(w, http.StatusNotImplemented, "registry credentials not available")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "credential id required")
		return
	}

	creds, err := s.deps.RegistryCredentials.GetRegistryCredentials()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load credentials")
		return
	}

	found := false
	filtered := make([]RegistryCredential, 0, len(creds))
	for _, c := range creds {
		if c.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, c)
	}

	if !found {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	if err := s.deps.RegistryCredentials.SetRegistryCredentials(filtered); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save credentials")
		return
	}

	s.logEvent("settings", "", "Registry credential removed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiTestRegistryCredential validates a credential by making a lightweight v2 API call.
func (s *Server) apiTestRegistryCredential(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string `json:"id"`
		Registry string `json:"registry"`
		Username string `json:"username"`
		Secret   string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Registry == "" || body.Username == "" || body.Secret == "" {
		writeError(w, http.StatusBadRequest, "registry, username, and secret are required")
		return
	}

	// If secret is masked, try to restore from saved credentials.
	if strings.HasSuffix(body.Secret, "****") && s.deps.RegistryCredentials != nil {
		existing, _ := s.deps.RegistryCredentials.GetRegistryCredentials()
		restored := false
		// Prefer lookup by ID (stable even if registry field was edited).
		if body.ID != "" {
			for _, c := range existing {
				if c.ID == body.ID {
					body.Secret = c.Secret
					restored = true
					break
				}
			}
		}
		// Fall back to registry name lookup.
		if !restored {
			for _, c := range existing {
				if c.Registry == body.Registry {
					body.Secret = c.Secret
					break
				}
			}
		}
	}

	// Test Docker Hub auth endpoint.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	authURL := "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/alpine:pull"
	if body.Registry != "docker.io" {
		// For non-Docker Hub, try GET /v2/ with basic auth.
		authURL = "https://" + body.Registry + "/v2/"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "invalid registry URL"})
		return
	}
	req.SetBasicAuth(body.Username, body.Secret)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Credentials valid"})
		return
	}
	if resp.StatusCode == http.StatusUnauthorized {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "Invalid credentials (401 Unauthorized)"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": fmt.Sprintf("Unexpected status: %d", resp.StatusCode)})
}

// apiGetRateLimits returns rate limit status for all registries (lower permission, for dashboard polling).
func (s *Server) apiGetRateLimits(w http.ResponseWriter, r *http.Request) {
	if s.deps.RateTracker == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"health":     "ok",
			"registries": []RateLimitStatus{},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"health":     s.deps.RateTracker.OverallHealth(),
		"registries": s.deps.RateTracker.Status(),
	})
}

// apiSaveStackOrder persists the user's custom stack display order.
func (s *Server) apiSaveStackOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Order []string `json:"order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.deps.SettingsStore == nil {
		writeError(w, http.StatusNotImplemented, "settings store not available")
		return
	}

	raw, err := json.Marshal(body.Order)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode order")
		return
	}

	if err := s.deps.SettingsStore.SaveSetting("stack_order", string(raw)); err != nil {
		s.deps.Log.Error("failed to save stack order", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save stack order")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// restoreSecrets returns the saved settings if the incoming settings contain any
// masked values (strings ending in "****"). This prevents overwriting real secrets
// with their masked representations when the frontend sends back unchanged channels.
func restoreSecrets(incoming, saved notify.Channel) json.RawMessage {
	var inFields map[string]interface{}
	if err := json.Unmarshal(incoming.Settings, &inFields); err != nil {
		return incoming.Settings
	}

	var savedFields map[string]interface{}
	if err := json.Unmarshal(saved.Settings, &savedFields); err != nil {
		return incoming.Settings
	}

	// Only restore individual masked fields from the saved data,
	// keeping all other fields from the incoming (user-edited) data.
	changed := false
	for k, v := range inFields {
		s, ok := v.(string)
		if ok && strings.HasSuffix(s, "****") {
			if orig, exists := savedFields[k]; exists {
				inFields[k] = orig
				changed = true
			}
		}
	}

	if !changed {
		return incoming.Settings
	}

	merged, err := json.Marshal(inFields)
	if err != nil {
		return incoming.Settings
	}
	return merged
}
