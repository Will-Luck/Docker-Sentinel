package web

import (
	"net/http"
	"sort"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
)

// pageData is the common data structure passed to all page templates.
type pageData struct {
	Page       string
	Containers []containerView
	Stacks     []stackGroup
	Queue      []PendingUpdate
	History    []UpdateRecord
	Settings   map[string]string
	Logs       []LogEntry

	// Dashboard stats (computed by the handler).
	TotalContainers   int
	RunningContainers int
	PendingUpdates    int
	QueueCount        int // sidebar badge: number of items in queue
}

// containerView is a container with computed display fields.
type containerView struct {
	ID            string
	Name          string
	Image         string
	Tag           string // Extracted tag from image ref (e.g. "latest", "v2.19.4")
	NewestVersion string // Newest available version if update pending (semver only)
	Policy        string
	State         string
	Maintenance   bool
	HasUpdate     bool
	IsSelf        bool
	Stack         string // com.docker.compose.project label, or "" for standalone
}

// stackGroup groups containers by their Docker Compose project name.
type stackGroup struct {
	Name       string
	Containers []containerView
	HasPending bool // true if any container in the group has an update available
}

// handleDashboard renders the main container dashboard.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	containers, err := s.deps.Docker.ListContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers", "error", err)
		http.Error(w, "failed to load containers", http.StatusInternalServerError)
		return
	}

	// Build the pending update lookup for "update available" badges.
	pendingNames := make(map[string]bool)
	for _, p := range s.deps.Queue.List() {
		pendingNames[p.ContainerName] = true
	}

	views := make([]containerView, 0, len(containers))
	for _, c := range containers {
		name := containerName(c)
		maintenance, _ := s.deps.Store.GetMaintenance(name)

		policy := containerPolicy(c.Labels)
		if s.deps.Policy != nil {
			if p, ok := s.deps.Policy.GetPolicyOverride(name); ok {
				policy = p
			}
		}

		// Extract tag for compact display; fall back to last path segment.
		tag := registry.ExtractTag(c.Image)
		if tag == "" {
			if idx := strings.LastIndex(c.Image, "/"); idx >= 0 {
				tag = c.Image[idx+1:]
			} else {
				tag = c.Image
			}
		}

		// Get newest version from queue entry if available.
		var newestVersion string
		if pending, ok := s.deps.Queue.Get(name); ok && len(pending.NewerVersions) > 0 {
			newestVersion = pending.NewerVersions[0]
		}

		views = append(views, containerView{
			ID:            c.ID,
			Name:          name,
			Image:         c.Image,
			Tag:           tag,
			NewestVersion: newestVersion,
			Policy:        policy,
			State:         c.State,
			Maintenance:   maintenance,
			HasUpdate:     pendingNames[name],
			IsSelf:        c.Labels["sentinel.self"] == "true",
			Stack:         c.Labels["com.docker.compose.project"],
		})
	}

	// Compute stats for the dashboard header.
	running := 0
	pending := 0
	for _, v := range views {
		if v.State == "running" {
			running++
		}
		if v.HasUpdate {
			pending++
		}
	}

	// Group containers by stack (Docker Compose project).
	stackMap := make(map[string][]containerView)
	var stackOrder []string
	for _, v := range views {
		key := v.Stack
		if _, seen := stackMap[key]; !seen {
			stackOrder = append(stackOrder, key)
		}
		stackMap[key] = append(stackMap[key], v)
	}
	// Named stacks alphabetically, standalone ("") last.
	sort.Slice(stackOrder, func(i, j int) bool {
		if stackOrder[i] == "" {
			return false
		}
		if stackOrder[j] == "" {
			return true
		}
		return stackOrder[i] < stackOrder[j]
	})
	stacks := make([]stackGroup, 0, len(stackOrder))
	for _, key := range stackOrder {
		name := key
		if name == "" {
			name = "Standalone"
		}
		group := stackGroup{
			Name:       name,
			Containers: stackMap[key],
		}
		for _, c := range group.Containers {
			if c.HasUpdate {
				group.HasPending = true
				break
			}
		}
		stacks = append(stacks, group)
	}

	data := pageData{
		Page:              "dashboard",
		Containers:        views,
		Stacks:            stacks,
		TotalContainers:   len(views),
		RunningContainers: running,
		PendingUpdates:    pending,
		QueueCount:        len(s.deps.Queue.List()),
	}

	s.renderTemplate(w, "index.html", data)
}

// handleQueue renders the pending update queue page.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	items := s.deps.Queue.List()
	if items == nil {
		items = []PendingUpdate{}
	}

	data := pageData{
		Page:       "queue",
		Queue:      items,
		QueueCount: len(items),
	}

	s.renderTemplate(w, "queue.html", data)
}

// handleHistory renders the update history page.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	records, err := s.deps.Store.ListHistory(100)
	if err != nil {
		s.deps.Log.Error("failed to list history", "error", err)
		http.Error(w, "failed to load history", http.StatusInternalServerError)
		return
	}

	if records == nil {
		records = []UpdateRecord{}
	}

	data := pageData{
		Page:       "history",
		History:    records,
		QueueCount: len(s.deps.Queue.List()),
	}

	s.renderTemplate(w, "history.html", data)
}

// handleSettings renders the settings page.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	data := pageData{
		Page:       "settings",
		Settings:   s.deps.Config.Values(),
		QueueCount: len(s.deps.Queue.List()),
	}
	s.renderTemplate(w, "settings.html", data)
}

// handleLogs renders the activity log page.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	var logs []LogEntry
	if s.deps.EventLog != nil {
		var err error
		logs, err = s.deps.EventLog.ListLogs(200)
		if err != nil {
			s.deps.Log.Error("failed to list logs", "error", err)
		}
	}
	if logs == nil {
		logs = []LogEntry{}
	}

	data := pageData{
		Page:       "logs",
		Logs:       logs,
		QueueCount: len(s.deps.Queue.List()),
	}
	s.renderTemplate(w, "logs.html", data)
}

// apiLogs returns recent activity log entries as JSON.
func (s *Server) apiLogs(w http.ResponseWriter, r *http.Request) {
	if s.deps.EventLog == nil {
		writeJSON(w, http.StatusOK, []LogEntry{})
		return
	}
	logs, err := s.deps.EventLog.ListLogs(200)
	if err != nil {
		s.deps.Log.Error("failed to list logs", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list logs")
		return
	}
	if logs == nil {
		logs = []LogEntry{}
	}
	writeJSON(w, http.StatusOK, logs)
}

// containerDetailData holds all data for the per-container detail page.
type containerDetailData struct {
	Container    containerView
	History      []UpdateRecord
	Snapshots    []SnapshotEntry
	Versions     []string
	HasSnapshot  bool
	ChangelogURL string
}

// handleContainerDetail renders the per-container detail page.
func (s *Server) handleContainerDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		s.renderError(w, http.StatusBadRequest, "Bad Request", "Container name is required.")
		return
	}

	// Find container by name.
	containers, err := s.deps.Docker.ListContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers", "error", err)
		s.renderError(w, http.StatusInternalServerError, "Server Error", "Failed to load containers.")
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
		s.renderError(w, http.StatusNotFound, "Container Not Found", "The container \""+name+"\" was not found. It may have been removed.")
		return
	}

	maintenance, _ := s.deps.Store.GetMaintenance(name)

	detailPolicy := containerPolicy(found.Labels)
	if s.deps.Policy != nil {
		if p, ok := s.deps.Policy.GetPolicyOverride(name); ok {
			detailPolicy = p
		}
	}

	detailTag := registry.ExtractTag(found.Image)
	if detailTag == "" {
		if idx := strings.LastIndex(found.Image, "/"); idx >= 0 {
			detailTag = found.Image[idx+1:]
		} else {
			detailTag = found.Image
		}
	}

	view := containerView{
		ID:          found.ID,
		Name:        containerName(*found),
		Image:       found.Image,
		Tag:         detailTag,
		Policy:      detailPolicy,
		State:       found.State,
		Maintenance: maintenance,
		IsSelf:      found.Labels["sentinel.self"] == "true",
	}

	// Gather history.
	history, err := s.deps.Store.ListHistoryByContainer(name, 50)
	if err != nil {
		s.deps.Log.Warn("failed to list history for container", "name", name, "error", err)
	}
	if history == nil {
		history = []UpdateRecord{}
	}

	// Gather snapshots (nil-check dependency).
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

	// Gather versions (nil-check dependency).
	var versions []string
	if s.deps.Registry != nil {
		versions, err = s.deps.Registry.ListVersions(r.Context(), found.Image)
		if err != nil {
			s.deps.Log.Warn("failed to list versions", "name", name, "error", err)
			versions = nil
		}
	}

	data := containerDetailData{
		Container:    view,
		History:      history,
		Snapshots:    snapshots,
		Versions:     versions,
		HasSnapshot:  len(snapshots) > 0,
		ChangelogURL: ChangelogURL(found.Image),
	}

	s.renderTemplate(w, "container.html", data)
}

// errorPageData holds data for the error.html template.
type errorPageData struct {
	Title   string
	Message string
}

// renderTemplate executes a named template and writes the result.
func (s *Server) renderTemplate(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.deps.Log.Error("template render failed", "template", name, "error", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

// renderError renders the error page with nav bar and a link back to the dashboard.
func (s *Server) renderError(w http.ResponseWriter, status int, title, message string) {
	w.WriteHeader(status)
	s.renderTemplate(w, "error.html", errorPageData{Title: title, Message: message})
}
