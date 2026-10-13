package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// queueResponse wraps a PendingUpdate with additional display fields.
type queueResponse struct {
	PendingUpdate
	ReleaseNotesURL  string `json:"release_notes_url,omitempty"`
	ReleaseNotesBody string `json:"release_notes_body,omitempty"`
}

// apiQueue returns all pending manual approvals, enriched with release notes URLs.
func (s *Server) apiQueue(w http.ResponseWriter, r *http.Request) {
	sources := s.loadReleaseSources()
	items := s.deps.Queue.List()
	out := make([]queueResponse, len(items))
	for i, item := range items {
		out[i] = queueResponse{PendingUpdate: item}
		if len(item.NewerVersions) > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			info := registry.FetchReleaseNotesWithSources(ctx, item.CurrentImage, item.NewerVersions[0], sources)
			cancel()
			if info != nil {
				out[i].ReleaseNotesURL = info.URL
				out[i].ReleaseNotesBody = info.Body
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// apiQueueCount returns just the number of pending items (no release notes enrichment).
func (s *Server) apiQueueCount(w http.ResponseWriter, r *http.Request) {
	count := len(s.deps.Queue.List())
	writeJSON(w, http.StatusOK, map[string]int{"count": count})
}

// apiQueueExport streams all pending queue items as CSV or JSON.
func (s *Server) apiQueueExport(w http.ResponseWriter, r *http.Request) {
	items := s.deps.Queue.List()
	if items == nil {
		items = []PendingUpdate{}
	}

	format := r.URL.Query().Get("format")
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=sentinel-queue.csv")
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"container", "current_image", "new_image", "detected_at", "type", "host_id"})
		for _, item := range items {
			newImage := ""
			if len(item.NewerVersions) > 0 {
				newImage = item.NewerVersions[0]
			}
			_ = cw.Write([]string{
				item.ContainerName,
				item.CurrentImage,
				newImage,
				item.DetectedAt.Format(time.RFC3339),
				item.Type,
				item.HostID,
			})
		}
		cw.Flush()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=sentinel-queue.json")
		_ = json.NewEncoder(w).Encode(items)
	}
}

// loadReleaseSources returns custom sources from the store, converted to registry types.
func (s *Server) loadReleaseSources() []registry.ReleaseSource {
	if s.deps.ReleaseSources == nil {
		return nil
	}
	webSrcs, err := s.deps.ReleaseSources.GetReleaseSources()
	if err != nil || len(webSrcs) == 0 {
		return nil
	}
	out := make([]registry.ReleaseSource, len(webSrcs))
	for i, src := range webSrcs {
		out[i] = registry.ReleaseSource{
			ImagePattern: src.ImagePattern,
			GitHubRepo:   src.GitHubRepo,
		}
	}
	return out
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

// remoteUpdateTarget is the image a remote agent is asked to update to. A
// digest-only update (same tag, new image behind it) has no newer tag, so
// the agent re-pulls the container's current image; agents before v3.0.0
// fail on an empty target.
func remoteUpdateTarget(currentImage, newerTarget string) string {
	if newerTarget != "" {
		return newerTarget
	}
	return currentImage
}

// apiApprove approves a pending update and triggers the update.
func (s *Server) apiApprove(w http.ResponseWriter, r *http.Request) {
	key, name := queueKeyName(r)
	if key == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	// Only the hub's own container is protected; an update for a Sentinel
	// agent on another host goes to that agent.
	var keyHost string
	if key != name {
		keyHost = strings.TrimSuffix(key, "::"+name)
	}
	if s.isQueueItemSelf(r.Context(), PendingUpdate{HostID: keyHost, ContainerName: name}) {
		writeError(w, http.StatusForbidden, "cannot approve updates for sentinel itself")
		return
	}

	// A remote update needs its host in the cluster. Refuse it, and keep
	// it queued, rather than hand a remote container to the local updater.
	if queued, ok := s.deps.Queue.Get(key); ok && queued.HostID != "" {
		if s.deps.Cluster == nil || !s.deps.Cluster.Enabled() {
			writeError(w, http.StatusConflict, "cluster mode is off: the update for "+name+" on "+queued.HostName+" cannot be applied")
			return
		}
		if _, known := s.deps.Cluster.GetHost(queued.HostID); !known {
			writeError(w, http.StatusConflict, "host "+queued.HostID+" is no longer in the cluster: the update for "+name+" cannot be applied")
			return
		}
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
		ctx := context.Background()
		start := time.Now()
		var err error
		var res RemoteUpdated
		if update.HostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
			// Remote container — dispatch to the agent via cluster.
			s.markRemoteUpdating(update.HostID, update.ContainerName)
			res, err = s.deps.Cluster.UpdateRemoteContainer(ctx, update.HostID, update.ContainerName, remoteUpdateTarget(update.CurrentImage, approveTarget), update.RemoteDigest)
			s.finishRemoteUpdating(update.HostID, update.ContainerName)
		} else if update.Type == "service" && s.deps.Swarm != nil {
			err = s.deps.Swarm.UpdateService(ctx, update.ContainerID, update.ContainerName, approveTarget)
		} else {
			err = s.deps.Updater.UpdateContainer(ctx, update.ContainerID, update.ContainerName, approveTarget)
		}
		if errors.Is(err, engine.ErrUpdateInProgress) {
			s.deps.Queue.Add(update)
			s.deps.Log.Warn("update busy, re-enqueued", "name", name)
			return
		}
		if errors.Is(err, cluster.ErrUpdateSkipped) {
			// Nothing changed; like a local skip, no history row.
			s.deps.Log.Info("remote update skipped by a pre-update hook", "name", name, "host", update.HostID)
			return
		}
		// The container the update changed, and the image it ran: the one
		// queued, unless the agent updated its own container instead.
		changed, oldImage, oldDigest := changedContainer(update.ContainerName, update.CurrentImage, update.CurrentDigest, res)
		key := update.Key()
		if changed != update.ContainerName {
			key = store.ScopedKey(update.HostID, changed)
		}
		if err != nil {
			s.deps.Log.Error("approved update failed", "name", name, "error", err)
			// Local updates record their own history inside the engine, for
			// every failure. Only a remote update is recorded here.
			if update.HostID == "" {
				return
			}
			// Remote rows are keyed "hostID::name" (update.Key()), like
			// every other remote history row; local keys are the name.
			newImage := approveTarget
			if update.HostID != "" {
				newImage = remoteUpdateTarget(update.CurrentImage, approveTarget)
			}
			_ = s.recordRemoteFailure(err, UpdateRecord{
				// An unanswered remote update's row is replaced by the
				// agent's result if it comes later.
				Timestamp:     cluster.HistoryTime(err, start),
				ContainerName: key,
				OldImage:      oldImage,
				OldDigest:     oldDigest,
				NewImage:      newImage,
				Outcome:       "failed",
				Duration:      time.Since(start),
				Error:         err.Error(),
				Type:          update.Type,
				HostID:        update.HostID,
				HostName:      update.HostName,
			})
		} else if res.Pending {
			// An agent's update of itself, under way: the hub records it.
			s.deps.Log.Info("remote self-update under way", "name", name, "host", update.HostID)
		} else if update.HostID != "" {
			// Record success for remote updates (cluster agent, swarm).
			// Local updates record their own history inside UpdateContainer().
			s.deps.Log.Info("remote update succeeded", "name", name, "host", update.HostID)
			_ = s.deps.Store.RecordUpdate(UpdateRecord{
				Timestamp:     start,
				ContainerName: key,
				OldImage:      oldImage,
				OldDigest:     oldDigest,
				NewImage:      remoteUpdateTarget(update.CurrentImage, approveTarget),
				NewDigest:     recordedNewDigest(update),
				Outcome:       "success",
				Duration:      time.Since(start),
				Type:          update.Type,
				HostID:        update.HostID,
				HostName:      update.HostName,
			})
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

	ignoredVersion := update.IgnoreKey()
	if ignoredVersion == "" {
		writeError(w, http.StatusBadRequest, "nothing to ignore: the update has no version or digest")
		return
	}
	if s.deps.IgnoredVersions != nil {
		// A remote row's versions are kept under its host-scoped key, which
		// the remote scan reads; a local row's key is the name.
		if err := s.deps.IgnoredVersions.AddIgnoredVersion(update.Key(), ignoredVersion); err != nil {
			s.deps.Log.Error("failed to save ignored version", "name", name, "version", ignoredVersion, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to save ignored version")
			return
		}
	}

	s.deps.Queue.Remove(key)
	if s.deps.NotifyState != nil {
		// Ignored, so no longer pending: it leaves the digest with the queue.
		_ = s.deps.NotifyState.ClearNotifyState(update.Key())
	}
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

// recordedNewDigest is the digest to record for an applied update. For a
// version bump the queue only holds the digest of the tag being replaced, not
// of the new one, so it records none rather than a wrong one. A same-tag
// update's remote digest is the new image.
func recordedNewDigest(u PendingUpdate) string {
	if len(u.NewerVersions) > 0 {
		return ""
	}
	return u.RemoteDigest
}
