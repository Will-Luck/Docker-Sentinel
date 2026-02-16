package web

import (
	"context"
	"net/http"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
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
		_ = s.deps.EventLog.AppendLog(LogEntry{
			Type:      "update",
			Message:   "service update triggered for " + name,
			Container: name,
			User:      user,
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

	go func() {
		if err := s.deps.Swarm.RollbackService(context.Background(), "", name); err != nil {
			s.deps.Log.Error("service rollback failed", "name", name, "error", err)
		}
	}()

	if s.deps.EventLog != nil {
		user := ""
		if rc := auth.GetRequestContext(r.Context()); rc != nil && rc.User != nil {
			user = rc.User.Username
		}
		_ = s.deps.EventLog.AppendLog(LogEntry{
			Type:      "rollback",
			Message:   "service rollback triggered for " + name,
			Container: name,
			User:      user,
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "rolling back"})
}
