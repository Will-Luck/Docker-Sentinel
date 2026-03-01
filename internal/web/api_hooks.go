package web

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Will-Luck/Docker-Sentinel/internal/deps"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// apiGetHooks returns hooks for a container.
func (s *Server) apiGetHooks(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("container")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}
	if s.deps.HookStore == nil {
		writeJSON(w, http.StatusOK, []HookEntry{})
		return
	}
	// For remote containers, scope the key by host to avoid collisions.
	hostID := r.URL.Query().Get("host")
	storeKey := store.ScopedKey(hostID, name)
	entries, err := s.deps.HookStore.ListHooks(storeKey)
	if err != nil {
		s.deps.Log.Error("failed to list hooks", "container", name, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list hooks")
		return
	}
	if entries == nil {
		entries = []HookEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// apiSaveHook creates or updates a hook for a container.
func (s *Server) apiSaveHook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("container")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}
	var body struct {
		Phase   string   `json:"phase"`
		Command []string `json:"command"`
		Timeout int      `json:"timeout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Phase != "pre-update" && body.Phase != "post-update" {
		writeError(w, http.StatusBadRequest, "phase must be pre-update or post-update")
		return
	}
	if len(body.Command) == 0 {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}
	if s.deps.HookStore == nil {
		writeError(w, http.StatusNotImplemented, "hook store not available")
		return
	}
	if body.Timeout <= 0 {
		body.Timeout = 30
	}
	// For remote containers, scope the key by host to avoid collisions.
	hostID := r.URL.Query().Get("host")
	storeKey := store.ScopedKey(hostID, name)
	entry := HookEntry{
		ContainerName: storeKey,
		Phase:         body.Phase,
		Command:       body.Command,
		Timeout:       body.Timeout,
	}
	if err := s.deps.HookStore.SaveHook(entry); err != nil {
		s.deps.Log.Error("failed to save hook", "container", name, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save hook")
		return
	}
	s.logEvent(r, "hooks", name, fmt.Sprintf("Saved %s hook", body.Phase))
	writeJSON(w, http.StatusOK, entry)
}

// apiDeleteHook removes a hook for a container.
func (s *Server) apiDeleteHook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("container")
	phase := r.PathValue("phase")
	if name == "" || phase == "" {
		writeError(w, http.StatusBadRequest, "container name and phase required")
		return
	}
	if s.deps.HookStore == nil {
		writeError(w, http.StatusNotImplemented, "hook store not available")
		return
	}
	// For remote containers, scope the key by host to avoid collisions.
	hostID := r.URL.Query().Get("host")
	storeKey := store.ScopedKey(hostID, name)
	if err := s.deps.HookStore.DeleteHook(storeKey, phase); err != nil {
		s.deps.Log.Error("failed to delete hook", "container", name, "phase", phase, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete hook")
		return
	}
	s.logEvent(r, "hooks", name, fmt.Sprintf("Deleted %s hook", phase))
	writeJSON(w, http.StatusOK, map[string]string{"message": "hook deleted"})
}

// apiGetDeps returns the full dependency graph.
func (s *Server) apiGetDeps(w http.ResponseWriter, r *http.Request) {
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}
	infos := make([]deps.ContainerInfo, len(containers))
	for i, c := range containers {
		infos[i] = deps.ContainerInfo{
			Name:   containerName(c),
			Labels: c.Labels,
		}
	}
	graph := deps.Build(infos)
	order, sortErr := graph.Sort()
	cycles := graph.DetectCycles()

	type depInfo struct {
		Name         string   `json:"name"`
		Dependencies []string `json:"dependencies"`
		Dependents   []string `json:"dependents"`
	}
	result := make([]depInfo, 0, len(containers))
	for _, c := range containers {
		name := containerName(c)
		result = append(result, depInfo{
			Name:         name,
			Dependencies: graph.Dependencies(name),
			Dependents:   graph.Dependents(name),
		})
	}
	resp := map[string]any{
		"containers": result,
		"order":      order,
		"has_cycles": len(cycles) > 0,
	}
	if sortErr != nil {
		resp["error"] = sortErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// apiGetContainerDeps returns dependencies for a single container.
func (s *Server) apiGetContainerDeps(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("container")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}
	infos := make([]deps.ContainerInfo, len(containers))
	for i, c := range containers {
		infos[i] = deps.ContainerInfo{
			Name:   containerName(c),
			Labels: c.Labels,
		}
	}
	graph := deps.Build(infos)
	writeJSON(w, http.StatusOK, map[string]any{
		"name":         name,
		"dependencies": graph.Dependencies(name),
		"dependents":   graph.Dependents(name),
	})
}
