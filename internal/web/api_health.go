package web

import (
	"context"
	"net/http"
	"time"
)

// apiHealthz is a simple liveness probe. It always returns 200.
func (s *Server) apiHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// apiReadyz is a readiness probe. It checks the database and Docker daemon
// and returns 200 when both are reachable, or 503 when either is down.
func (s *Server) apiReadyz(w http.ResponseWriter, _ *http.Request) {
	checks := map[string]string{}
	healthy := true

	// Check database (BoltDB) via a lightweight read.
	if s.deps.SettingsStore != nil {
		_, err := s.deps.SettingsStore.LoadSetting("_healthcheck")
		if err != nil {
			checks["db"] = err.Error()
			healthy = false
		} else {
			checks["db"] = "ok"
		}
	} else {
		checks["db"] = "not configured"
		healthy = false
	}

	// Check Docker daemon connectivity.
	if s.deps.Docker != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := s.deps.Docker.ListContainers(ctx)
		cancel()
		if err != nil {
			checks["docker"] = err.Error()
			healthy = false
		} else {
			checks["docker"] = "ok"
		}
	} else {
		checks["docker"] = "not configured"
		healthy = false
	}

	status := http.StatusOK
	statusText := "ready"
	if !healthy {
		status = http.StatusServiceUnavailable
		statusText = "not_ready"
	}

	writeJSON(w, status, map[string]any{
		"status": statusText,
		"checks": checks,
	})
}
