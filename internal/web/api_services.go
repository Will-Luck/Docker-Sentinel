package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
)

// apiServiceUpdate triggers an update for a Swarm service.
func (s *Server) apiServiceUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}

	if s.deps.Swarm == nil || !s.deps.Swarm.IsSwarmMode() {
		writeError(w, http.StatusBadRequest, "swarm mode not active")
		return
	}

	// Build the full target image reference (e.g. "nginx:1.29.5" not just "1.29.5").
	var targetImage string
	if pending, ok := s.deps.Queue.Get(name); ok {
		if len(pending.NewerVersions) > 0 {
			targetImage = webReplaceTag(pending.CurrentImage, pending.NewerVersions[0])
		}
	}

	// Explicit version override from form/query.
	if v := r.FormValue("version"); v != "" {
		targetImage = v
	}

	// Look up the service ID from the queue or list.
	var serviceID string
	if pending, ok := s.deps.Queue.Get(name); ok {
		serviceID = pending.ContainerID
	}
	if serviceID == "" {
		// Fall back to listing services.
		services, err := s.deps.Swarm.ListServices(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list services")
			return
		}
		for _, svc := range services {
			if svc.Name == name {
				serviceID = svc.ID
				break
			}
		}
	}
	if serviceID == "" {
		writeError(w, http.StatusNotFound, "service not found")
		return
	}

	go func() {
		// Detach from request context — the update runs long after the HTTP response.
		if err := s.deps.Swarm.UpdateService(context.Background(), serviceID, name, targetImage); err != nil {
			s.deps.Log.Error("service update failed", "name", name, "error", err)
		}
	}()

	if s.deps.EventLog != nil {
		user := ""
		if rc := auth.GetRequestContext(r.Context()); rc != nil && rc.User != nil {
			user = rc.User.Username
		}
		msg := "service update triggered for " + name
		if targetImage != "" {
			currentTag := ""
			if pending, ok := s.deps.Queue.Get(name); ok {
				currentTag = registry.ExtractTag(pending.CurrentImage)
			}
			newTag := registry.ExtractTag(targetImage)
			if currentTag != "" && newTag != "" {
				msg = fmt.Sprintf("service %s: %s → %s", name, currentTag, newTag)
			}
		}
		_ = s.deps.EventLog.AppendLog(LogEntry{
			Type:      "update",
			Message:   msg,
			Container: name,
			User:      user,
			Kind:      "service",
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updating"})
}

// apiServiceRollback triggers a Swarm native rollback.
func (s *Server) apiServiceRollback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}

	if s.deps.Swarm == nil || !s.deps.Swarm.IsSwarmMode() {
		writeError(w, http.StatusBadRequest, "swarm mode not active")
		return
	}

	user := ""
	if rc := auth.GetRequestContext(r.Context()); rc != nil && rc.User != nil {
		user = rc.User.Username
	}

	// Capture current image before rollback so history records have context.
	currentImage := ""
	if details, err := s.deps.Swarm.ListServiceDetail(r.Context()); err == nil {
		for _, d := range details {
			if d.Name == name {
				currentImage = d.Image
				break
			}
		}
	}

	go func() {
		start := time.Now()
		err := s.deps.Swarm.RollbackService(context.Background(), "", name)
		duration := time.Since(start)

		outcome := "success"
		errMsg := ""
		if err != nil {
			outcome = "failed"
			errMsg = err.Error()
			s.deps.Log.Error("service rollback failed", "name", name, "error", err)
		}

		// Record in update history so rollbacks appear on the history page.
		_ = s.deps.Store.RecordUpdate(UpdateRecord{
			Timestamp:     time.Now(),
			ContainerName: name,
			OldImage:      currentImage,
			NewImage:      "(previous version)",
			Outcome:       "rollback_" + outcome,
			Duration:      duration,
			Error:         errMsg,
			Type:          "service",
		})

		// Apply rollback policy setting — change the service's policy to prevent
		// the next scan from immediately retrying the same broken update.
		if outcome == "success" && s.deps.SettingsStore != nil && s.deps.Policy != nil {
			if rp, _ := s.deps.SettingsStore.LoadSetting("rollback_policy"); rp == "manual" || rp == "pinned" {
				_ = s.deps.Policy.SetPolicyOverride(name, rp)
				s.deps.Log.Info("policy changed after manual rollback", "name", name, "policy", rp)
			}
		}
	}()

	if s.deps.EventLog != nil {
		_ = s.deps.EventLog.AppendLog(LogEntry{
			Type:      "rollback",
			Message:   "service rollback triggered for " + name,
			Container: name,
			User:      user,
			Kind:      "service",
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "rolling back"})
}

// apiServicesList returns all Swarm services as JSON for the dashboard.
func (s *Server) apiServicesList(w http.ResponseWriter, r *http.Request) {
	if s.deps.Swarm == nil || !s.deps.Swarm.IsSwarmMode() {
		writeJSON(w, http.StatusOK, []serviceView{})
		return
	}

	details, err := s.deps.Swarm.ListServiceDetail(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list services")
		return
	}

	pendingNames := make(map[string]bool)
	for _, p := range s.deps.Queue.List() {
		pendingNames[p.ContainerName] = true
	}

	views := make([]serviceView, 0, len(details))
	for _, d := range details {
		name := d.Name
		policy := containerPolicy(d.Labels)
		if s.deps.Policy != nil {
			if p, ok := s.deps.Policy.GetPolicyOverride(name); ok {
				policy = p
			}
		}
		tag := registry.ExtractTag(d.Image)
		if tag == "" {
			if idx := strings.LastIndex(d.Image, "/"); idx >= 0 {
				tag = d.Image[idx+1:]
			} else {
				tag = d.Image
			}
		}
		var newestVersion string
		if pending, ok := s.deps.Queue.Get(name); ok && len(pending.NewerVersions) > 0 {
			newestVersion = pending.NewerVersions[0]
		}
		tasks := make([]taskView, len(d.Tasks))
		for i, t := range d.Tasks {
			tasks[i] = taskView{
				NodeName: t.NodeName,
				NodeAddr: t.NodeAddr,
				State:    t.State,
				Image:    t.Image,
				Tag:      t.Tag,
				Slot:     t.Slot,
				Error:    t.Error,
			}
		}
		sv := serviceView{
			ID:              d.ID,
			Name:            name,
			Image:           d.Image,
			Tag:             tag,
			NewestVersion:   newestVersion,
			Policy:          policy,
			HasUpdate:       pendingNames[name],
			Replicas:        d.Replicas,
			DesiredReplicas: d.DesiredReplicas,
			RunningReplicas: d.RunningReplicas,
			Registry:        registry.RegistryHost(d.Image),
			UpdateStatus:    d.UpdateStatus,
			Tasks:           tasks,
		}

		// For scaled-to-0 services, load previous replica count for display and "Scale up".
		if d.DesiredReplicas == 0 && s.deps.SettingsStore != nil {
			if saved, _ := s.deps.SettingsStore.LoadSetting("svc_prev_replicas::" + name); saved != "" {
				if n, err := strconv.ParseUint(saved, 10, 64); err == nil {
					sv.PrevReplicas = n
					sv.Replicas = fmt.Sprintf("0/%d", n)
				}
			}
		}

		views = append(views, sv)
	}
	writeJSON(w, http.StatusOK, views)
}

// apiServiceDetail returns a single Swarm service with tasks as JSON.
func (s *Server) apiServiceDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}

	if s.deps.Swarm == nil || !s.deps.Swarm.IsSwarmMode() {
		writeError(w, http.StatusBadRequest, "swarm mode not active")
		return
	}

	details, err := s.deps.Swarm.ListServiceDetail(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list services")
		return
	}

	for _, d := range details {
		if d.Name != name {
			continue
		}

		policy := containerPolicy(d.Labels)
		if s.deps.Policy != nil {
			if p, ok := s.deps.Policy.GetPolicyOverride(name); ok {
				policy = p
			}
		}
		tag := registry.ExtractTag(d.Image)
		if tag == "" {
			if idx := strings.LastIndex(d.Image, "/"); idx >= 0 {
				tag = d.Image[idx+1:]
			} else {
				tag = d.Image
			}
		}
		var newestVersion string
		if pending, ok := s.deps.Queue.Get(name); ok && len(pending.NewerVersions) > 0 {
			newestVersion = pending.NewerVersions[0]
		}
		tasks := make([]taskView, len(d.Tasks))
		for i, t := range d.Tasks {
			tasks[i] = taskView{
				NodeName: t.NodeName,
				NodeAddr: t.NodeAddr,
				State:    t.State,
				Image:    t.Image,
				Tag:      t.Tag,
				Slot:     t.Slot,
				Error:    t.Error,
			}
		}
		hasUpdate := false
		for _, p := range s.deps.Queue.List() {
			if p.ContainerName == name {
				hasUpdate = true
				break
			}
		}
		view := serviceView{
			ID:              d.ID,
			Name:            name,
			Image:           d.Image,
			Tag:             tag,
			NewestVersion:   newestVersion,
			Policy:          policy,
			HasUpdate:       hasUpdate,
			Replicas:        d.Replicas,
			DesiredReplicas: d.DesiredReplicas,
			RunningReplicas: d.RunningReplicas,
			Registry:        registry.RegistryHost(d.Image),
			UpdateStatus:    d.UpdateStatus,
			Tasks:           tasks,
		}

		// For scaled-to-0 services, load previous replica count for display and "Scale up".
		if d.DesiredReplicas == 0 && s.deps.SettingsStore != nil {
			if saved, _ := s.deps.SettingsStore.LoadSetting("svc_prev_replicas::" + name); saved != "" {
				if n, err := strconv.ParseUint(saved, 10, 64); err == nil {
					view.PrevReplicas = n
					view.Replicas = fmt.Sprintf("0/%d", n)
				}
			}
		}

		writeJSON(w, http.StatusOK, view)
		return
	}

	writeError(w, http.StatusNotFound, "service not found")
}

// apiServiceScale scales a Swarm service to the requested replica count.
func (s *Server) apiServiceScale(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}

	if s.deps.Swarm == nil || !s.deps.Swarm.IsSwarmMode() {
		writeError(w, http.StatusBadRequest, "swarm mode not active")
		return
	}

	var body struct {
		Replicas uint64 `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Before scaling, look up current desired replicas so we can remember
	// the previous count (used by "Scale up" to restore to original value).
	var prevReplicas uint64
	details, _ := s.deps.Swarm.ListServiceDetail(r.Context())
	for _, d := range details {
		if d.Name == name {
			prevReplicas = d.DesiredReplicas
			break
		}
	}

	// Scaling to 0: save previous replica count so "Scale up" can restore it.
	if body.Replicas == 0 && prevReplicas > 0 && s.deps.SettingsStore != nil {
		_ = s.deps.SettingsStore.SaveSetting("svc_prev_replicas::"+name, fmt.Sprintf("%d", prevReplicas))
	}
	// Scaling back up: clear the saved count.
	if body.Replicas > 0 && s.deps.SettingsStore != nil {
		_ = s.deps.SettingsStore.SaveSetting("svc_prev_replicas::"+name, "")
	}

	if err := s.deps.Swarm.ScaleService(r.Context(), name, body.Replicas); err != nil {
		s.deps.Log.Error("service scale failed", "name", name, "replicas", body.Replicas, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to scale service: "+err.Error())
		return
	}

	if s.deps.EventLog != nil {
		user := ""
		if rc := auth.GetRequestContext(r.Context()); rc != nil && rc.User != nil {
			user = rc.User.Username
		}
		_ = s.deps.EventLog.AppendLog(LogEntry{
			Type:      "scale",
			Message:   fmt.Sprintf("service %s scaled to %d replicas", name, body.Replicas),
			Container: name,
			User:      user,
			Kind:      "service",
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "scaled",
		"previous_replicas": prevReplicas,
	})
}
