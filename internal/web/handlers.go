package web

import "net/http"

// pageData is the common data structure passed to all page templates.
type pageData struct {
	Page       string
	Containers []containerView
	Queue      []PendingUpdate
	History    []UpdateRecord
	Settings   map[string]string

	// Dashboard stats (computed by the handler).
	TotalContainers   int
	RunningContainers int
	PendingUpdates    int
}

// containerView is a container with computed display fields.
type containerView struct {
	ID          string
	Name        string
	Image       string
	Policy      string
	State       string
	Maintenance bool
	HasUpdate   bool
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

		views = append(views, containerView{
			ID:          c.ID,
			Name:        name,
			Image:       c.Image,
			Policy:      containerPolicy(c.Labels),
			State:       c.State,
			Maintenance: maintenance,
			HasUpdate:   pendingNames[name],
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

	data := pageData{
		Page:              "dashboard",
		Containers:        views,
		TotalContainers:   len(views),
		RunningContainers: running,
		PendingUpdates:    pending,
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
		Page:  "queue",
		Queue: items,
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
		Page:    "history",
		History: records,
	}

	s.renderTemplate(w, "history.html", data)
}

// renderTemplate executes a named template and writes the result.
func (s *Server) renderTemplate(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.deps.Log.Error("template render failed", "template", name, "error", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}
