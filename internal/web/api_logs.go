package web

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// apiContainerLogs returns the last N lines of a container's logs.
func (s *Server) apiContainerLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	// Parse lines early so it's available for both local and remote paths.
	lines := 50
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	if lines > 500 {
		lines = 500
	}

	// Remote containers — fetch logs via cluster gRPC.
	if host := r.URL.Query().Get("host"); host != "" {
		if s.deps.Cluster == nil || !s.deps.Cluster.Enabled() {
			writeError(w, http.StatusServiceUnavailable, "cluster not available")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		output, err := s.deps.Cluster.RemoteContainerLogs(ctx, host, name, lines)
		if err != nil {
			s.deps.Log.Error("remote logs failed", "name", name, "host", host, "error", err)
			writeError(w, http.StatusBadGateway, "failed to fetch remote logs: "+err.Error())
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"logs":   output,
			"lines":  lines,
			"remote": true,
		})
		return
	}

	if s.deps.LogViewer == nil {
		writeError(w, http.StatusServiceUnavailable, "log viewer not available")
		return
	}

	// Resolve container ID from name.
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	var containerID string
	for _, c := range containers {
		for _, n := range c.Names {
			cname := n
			if len(cname) > 0 && cname[0] == '/' {
				cname = cname[1:]
			}
			if cname == name {
				containerID = c.ID
				break
			}
		}
		if containerID != "" {
			break
		}
	}

	if containerID == "" {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}

	output, err := s.deps.LogViewer.ContainerLogs(r.Context(), containerID, lines)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch logs: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"logs":   output,
		"lines":  lines,
		"remote": false,
	})
}
