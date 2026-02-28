package web

import (
	"net/http"
	"strconv"
)

// apiContainerLogs returns the last N lines of a container's logs.
func (s *Server) apiContainerLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	// Remote containers — logs not available in v1.
	if host := r.URL.Query().Get("host"); host != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"logs":   "Logs not available for remote containers.",
			"remote": true,
		})
		return
	}

	if s.deps.LogViewer == nil {
		writeError(w, http.StatusServiceUnavailable, "log viewer not available")
		return
	}

	lines := 50
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	if lines > 500 {
		lines = 500
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
