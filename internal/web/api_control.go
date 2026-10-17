package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/selfupdate"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

var validTag = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// apiRestart restarts a container by name.
func (s *Server) apiRestart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isValidContainerName(name) {
		writeError(w, http.StatusBadRequest, "invalid container name")
		return
	}

	if s.isProtectedTarget(r, name) {
		writeError(w, http.StatusForbidden, "cannot restart sentinel itself via the dashboard")
		return
	}

	if s.deps.Restarter == nil {
		writeError(w, http.StatusNotImplemented, "restart not available")
		return
	}

	// Route to remote agent if host parameter is present.
	hostID := r.URL.Query().Get("host")
	if hostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		go func() {
			if err := s.deps.Cluster.RemoteContainerAction(context.Background(), hostID, name, "restart"); err != nil {
				s.deps.Log.Error("remote restart failed", "name", name, "host", hostID, "error", err)
				s.deps.EventBus.Publish(events.SSEEvent{
					Type:          events.EventContainerState,
					ContainerName: name,
					HostID:        hostID,
					Message:       "restart failed on " + s.hostLabel(hostID) + ": " + err.Error(),
					Timestamp:     time.Now(),
				})
			}
			// Success event is published by handleContainerActionResult.
		}()
		s.logEvent(r, "restart", name, "Container restarted on "+s.hostLabel(hostID))
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
// isRemoteSentinelAgent reports whether name on hostID is that host's
// Sentinel agent container.
func (s *Server) isRemoteSentinelAgent(hostID, name string) bool {
	if s.deps.Cluster == nil || !s.deps.Cluster.Enabled() {
		return false
	}
	for _, rc := range s.deps.Cluster.AllHostContainers() {
		if rc.HostID == hostID && rc.Name == name {
			return rc.Labels["sentinel.self"] == "true"
		}
	}
	return false
}

func (s *Server) apiStop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isValidContainerName(name) {
		writeError(w, http.StatusBadRequest, "invalid container name")
		return
	}

	if s.isProtectedTarget(r, name) {
		writeError(w, http.StatusForbidden, "cannot stop sentinel itself via the dashboard")
		return
	}
	// An agent that stops its own container cannot start it again, and
	// nothing else on that host would.
	if hostID := r.URL.Query().Get("host"); hostID != "" && s.isRemoteSentinelAgent(hostID, name) {
		writeError(w, http.StatusForbidden, "stopping a Sentinel agent from the hub would leave that host unmanaged; stop it on the host itself")
		return
	}

	if s.deps.Stopper == nil {
		writeError(w, http.StatusNotImplemented, "stop not available")
		return
	}

	// Route to remote agent if host parameter is present.
	hostID := r.URL.Query().Get("host")
	if hostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		go func() {
			if err := s.deps.Cluster.RemoteContainerAction(context.Background(), hostID, name, "stop"); err != nil {
				s.deps.Log.Error("remote stop failed", "name", name, "host", hostID, "error", err)
				s.deps.EventBus.Publish(events.SSEEvent{
					Type:          events.EventContainerState,
					ContainerName: name,
					HostID:        hostID,
					Message:       "stop failed on " + s.hostLabel(hostID) + ": " + err.Error(),
					Timestamp:     time.Now(),
				})
			}
			// Success event is published by handleContainerActionResult.
		}()
		s.logEvent(r, "stop", name, "Container stopped on "+s.hostLabel(hostID))
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
	if !isValidContainerName(name) {
		writeError(w, http.StatusBadRequest, "invalid container name")
		return
	}

	if s.isProtectedTarget(r, name) {
		writeError(w, http.StatusForbidden, "cannot start sentinel itself via the dashboard")
		return
	}

	if s.deps.Starter == nil {
		writeError(w, http.StatusNotImplemented, "start not available")
		return
	}

	// Route to remote agent if host parameter is present.
	hostID := r.URL.Query().Get("host")
	if hostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		go func() {
			if err := s.deps.Cluster.RemoteContainerAction(context.Background(), hostID, name, "start"); err != nil {
				s.deps.Log.Error("remote start failed", "name", name, "host", hostID, "error", err)
				s.deps.EventBus.Publish(events.SSEEvent{
					Type:          events.EventContainerState,
					ContainerName: name,
					HostID:        hostID,
					Message:       "start failed on " + s.hostLabel(hostID) + ": " + err.Error(),
					Timestamp:     time.Now(),
				})
			}
			// Success event is published by handleContainerActionResult.
		}()
		s.logEvent(r, "start", name, "Container started on "+s.hostLabel(hostID))
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
	if !isValidContainerName(name) {
		writeError(w, http.StatusBadRequest, "invalid container name")
		return
	}

	if s.isProtectedTarget(r, name) {
		writeError(w, http.StatusForbidden, "cannot update sentinel itself via the dashboard")
		return
	}

	// Route to remote agent if host parameter is present.
	hostID := r.URL.Query().Get("host")
	if hostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		var rc *RemoteContainer
		for _, c := range s.deps.Cluster.AllHostContainers() {
			if c.HostID == hostID && c.Name == name {
				rc = &c
				break
			}
		}
		if rc == nil {
			writeError(w, http.StatusNotFound, "remote container not found: "+name)
			return
		}

		targetImage := rc.Image
		queueKey := hostID + "::" + name
		if pending, ok := s.deps.Queue.Get(queueKey); ok && len(pending.NewerVersions) > 0 {
			targetImage = webReplaceTag(rc.Image, pending.NewerVersions[0])
		}

		s.markRemoteUpdating(hostID, name)
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					s.deps.Log.Error("panic in remote update goroutine", "name", name, "host", hostID, "panic", rec)
				}
			}()
			start := time.Now()
			res, err := s.deps.Cluster.UpdateRemoteContainer(context.Background(), hostID, name, targetImage, "")
			s.recordRemoteUpdate(start, hostID, rc.HostName, name, rc.Image, targetImage, res, err)
			if errors.Is(err, cluster.ErrUpdateSkipped) {
				s.deps.Log.Info("remote update skipped by a pre-update hook", "name", name, "host", hostID)
			} else if err != nil {
				s.deps.Log.Error("remote update failed", "name", name, "host", hostID, "error", err)
				s.tellRemoteFailure(hostID, name, "update failed on "+s.hostLabel(hostID)+": ", err)
			} else {
				s.deps.Queue.Remove(queueKey)
			}
			// Delay clearing so the SSE-triggered row fetch still sees Maintenance=true.
			// The SSE event is published by handleUpdateResult before deliverPending
			// unblocks this goroutine, but the browser needs time to receive it and
			// fetch the updated row HTML. After clearing, fire a second SSE event
			// so the row refreshes to show "Running" instead of lingering on "Updating".
			s.finishRemoteUpdating(hostID, name)
		}()

		s.logEvent(r, "update", name, "Remote update triggered on "+s.hostLabel(hostID))
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "started",
			"name":    name,
			"message": "update started for " + name,
		})
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

	// Build target image: look up the queue for a newer version (semver bump).
	// Without this, the updater re-pulls the current tag instead of the newer version.
	targetImage := ""
	if pending, ok := s.deps.Queue.Get(name); ok && len(pending.NewerVersions) > 0 {
		targetImage = webReplaceTag(pending.CurrentImage, pending.NewerVersions[0])
	}

	// Trigger update in background — detached context since r.Context() dies with the response.
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				s.deps.Log.Error("panic in local update goroutine", "name", name, "panic", rec)
			}
		}()
		err := s.deps.Updater.UpdateContainer(context.Background(), containerID, name, targetImage)
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
	if !isValidContainerName(name) {
		writeError(w, http.StatusBadRequest, "invalid container name")
		return
	}

	if s.isProtectedTarget(r, name) {
		writeError(w, http.StatusForbidden, "cannot rollback sentinel itself via the dashboard")
		return
	}

	if s.deps.Rollback == nil {
		writeError(w, http.StatusNotImplemented, "rollback not available")
		return
	}

	// Route to remote agent if host parameter is present.
	hostID := r.URL.Query().Get("host")
	if hostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		if s.remoteRollbackTarget(hostID, name) == "" {
			image := ""
			for _, c := range s.deps.Cluster.AllHostContainers() {
				if c.HostID == hostID && c.Name == name {
					image = c.Image
				}
			}
			writeError(w, http.StatusConflict, nothingToRollBackTo(image))
			return
		}
		go func() {
			if err := s.deps.Cluster.RollbackRemoteContainer(context.Background(), hostID, name); err != nil {
				s.deps.Log.Error("remote rollback failed", "name", name, "host", hostID, "error", err)
				s.tellRemoteFailure(hostID, name, "rollback failed: ", err)
				return
			}
			// Apply rollback policy if configured.
			if s.deps.Policy != nil {
				policyKey := hostID + "::" + name
				if rp := s.effectiveRollbackPolicy(); rp == "manual" || rp == "pinned" {
					_ = s.deps.Policy.SetPolicyOverride(policyKey, rp)
				}
			}
		}()

		s.logEvent(r, "rollback", name, "Remote rollback triggered on "+s.hostLabel(hostID))
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "started", "name": name, "message": "rollback started for " + name,
		})
		return
	}

	if !s.localRollbackOffered(name) {
		image := ""
		if containers, err := s.deps.Docker.ListAllContainers(r.Context()); err == nil {
			for _, c := range containers {
				if containerName(c) == name {
					image = c.Image
				}
			}
		}
		writeError(w, http.StatusConflict, nothingToRollBackTo(image))
		return
	}

	go func() {
		if err := s.deps.Rollback.RollbackContainer(context.Background(), name); err != nil {
			s.deps.Log.Error("rollback failed", "name", name, "error", err)
			return
		}
		// Apply rollback policy setting — change the container's policy to prevent
		// the next scan from immediately retrying the same broken update.
		if s.deps.Policy != nil {
			if rp := s.effectiveRollbackPolicy(); rp == "manual" || rp == "pinned" {
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
	if !isValidContainerName(name) {
		writeError(w, http.StatusBadRequest, "invalid container name")
		return
	}

	if s.deps.RegistryChecker == nil {
		writeError(w, http.StatusNotImplemented, "registry checker not available")
		return
	}

	// Route to remote container if host parameter is present.
	hostID := r.URL.Query().Get("host")
	if hostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		var rc *RemoteContainer
		for _, c := range s.deps.Cluster.AllHostContainers() {
			if c.HostID == hostID && c.Name == name {
				rc = &c
				break
			}
		}
		if rc == nil {
			writeError(w, http.StatusNotFound, "remote container not found: "+name)
			return
		}

		s.logEvent(r, "check", name, "Manual registry check for remote container on "+s.hostLabel(hostID))

		// Check against the digest the agent reported, as the scan does:
		// the hub's own Docker usually does not have the image.
		var check RemoteCheck
		var checkErr error
		if rcheck, ok := s.deps.RegistryChecker.(RemoteRegistryChecker); ok {
			check, checkErr = rcheck.CheckRemoteForUpdate(r.Context(), rc.Image, rc.ImageDigest, rc.Labels)
		} else {
			check.UpdateAvailable, check.NewerVersions, check.ResolvedCurrentVersion, check.ResolvedTargetVersion, checkErr =
				s.deps.RegistryChecker.CheckForUpdate(r.Context(), rc.Image)
		}
		if checkErr != nil {
			s.deps.Log.Warn("registry check failed for remote container", "name", name, "host", hostID, "error", checkErr)
			writeError(w, http.StatusBadGateway, "registry check failed: "+checkErr.Error())
			return
		}

		if check.UpdateAvailable {
			// Versions ignored for this remote container are kept under its
			// host-scoped key. An update with no newer tag (a new image
			// behind the same tag) is queued as such, as the scan does.
			if s.ignoredFor(hostID+"::"+name, &check) {
				writeJSON(w, http.StatusOK, map[string]any{
					"status":  "up_to_date",
					"name":    name,
					"message": name + " is up to date (newer versions ignored)",
				})
				return
			}

			s.deps.Queue.Add(PendingUpdate{
				ContainerName:          name,
				CurrentImage:           rc.Image,
				CurrentDigest:          rc.ImageDigest,
				RemoteDigest:           check.RemoteDigest,
				NewerVersions:          check.NewerVersions,
				ResolvedCurrentVersion: check.ResolvedCurrentVersion,
				ResolvedTargetVersion:  check.ResolvedTargetVersion,
				HostID:                 hostID,
				HostName:               rc.HostName,
			})
			s.deps.Log.Info("update found via manual check for remote container", "name", name, "host", hostID)

			s.deps.EventBus.Publish(events.SSEEvent{
				Type:          events.EventContainerUpdate,
				ContainerName: name,
				HostID:        hostID,
				Message:       "Update available for " + name,
				Timestamp:     time.Now(),
			})

			writeJSON(w, http.StatusOK, map[string]any{
				"status":         "update_available",
				"name":           name,
				"message":        "Update available for " + name,
				"newer_versions": check.NewerVersions,
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "up_to_date",
			"name":    name,
			"message": name + " is up to date",
		})
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
	var check RemoteCheck
	var checkErr error
	check.UpdateAvailable, check.NewerVersions, check.ResolvedCurrentVersion, check.ResolvedTargetVersion, checkErr =
		s.deps.RegistryChecker.CheckForUpdate(r.Context(), imageRef)
	if checkErr != nil {
		s.deps.Log.Warn("registry check failed", "name", name, "error", checkErr)
		writeError(w, http.StatusBadGateway, "registry check failed: "+checkErr.Error())
		return
	}

	if check.UpdateAvailable {
		// Ignored versions are filtered as the scan does. An update with no
		// newer tag (a new image behind the same tag) is queued as such.
		if s.ignoredFor(name, &check) {
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
			NewerVersions:          check.NewerVersions,
			ResolvedCurrentVersion: check.ResolvedCurrentVersion,
			ResolvedTargetVersion:  check.ResolvedTargetVersion,
		})
		s.deps.Log.Info("update found via manual check", "name", name)
		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "update_available",
			"name":           name,
			"message":        "Update available for " + name,
			"newer_versions": check.NewerVersions,
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

	// Find this Sentinel's own container (by its container ID, never by the
	// first sentinel.self label: several Sentinels can share a host) and build
	// the target image from the queue.
	var targetImage string
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("self-update: could not list containers", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list containers")
		return
	}
	self, ok := ownContainer(containers)
	if !ok {
		writeError(w, http.StatusConflict, selfupdate.ErrSelfUnknown.Error())
		return
	}
	if pending, ok := s.deps.Queue.Get(containerName(self)); ok && len(pending.NewerVersions) > 0 {
		targetImage = webReplaceTag(self.Image, pending.NewerVersions[0])
	}

	msg := "Self-update initiated — Sentinel will restart shortly"
	if targetImage != "" {
		tag := targetImage[strings.LastIndex(targetImage, ":")+1:]
		msg = "Self-update to " + tag + " initiated — Sentinel will restart shortly"
	}

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				s.deps.Log.Error("panic in self-update goroutine", "panic", rec)
				s.deps.EventBus.Publish(events.SSEEvent{
					Type:      events.EventContainerUpdate,
					Message:   fmt.Sprintf("self-update crashed: %v", rec),
					Timestamp: time.Now(),
				})
			}
		}()
		if err := s.deps.SelfUpdater.Update(context.Background(), targetImage); err != nil {
			s.deps.Log.Error("self-update failed", "error", err)
			s.deps.EventBus.Publish(events.SSEEvent{
				Type:      events.EventContainerUpdate,
				Message:   "self-update failed: " + err.Error(),
				Timestamp: time.Now(),
			})
		}
	}()

	s.logEvent(r, "self_update", "sentinel", msg)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "started",
		"message": msg,
	})
}

// apiUpdateToVersion recreates a container with an explicit version tag.
// For Sentinel containers, it routes through the self-updater helper.
func (s *Server) apiUpdateToVersion(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !isValidContainerName(name) {
		writeError(w, http.StatusBadRequest, "invalid container name")
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

	// Route to remote agent if host parameter is present.
	hostID := r.URL.Query().Get("host")
	if hostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		var rc *RemoteContainer
		for _, c := range s.deps.Cluster.AllHostContainers() {
			if c.HostID == hostID && c.Name == name {
				rc = &c
				break
			}
		}
		if rc == nil {
			writeError(w, http.StatusNotFound, "remote container not found: "+name)
			return
		}

		targetImage := webReplaceTag(rc.Image, body.Tag)

		queueKey := hostID + "::" + name
		s.markRemoteUpdating(hostID, name)
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					s.deps.Log.Error("panic in remote version-update goroutine", "name", name, "host", hostID, "tag", body.Tag, "panic", rec)
				}
			}()
			start := time.Now()
			res, err := s.deps.Cluster.UpdateRemoteContainer(context.Background(), hostID, name, targetImage, "")
			s.recordRemoteUpdate(start, hostID, rc.HostName, name, rc.Image, targetImage, res, err)
			if err != nil {
				s.deps.Log.Error("remote version update failed", "name", name, "host", hostID, "tag", body.Tag, "error", err)
				s.tellRemoteFailure(hostID, name, "update to "+body.Tag+" failed on "+s.hostLabel(hostID)+": ", err)
			} else {
				s.deps.Queue.Remove(queueKey)
			}
			s.finishRemoteUpdating(hostID, name)
		}()

		s.logEvent(r, "update_to_version", name, "Remote update to "+body.Tag+" on "+s.hostLabel(hostID))
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "started",
			"name":    name,
			"message": "Updating " + name + " to " + targetImage,
		})
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

	if s.isProtectedTarget(r, name) {
		// Only this Sentinel's own container can be updated this way. Another
		// Sentinel on the host (a hub next to an agent) is updated from its own
		// dashboard; self-updating would change the wrong container.
		if self, ok := ownContainer(containers); !ok || self.ID != containerID {
			writeError(w, http.StatusConflict, "that is another Sentinel container; update it from its own dashboard")
			return
		}
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

// recordRemoteUpdate writes the history row for an "Update now" on a remote
// container, under the scoped key every remote history row uses.
func (s *Server) recordRemoteUpdate(start time.Time, hostID, hostName, name, oldImage, newImage string, res RemoteUpdated, updateErr error) {
	// A skipped update changed nothing and leaves no row, as locally. An
	// agent's update of itself under way is recorded by the hub.
	if s.deps.Store == nil || errors.Is(updateErr, cluster.ErrUpdateSkipped) || res.Pending {
		return
	}
	name, oldImage, _ = changedContainer(name, oldImage, "", res)
	rec := UpdateRecord{
		// An unanswered update's row is replaced by the agent's result if
		// it comes later.
		Timestamp:     cluster.HistoryTime(updateErr, start),
		ContainerName: store.ScopedKey(hostID, name),
		OldImage:      oldImage,
		NewImage:      newImage,
		Outcome:       "success",
		Duration:      time.Since(start),
		HostID:        hostID,
		HostName:      hostName,
	}
	if updateErr != nil {
		rec.Outcome = "failed"
		rec.Error = updateErr.Error()
	}
	if err := s.recordRemoteFailure(updateErr, rec); err != nil {
		s.deps.Log.Warn("failed to record remote update", "name", name, "host", hostID, "error", err)
	}
}

// changedContainer returns the container a remote update of name changed,
// and the image and digest it ran: name's, unless the agent updated
// another container (its own, for a request naming another Sentinel
// container on its host).
func changedContainer(name, oldImage, oldDigest string, res RemoteUpdated) (string, string, string) {
	if res.ContainerName == "" || res.ContainerName == name {
		return name, oldImage, oldDigest
	}
	return res.ContainerName, res.OldImage, res.OldDigest
}

// historyInserter is a HistoryStore that records a row only when none
// exists at its time (store.Store.RecordUpdateIfAbsent).
type historyInserter interface {
	RecordUpdateIfAbsent(rec UpdateRecord) (bool, error)
}

// recordRemoteFailure records rec, the history row of a remote update that
// failed with err (or worked, err nil). The failure of an update the agent
// did not answer is recorded only if no row exists at its time: the
// agent's late result, recorded at the same time, may have got there
// first, and it wins.
func (s *Server) recordRemoteFailure(err error, rec UpdateRecord) error {
	if cluster.Unanswered(err) {
		if ins, ok := s.deps.Store.(historyInserter); ok {
			_, insErr := ins.RecordUpdateIfAbsent(rec)
			return insErr
		}
	}
	return s.deps.Store.RecordUpdate(rec)
}

// selfContainerID returns this process's own container ID; tests replace it.
var selfContainerID = selfupdate.SelfID

// ownContainer returns the container this Sentinel is running in: found by its
// container ID, or by SENTINEL_SELF_CONTAINER when the ID cannot be read (see
// selfupdate.ResolveSelf).
func ownContainer(containers []ContainerSummary) (ContainerSummary, bool) {
	list := make([]container.Summary, len(containers))
	for i, c := range containers {
		list[i] = container.Summary{ID: c.ID, Names: c.Names, Labels: c.Labels}
	}
	self, err := selfupdate.ResolveSelf(list, selfContainerID())
	if err != nil {
		return ContainerSummary{}, false
	}
	for _, c := range containers {
		if c.ID == self.ID {
			return c, true
		}
	}
	return ContainerSummary{}, false
}

// historyRow is the part of a history record the rollback rule reads.
func historyRow(rec UpdateRecord) cluster.HistoryRow {
	return cluster.HistoryRow{Outcome: rec.Outcome, Error: rec.Error, Type: rec.Type, OldImage: rec.OldImage}
}

// isManualRollbackRow reports whether a history row is a rollback asked for
// from the dashboard or the API (not an automatic rollback of a failed update).
func isManualRollbackRow(rec UpdateRecord) bool {
	return cluster.IsManualRollback(historyRow(rec))
}

// remoteRollbackTarget is the image a rollback of a remote container goes back
// to (cluster.RollbackTarget): the old image of the newest update that has not
// been rolled back already. It is the same rule the rollback itself uses
// (lastRemoteOldImage), so the button shows exactly when there is something
// to do. Empty when none.
func (s *Server) remoteRollbackTarget(hostID, name string) string {
	if s.deps.Store == nil {
		return ""
	}
	recs, err := s.deps.Store.ListHistoryByContainer(store.ScopedKey(hostID, name), cluster.RollbackHistoryDepth)
	if err != nil {
		return ""
	}
	rows := make([]cluster.HistoryRow, len(recs))
	for i, rec := range recs {
		rows[i] = historyRow(rec)
	}
	return cluster.RollbackTarget(rows)
}

// localRollbackOffered reports whether the local container's Rollback button
// should show: not while the newest image change is a rollback, because the
// container then already runs what the snapshot holds.
func (s *Server) localRollbackOffered(name string) bool {
	if s.deps.Store == nil {
		return true
	}
	recs, err := s.deps.Store.ListHistoryByContainer(name, 50)
	if err != nil {
		return true
	}
	for _, rec := range recs { // newest first
		switch {
		case isManualRollbackRow(rec):
			return false
		case rec.Outcome == "success" && rec.Type != "rollback":
			return true
		}
	}
	return true
}

// nothingToRollBackTo is the message of a refused rollback: the container was
// rolled back already, so going back would recreate it on the image it runs.
func nothingToRollBackTo(image string) string {
	if image == "" {
		return "nothing to roll back to: the container already runs its earlier image"
	}
	return "nothing to roll back to: the container already runs " + image
}
