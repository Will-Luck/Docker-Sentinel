package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

var validTag = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

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

	// Route to remote agent if host parameter is present.
	hostID := r.URL.Query().Get("host")
	if hostID != "" && s.deps.Cluster != nil {
		go func() {
			if err := s.deps.Cluster.RemoteContainerAction(context.Background(), hostID, name, "restart"); err != nil {
				s.deps.Log.Error("remote restart failed", "name", name, "host", hostID, "error", err)
				s.deps.EventBus.Publish(events.SSEEvent{
					Type:          events.EventContainerState,
					ContainerName: name,
					Message:       "restart failed on " + hostID + ": " + err.Error(),
					Timestamp:     time.Now(),
				})
				return
			}
			s.deps.EventBus.Publish(events.SSEEvent{
				Type:          events.EventContainerState,
				ContainerName: name,
				Message:       "Container restarted: " + name,
				Timestamp:     time.Now(),
			})
		}()
		s.logEvent(r, "restart", name, "Container restarted on "+hostID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "restarting", "name": name, "message": "restart initiated for " + name})
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
			s.deps.EventBus.Publish(events.SSEEvent{
				Type:          events.EventContainerState,
				ContainerName: name,
				Message:       "restart failed: " + err.Error(),
				Timestamp:     time.Now(),
			})
			return
		}
		s.deps.EventBus.Publish(events.SSEEvent{
			Type:          events.EventContainerState,
			ContainerName: name,
			Message:       "Container restarted: " + name,
			Timestamp:     time.Now(),
		})
	}()

	s.logEvent(r, "restart", name, "Container restarted")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "restarting",
		"name":    name,
		"message": "restart initiated for " + name,
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

	// Route to remote agent if host parameter is present.
	hostID := r.URL.Query().Get("host")
	if hostID != "" && s.deps.Cluster != nil {
		go func() {
			if err := s.deps.Cluster.RemoteContainerAction(context.Background(), hostID, name, "stop"); err != nil {
				s.deps.Log.Error("remote stop failed", "name", name, "host", hostID, "error", err)
				s.deps.EventBus.Publish(events.SSEEvent{
					Type:          events.EventContainerState,
					ContainerName: name,
					Message:       "stop failed on " + hostID + ": " + err.Error(),
					Timestamp:     time.Now(),
				})
				return
			}
			s.deps.EventBus.Publish(events.SSEEvent{
				Type:          events.EventContainerState,
				ContainerName: name,
				Message:       "Container stopped: " + name,
				Timestamp:     time.Now(),
			})
		}()
		s.logEvent(r, "stop", name, "Container stopped on "+hostID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopping", "name": name, "message": "stop initiated for " + name})
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
			s.deps.EventBus.Publish(events.SSEEvent{
				Type:          events.EventContainerState,
				ContainerName: name,
				Message:       "stop failed: " + err.Error(),
				Timestamp:     time.Now(),
			})
			return
		}
		s.deps.EventBus.Publish(events.SSEEvent{
			Type:          events.EventContainerState,
			ContainerName: name,
			Message:       "Container stopped: " + name,
			Timestamp:     time.Now(),
		})
	}()

	s.logEvent(r, "stop", name, "Container stopped")

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

	// Route to remote agent if host parameter is present.
	hostID := r.URL.Query().Get("host")
	if hostID != "" && s.deps.Cluster != nil {
		go func() {
			if err := s.deps.Cluster.RemoteContainerAction(context.Background(), hostID, name, "start"); err != nil {
				s.deps.Log.Error("remote start failed", "name", name, "host", hostID, "error", err)
				s.deps.EventBus.Publish(events.SSEEvent{
					Type:          events.EventContainerState,
					ContainerName: name,
					Message:       "start failed on " + hostID + ": " + err.Error(),
					Timestamp:     time.Now(),
				})
				return
			}
			s.deps.EventBus.Publish(events.SSEEvent{
				Type:          events.EventContainerState,
				ContainerName: name,
				Message:       "Container started: " + name,
				Timestamp:     time.Now(),
			})
		}()
		s.logEvent(r, "start", name, "Container started on "+hostID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "starting", "name": name, "message": "start initiated for " + name})
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
			s.deps.EventBus.Publish(events.SSEEvent{
				Type:          events.EventContainerState,
				ContainerName: name,
				Message:       "start failed: " + err.Error(),
				Timestamp:     time.Now(),
			})
			return
		}
		s.deps.EventBus.Publish(events.SSEEvent{
			Type:          events.EventContainerState,
			ContainerName: name,
			Message:       "Container started: " + name,
			Timestamp:     time.Now(),
		})
	}()

	s.logEvent(r, "start", name, "Container started")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "starting",
		"name":    name,
		"message": "start initiated for " + name,
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
			s.deps.EventBus.Publish(events.SSEEvent{
				Type:          events.EventContainerUpdate,
				ContainerName: name,
				Message:       "update failed: " + err.Error(),
				Timestamp:     time.Now(),
			})
		}
	}()

	s.logEvent(r, "update", name, "Manual update triggered")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "started",
		"name":    name,
		"message": "update started for " + name,
	})
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
			return
		}
		// Apply rollback policy setting — change the container's policy to prevent
		// the next scan from immediately retrying the same broken update.
		if s.deps.SettingsStore != nil && s.deps.Policy != nil {
			if rp, err := s.deps.SettingsStore.LoadSetting("rollback_policy"); err != nil {
				s.deps.Log.Error("failed to load rollback policy", "name", name, "error", err)
			} else if rp == "manual" || rp == "pinned" {
				if err := s.deps.Policy.SetPolicyOverride(name, rp); err != nil {
					s.deps.Log.Error("failed to set policy after rollback", "name", name, "policy", rp, "error", err)
				} else {
					s.deps.Log.Info("policy changed after manual rollback", "name", name, "policy", rp)
				}
			}
		}
	}()

	s.logEvent(r, "rollback", name, "Rollback triggered")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "started",
		"name":    name,
		"message": "rollback started for " + name,
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

	s.logEvent(r, "check", name, "Manual registry check triggered")

	// Run the check synchronously so the client spinner stays active.
	updateAvailable, newerVersions, resolvedCurrent, resolvedTarget, checkErr := s.deps.RegistryChecker.CheckForUpdate(r.Context(), imageRef)
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
			ContainerID:            containerID,
			ContainerName:          name,
			CurrentImage:           imageRef,
			NewerVersions:          newerVersions,
			ResolvedCurrentVersion: resolvedCurrent,
			ResolvedTargetVersion:  resolvedTarget,
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

// apiSelfUpdate triggers a self-update via an ephemeral helper container.
func (s *Server) apiSelfUpdate(w http.ResponseWriter, r *http.Request) {
	if s.deps.SelfUpdater == nil {
		writeError(w, http.StatusNotImplemented, "self-update not available")
		return
	}

	go func() {
		if err := s.deps.SelfUpdater.Update(context.Background(), ""); err != nil {
			s.deps.Log.Error("self-update failed", "error", err)
			s.deps.EventBus.Publish(events.SSEEvent{
				Type:      events.EventContainerUpdate,
				Message:   "self-update failed: " + err.Error(),
				Timestamp: time.Now(),
			})
		}
	}()

	s.logEvent(r, "self_update", "sentinel", "Self-update initiated")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "started",
		"message": "Self-update initiated — Sentinel will restart shortly",
	})
}

// apiUpdateToVersion recreates a container with an explicit version tag.
// For Sentinel containers, it routes through the self-updater helper.
func (s *Server) apiUpdateToVersion(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	var body struct {
		Tag string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Tag == "" {
		writeError(w, http.StatusBadRequest, "tag required")
		return
	}
	if !validTag.MatchString(body.Tag) {
		writeError(w, http.StatusBadRequest, "invalid tag format")
		return
	}

	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers for version update", "error", err)
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

	targetImage := webReplaceTag(imageRef, body.Tag)

	if s.isProtectedContainer(r.Context(), name) {
		// Sentinel — route through self-updater helper container.
		if s.deps.SelfUpdater == nil {
			writeError(w, http.StatusNotImplemented, "self-update not available")
			return
		}
		go func() {
			if err := s.deps.SelfUpdater.Update(context.Background(), targetImage); err != nil {
				s.deps.Log.Error("self-update to version failed", "tag", body.Tag, "error", err)
				s.deps.EventBus.Publish(events.SSEEvent{
					Type:      events.EventContainerUpdate,
					Message:   "self-update to " + body.Tag + " failed: " + err.Error(),
					Timestamp: time.Now(),
				})
			}
		}()
		s.logEvent(r, "self_update", name, "Self-update to "+body.Tag+" initiated")
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "started",
			"name":    name,
			"message": "Self-update to " + body.Tag + " initiated — Sentinel will restart shortly",
		})
		return
	}

	// Regular container — use the standard update lifecycle.
	go func() {
		err := s.deps.Updater.UpdateContainer(context.Background(), containerID, name, targetImage)
		if errors.Is(err, engine.ErrUpdateInProgress) {
			s.deps.Log.Warn("version update skipped, already in progress", "name", name)
			s.deps.EventBus.Publish(events.SSEEvent{
				Type:          events.EventContainerUpdate,
				ContainerName: name,
				Message:       "update already in progress for " + name,
				Timestamp:     time.Now(),
			})
			return
		}
		if err != nil {
			s.deps.Log.Error("version update failed", "name", name, "tag", body.Tag, "error", err)
			s.deps.EventBus.Publish(events.SSEEvent{
				Type:          events.EventContainerUpdate,
				ContainerName: name,
				Message:       "update to " + body.Tag + " failed: " + err.Error(),
				Timestamp:     time.Now(),
			})
		}
	}()

	s.logEvent(r, "update_to_version", name, "Update to "+body.Tag+" triggered")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "started",
		"name":    name,
		"message": "Updating " + name + " to " + targetImage,
	})
}
