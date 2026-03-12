package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/portainer"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
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
		ctx := context.Background()
		start := time.Now()
		var err error
		if strings.HasPrefix(update.HostID, "portainer:") && s.deps.Portainer != nil {
			// Portainer-managed container — route through Portainer API.
			err = s.approvePortainerUpdate(ctx, update, approveTarget)
		} else if update.HostID != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
			// Remote container — dispatch to the agent via cluster.
			s.markRemoteUpdating(update.HostID, update.ContainerName)
			err = s.deps.Cluster.UpdateRemoteContainer(ctx, update.HostID, update.ContainerName, approveTarget, update.RemoteDigest)
			time.AfterFunc(5*time.Second, func() { s.clearRemoteUpdating(update.HostID, update.ContainerName) })
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
		if err != nil {
			s.deps.Log.Error("approved update failed", "name", name, "error", err)
			_ = s.deps.Store.RecordUpdate(UpdateRecord{
				Timestamp:     start,
				ContainerName: update.ContainerName,
				OldImage:      update.CurrentImage,
				OldDigest:     update.CurrentDigest,
				NewImage:      approveTarget,
				Outcome:       "failed",
				Duration:      time.Since(start),
				Error:         err.Error(),
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

// approvePortainerUpdate routes a Portainer-managed queue approval through the
// correct Portainer API path: stack redeploy, portainer-updater self-update,
// or standalone container recreation.
func (s *Server) approvePortainerUpdate(ctx context.Context, update PendingUpdate, approveTarget string) error {
	parts := strings.SplitN(strings.TrimPrefix(update.HostID, "portainer:"), ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid portainer host format: %s", update.HostID)
	}
	instanceID := parts[0]
	epID, err := strconv.Atoi(parts[1])
	if err != nil {
		return fmt.Errorf("invalid endpoint ID %q: %w", parts[1], err)
	}

	s.markRemoteUpdating(update.HostID, update.ContainerName)
	defer time.AfterFunc(5*time.Second, func() {
		s.clearRemoteUpdating(update.HostID, update.ContainerName)
		s.deps.EventBus.Publish(events.SSEEvent{
			Type:          events.EventContainerState,
			ContainerName: update.ContainerName,
			HostID:        update.HostID,
			Timestamp:     time.Now(),
		})
	})

	// Look up the container on the Portainer endpoint to get its current state.
	containers, err := s.deps.Portainer.EndpointContainers(ctx, instanceID, epID)
	if err != nil {
		return fmt.Errorf("list portainer containers: %w", err)
	}
	var pc *PortainerContainerInfo
	for i := range containers {
		if containers[i].Name == update.ContainerName {
			pc = &containers[i]
			break
		}
	}
	if pc == nil {
		return fmt.Errorf("container %q not found on portainer endpoint %d", update.ContainerName, epID)
	}

	targetImage := approveTarget
	if targetImage == "" {
		targetImage = pc.Image
	}

	if pc.StackID != 0 {
		return s.deps.Portainer.RedeployStack(ctx, instanceID, pc.StackID, epID)
	}
	if portainer.IsPortainerImage(pc.Image) {
		return s.deps.Portainer.UpdatePortainerSelf(ctx, instanceID, epID, pc.ID, targetImage)
	}
	return s.deps.Portainer.UpdateStandaloneContainer(ctx, instanceID, epID, pc.ID, targetImage)
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
