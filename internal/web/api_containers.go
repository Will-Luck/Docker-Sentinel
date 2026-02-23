package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
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
		// Filter out Swarm task containers — they appear under Swarm Services.
		if _, isTask := c.Labels["com.docker.swarm.task"]; isTask {
			continue
		}

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

	// Append Swarm services to the container list so notification preferences
	// can reference them. Uses State="service" to distinguish from real containers.
	if s.deps.Swarm != nil && s.deps.Swarm.IsSwarmMode() {
		services, _ := s.deps.Swarm.ListServices(r.Context())
		for _, svc := range services {
			result = append(result, containerInfo{
				ID:    svc.ID,
				Name:  svc.Name,
				Image: svc.Image,
				State: "service",
				Stack: "swarm",
			})
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// apiHistory returns the most recent update records.
func (s *Server) apiHistory(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	before := r.URL.Query().Get("before")

	records, err := s.deps.Store.ListHistory(limit, before)
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

	var maintenance bool
	maintenance, err = s.deps.Store.GetMaintenance(name)
	if err != nil {
		s.deps.Log.Debug("failed to load maintenance state", "name", name, "error", err)
	}

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

// apiHistoryExport streams all history records as CSV or JSON.
func (s *Server) apiHistoryExport(w http.ResponseWriter, r *http.Request) {
	records, err := s.deps.Store.ListAllHistory()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	format := r.URL.Query().Get("format")
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=sentinel-history.csv")
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"timestamp", "container", "type", "old_image", "new_image", "outcome", "duration_s", "error", "host_id", "host_name"})
		for _, rec := range records {
			dur := ""
			if rec.Duration > 0 {
				dur = fmt.Sprintf("%.1f", rec.Duration.Seconds())
			}
			_ = cw.Write([]string{
				rec.Timestamp.Format(time.RFC3339),
				rec.ContainerName,
				rec.Type,
				rec.OldImage,
				rec.NewImage,
				rec.Outcome,
				dur,
				rec.Error,
				rec.HostID,
				rec.HostName,
			})
		}
		cw.Flush()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=sentinel-history.json")
		_ = json.NewEncoder(w).Encode(records)
	}
}

// apiTriggerScan triggers an immediate scan cycle.
func (s *Server) apiTriggerScan(w http.ResponseWriter, r *http.Request) {
	if s.deps.Scheduler == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}

	go s.deps.Scheduler.TriggerScan(context.Background())

	s.logEvent(r, "scan", "", "Manual scan triggered")
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "Scan started",
	})
}
