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
// sent via UpdateContainerSync. The agent detects its own container and uses
// rename-before-replace: rename self out of the way, create new container with
// the original name, start it. The old agent process exits naturally.
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
		agentVer := s.agentVersion(hostID, as)

		if !agentNeedsUpdate(agentVer, serverVersion) {
			continue
		}

		s.log.Info("agent version mismatch",
			"hostID", hostID,
			"agent_version", agentVer,
			"server_version", serverVersion,
		)

		s.updateAgentContainer(ctx, hostID, imageTagForVersion(baseServer))
	}
}

// imageTagForVersion maps a release version ("v2.16.1") to the image tag
// the release workflow publishes for it ("2.16.1"). Git tags carry the
// "v"; the semver image tags do not.
func imageTagForVersion(v string) string {
	return strings.TrimPrefix(v, "v")
}

// updateAgentContainer finds the sentinel container on the given host and
// sends an UpdateContainerRequest to bring it to the target version.
func (s *Server) updateAgentContainer(ctx context.Context, hostID, targetTag string) {
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

	newImage := replaceImageTag(sentinelImage, targetTag)
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

// agentNeedsUpdate reports whether an agent on agentVer should be moved to
// the hub's serverVersion. Dev builds and agents that have not reported a
// version yet are left alone.
func agentNeedsUpdate(agentVer, serverVersion string) bool {
	baseAgent := baseVersion(agentVer)
	baseServer := baseVersion(serverVersion)
	if baseAgent == "" || baseAgent == "dev" || baseServer == "" || baseServer == "dev" {
		return false
	}
	return imageTagForVersion(baseAgent) != imageTagForVersion(baseServer)
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
		out[id] = s.agentVersion(id, as)
	}
	return out
}

// agentVersion is the version of the agent on stream as: from its last
// heartbeat, else from the state report it sent before opening the stream
// (a hub that has just restarted hears that first).
func (s *Server) agentVersion(hostID string, as *agentStream) string {
	as.mu.RLock()
	v := as.version
	as.mu.RUnlock()
	if v == "" {
		if hs, ok := s.registry.Get(hostID); ok {
			v = hs.Info.AgentVersion
		}
	}
	return v
}
