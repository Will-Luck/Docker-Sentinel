package server

import (
	"context"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
)

// CheckAgentVersions iterates connected agents and triggers an update for any
// agent running a different version than the server. The sentinel container on
// each agent is identified by the "sentinel.self=true" label. The update is
// sent via UpdateContainerSync, which recreates the container with the new
// image tag -- the agent process is killed during the stop phase and the new
// container starts a fresh agent that reconnects automatically.
//
// Skipped when serverVersion is empty or "dev" (local/untagged builds).
func (s *Server) CheckAgentVersions(ctx context.Context, serverVersion string) {
	baseServer := baseVersion(serverVersion)
	if baseServer == "" || baseServer == "dev" {
		return
	}

	s.mu.RLock()
	// Snapshot the streams map so we don't hold the lock during RPCs.
	agents := make(map[string]*agentStream, len(s.streams))
	for id, as := range s.streams {
		agents[id] = as
	}
	s.mu.RUnlock()

	for hostID, as := range agents {
		as.mu.RLock()
		agentVer := as.version
		as.mu.RUnlock()

		baseAgent := baseVersion(agentVer)
		if baseAgent == "" || baseAgent == "dev" {
			continue // agent hasn't reported version yet, or is a dev build
		}
		if baseAgent == baseServer {
			continue // already on the right version
		}

		s.log.Info("agent version mismatch",
			"hostID", hostID,
			"agent_version", agentVer,
			"server_version", serverVersion,
		)

		s.updateAgentContainer(ctx, hostID, baseServer)
	}
}

// updateAgentContainer finds the sentinel container on the given host and
// sends an UpdateContainerRequest to bring it to the target version.
func (s *Server) updateAgentContainer(ctx context.Context, hostID, targetVersion string) {
	hs, ok := s.registry.Get(hostID)
	if !ok {
		s.log.Warn("auto-update: host not in registry", "hostID", hostID)
		return
	}

	// Snapshot containers to avoid racing with registry updates.
	containers := make([]cluster.ContainerInfo, len(hs.Containers))
	copy(containers, hs.Containers)

	// Find the sentinel container (has sentinel.self=true label).
	var sentinelName, sentinelImage string
	for _, c := range containers {
		if c.Labels["sentinel.self"] == "true" {
			sentinelName = c.Name
			sentinelImage = c.Image
			break
		}
	}

	if sentinelName == "" {
		s.log.Warn("auto-update: no sentinel container found on agent",
			"hostID", hostID,
		)
		return
	}

	newImage := replaceImageTag(sentinelImage, targetVersion)
	if newImage == sentinelImage {
		s.log.Debug("auto-update: image already matches target",
			"hostID", hostID,
			"image", sentinelImage,
		)
		return
	}

	s.log.Info("auto-update: updating agent container",
		"hostID", hostID,
		"container", sentinelName,
		"from", sentinelImage,
		"to", newImage,
	)

	// Use a generous timeout -- the agent needs to pull the new image,
	// stop, remove, and recreate the container.
	updateCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	result, err := s.UpdateContainerSync(updateCtx, hostID, sentinelName, newImage, "")
	if err != nil {
		s.log.Error("auto-update: update failed",
			"hostID", hostID,
			"container", sentinelName,
			"error", err,
		)
		return
	}

	if result.Outcome != "success" {
		s.log.Warn("auto-update: update did not succeed",
			"hostID", hostID,
			"container", sentinelName,
			"outcome", result.Outcome,
			"error", result.Error,
		)
		return
	}

	s.log.Info("auto-update: agent updated successfully",
		"hostID", hostID,
		"container", sentinelName,
		"new_image", newImage,
	)
}

// baseVersion strips the commit hash suffix from a version string.
// "v2.0.1 (abc1234)" -> "v2.0.1", "dev" -> "dev", "" -> "".
func baseVersion(v string) string {
	v = strings.TrimSpace(v)
	if idx := strings.Index(v, " ("); idx != -1 {
		return v[:idx]
	}
	return v
}

// replaceImageTag replaces the tag portion of a Docker image reference.
// "ghcr.io/foo/sentinel:v2.0.0" + "v2.0.1" -> "ghcr.io/foo/sentinel:v2.0.1"
// "sentinel:latest" + "v2.0.1" -> "sentinel:v2.0.1"
// "sentinel" + "v2.0.1" -> "sentinel:v2.0.1"
// "ghcr.io/foo/sentinel@sha256:abc123" + "v2.0.1" -> "ghcr.io/foo/sentinel:v2.0.1"
// "registry.example.com:5000/sentinel" + "v2.0.1" -> "registry.example.com:5000/sentinel:v2.0.1"
func replaceImageTag(image, newTag string) string {
	// Strip digest if present.
	if at := strings.Index(image, "@"); at != -1 {
		image = image[:at]
	}
	// Find tag colon — must be after the last slash to avoid port confusion.
	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon > lastSlash {
		return image[:lastColon] + ":" + newTag
	}
	return image + ":" + newTag
}

// AgentVersions returns a snapshot of all connected agents and their reported
// versions, keyed by host ID. Useful for dashboard display and debugging.
func (s *Server) AgentVersions() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]string, len(s.streams))
	for id, as := range s.streams {
		as.mu.RLock()
		out[id] = as.version
		as.mu.RUnlock()
	}
	return out
}
