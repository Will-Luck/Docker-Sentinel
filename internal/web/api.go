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
		Policy:      containerPolicy(found.Labels),
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

	if s.deps.Rollback == nil {
		writeError(w, http.StatusNotImplemented, "rollback not available")
		return
	}

	go func() {
		if err := s.deps.Rollback.RollbackContainer(context.Background(), name); err != nil {
			s.deps.Log.Error("rollback failed", "name", name, "error", err)
		}
	}()

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "started",
		"name":    name,
		"message": "rollback started for " + name,
	})
}

// apiChangePolicy changes the update policy for a container.
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

	go func() {
		if err := s.deps.Policy.ChangePolicy(context.Background(), name, body.Policy); err != nil {
			s.deps.Log.Error("policy change failed", "name", name, "policy", body.Policy, "error", err)
		}
	}()

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "started",
		"name":    name,
		"message": "policy change to " + body.Policy + " started for " + name,
	})
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
