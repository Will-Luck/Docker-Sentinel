package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
)

// queueResponse wraps a PendingUpdate with additional display fields.
type queueResponse struct {
	PendingUpdate
	ReleaseNotesURL string `json:"release_notes_url,omitempty"`
}

// apiQueue returns all pending manual approvals, enriched with release notes URLs.
func (s *Server) apiQueue(w http.ResponseWriter, r *http.Request) {
	items := s.deps.Queue.List()
	out := make([]queueResponse, len(items))
	for i, item := range items {
		out[i] = queueResponse{PendingUpdate: item}
		if len(item.NewerVersions) > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			info := registry.FetchReleaseNotes(ctx, item.CurrentImage, item.NewerVersions[0])
			cancel()
			if info != nil {
				out[i].ReleaseNotesURL = info.URL
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// queueKeyName extracts the queue key and plain container name from the
// request path. The key is the full queue identifier ("hostID::name" for
// remote containers, just "name" for local ones). The name is always the
// plain container name, used for protection checks and user-facing messages.
func queueKeyName(r *http.Request) (key, name string) {
	key = r.PathValue("key")
	name = key
	if idx := strings.Index(key, "::"); idx >= 0 {
		name = key[idx+2:]
	}
	return key, name
}

// apiApprove approves a pending update and triggers the update.
func (s *Server) apiApprove(w http.ResponseWriter, r *http.Request) {
	key, name := queueKeyName(r)
	if key == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	if s.isProtectedContainer(r.Context(), name) {
		writeError(w, http.StatusForbidden, "cannot approve updates for sentinel itself")
		return
	}

	update, ok := s.deps.Queue.Approve(key)
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
	// Route to service updater, remote agent, or local container updater.
	go func() {
		var err error
		if update.HostID != "" && s.deps.Cluster.Enabled() {
			// Remote container — dispatch to the agent via cluster.
			err = s.deps.Cluster.UpdateRemoteContainer(context.Background(), update.HostID, update.ContainerName, approveTarget, update.RemoteDigest)
		} else if update.Type == "service" && s.deps.Swarm != nil {
			err = s.deps.Swarm.UpdateService(context.Background(), update.ContainerID, update.ContainerName, approveTarget)
		} else {
			err = s.deps.Updater.UpdateContainer(context.Background(), update.ContainerID, update.ContainerName, approveTarget)
		}
		if errors.Is(err, engine.ErrUpdateInProgress) {
			s.deps.Queue.Add(update)
			s.deps.Log.Warn("update busy, re-enqueued", "name", name)
			return
		}
		if err != nil {
			s.deps.Log.Error("approved update failed", "name", name, "error", err)
		}
	}()

	s.logEvent(r, "approve", name, "Update approved and started")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "approved",
		"name":    name,
		"message": "update started for " + name,
	})
}

// apiIgnoreVersion ignores a specific version for a container and removes it from the queue.
func (s *Server) apiIgnoreVersion(w http.ResponseWriter, r *http.Request) {
	key, name := queueKeyName(r)
	if key == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	update, ok := s.deps.Queue.Get(key)
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

	s.deps.Queue.Remove(key)
	s.logEvent(r, "ignore", name, "Ignored version "+ignoredVersion)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ignored",
		"name":    name,
		"version": ignoredVersion,
		"message": "version " + ignoredVersion + " ignored for " + name,
	})
}

// apiReject rejects and removes a pending update from the queue.
func (s *Server) apiReject(w http.ResponseWriter, r *http.Request) {
	key, name := queueKeyName(r)
	if key == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	s.deps.Queue.Remove(key)
	s.logEvent(r, "reject", name, "Update rejected")

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "rejected",
		"name":    name,
		"message": "update rejected for " + name,
	})
}
