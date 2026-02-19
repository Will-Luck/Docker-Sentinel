package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
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

	// Swarm services — separate section below standalone containers.
	SwarmServices []serviceView

	// Dashboard stats (computed by the handler).
	TotalContainers   int
	RunningContainers int
	PendingUpdates    int
	QueueCount        int // sidebar badge: number of items in queue

	// Cluster state (populated by withCluster helper).
	ClusterEnabled bool
	ClusterHosts   []ClusterHost // populated only on /cluster page
	HostGroups     []hostGroup   // populated only on dashboard when cluster active (Task 9)

	// Cluster page stats (populated by handleCluster).
	ClusterConnectedCount int
	ClusterContainerCount int
	ServerVersion         string

	// Auth context (populated by withAuth helper).
	CurrentUser *auth.User
	AuthEnabled bool
	CSRFToken   string
}

// hostGroup groups containers by host when cluster mode is active.
// "local" is this server; remote hosts come from the cluster provider.
type hostGroup struct {
	ID        string       // "local" or host ID
	Name      string       // display name
	Connected bool         // always true for local
	Stacks    []stackGroup // existing stack grouping within this host
	Count     int          // total container count
}

// containerView is a container or Swarm service with computed display fields.
type containerView struct {
	ID              string
	Name            string
	Image           string
	Tag             string // Extracted tag from image ref (e.g. "latest", "v2.19.4")
	ResolvedVersion string // Actual semver behind non-version tags (e.g. "v2.34.2" for "latest")
	NewestVersion   string // Newest available version if update pending (semver only)
	Policy          string
	State           string
	Maintenance     bool
	HasUpdate       bool
	IsSelf          bool
	Stack           string // com.docker.compose.project label, or "" for standalone
	Registry        string // Registry host (e.g. "docker.io", "ghcr.io", "lscr.io")
	IsService       bool   // true for Swarm services
	Replicas        string // e.g. "3/3" for services, empty for containers
	HostID          string // cluster host ID (empty = local)
	HostName        string // cluster host name (empty = local)
}

// stackGroup groups containers by their Docker Compose project name.
type stackGroup struct {
	Name         string
	Containers   []containerView
	HasPending   bool // true if any container in the group has an update available
	RunningCount int
	StoppedCount int
	PendingCount int
}

// serviceView is a Swarm service row for the dashboard template.
type serviceView struct {
	ID              string
	Name            string
	Image           string
	Tag             string
	ResolvedVersion string // Actual semver behind non-version tags (e.g. "v2.34.2" for "latest")
	NewestVersion   string
	Policy          string
	HasUpdate       bool
	Replicas        string
	DesiredReplicas uint64
	RunningReplicas uint64
	PrevReplicas    uint64 // Previous desired replicas (for "Scale up" after scale-to-0)
	Registry        string
	UpdateStatus    string
	Tasks           []taskView
	ChangelogURL    string `json:"ChangelogURL,omitempty"` // Pre-computed changelog link for JS row updates
	VersionURL      string `json:"VersionURL,omitempty"`   // Pre-computed version-specific link for JS row updates
}

// taskView is a single Swarm task (replica) row nested under a service.
type taskView struct {
	NodeName string
	NodeAddr string
	State    string
	Image    string
	Tag      string
	Slot     int
	Error    string
}

// buildServiceView constructs a serviceView from a ServiceDetail, resolving
// policy overrides, tag extraction, queue state, and prev-replicas. Used by
// the dashboard, API list, API detail, and service detail page handlers.
func (s *Server) buildServiceView(d ServiceDetail, pendingNames map[string]bool) serviceView {
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
	var newestVersion, resolved string
	if pending, ok := s.deps.Queue.Get(name); ok {
		if len(pending.NewerVersions) > 0 {
			newestVersion = pending.NewerVersions[0]
		}
		// For services, image labels aren't in the service spec — use the
		// digest-resolved version from the registry check instead.
		if _, isSemver := registry.ParseSemVer(tag); !isSemver {
			if pending.ResolvedCurrentVersion != "" && pending.ResolvedCurrentVersion != tag {
				resolved = pending.ResolvedCurrentVersion
			}
		}
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
	// Pre-compute URLs so the JS SSE handler can build proper links
	// without duplicating the URL logic client-side.
	changelogLink := ChangelogURL(d.Image)
	var versionLink string
	if newestVersion != "" {
		versionLink = VersionURL(d.Image, newestVersion)
	}

	sv := serviceView{
		ID:              d.ID,
		Name:            name,
		Image:           d.Image,
		Tag:             tag,
		ResolvedVersion: resolved,
		NewestVersion:   newestVersion,
		Policy:          policy,
		HasUpdate:       pendingNames[name],
		Replicas:        d.Replicas,
		DesiredReplicas: d.DesiredReplicas,
		RunningReplicas: d.RunningReplicas,
		Registry:        registry.RegistryHost(d.Image),
		UpdateStatus:    d.UpdateStatus,
		Tasks:           tasks,
		ChangelogURL:    changelogLink,
		VersionURL:      versionLink,
	}
	// For scaled-to-0 services, load previous replica count so "Scale up"
	// can restore to the original value instead of defaulting to 1,
	// and show "0/3" instead of "0/0" in the badge.
	if sv.DesiredReplicas == 0 && s.deps.SettingsStore != nil {
		if saved, _ := s.deps.SettingsStore.LoadSetting("svc_prev_replicas::" + name); saved != "" {
			if n, err := strconv.ParseUint(saved, 10, 64); err == nil {
				sv.PrevReplicas = n
				sv.Replicas = fmt.Sprintf("0/%d", n)
			}
		}
	}
	return sv
}

// handleDashboard renders the main container dashboard.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
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
		// Filter out Swarm task containers — they'll appear in the Swarm Services section.
		if _, isTask := c.Labels["com.docker.swarm.task"]; isTask {
			continue
		}

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

		// Resolve the actual version behind non-semver tags like "latest".
		// Docker merges image labels into container labels at creation time,
		// so org.opencontainers.image.version is available without extra API calls.
		var resolved string
		if _, isSemver := registry.ParseSemVer(tag); !isSemver {
			if v := c.Labels["org.opencontainers.image.version"]; v != "" && v != tag {
				resolved = v
			}
		}

		views = append(views, containerView{
			ID:              c.ID,
			Name:            name,
			Image:           c.Image,
			Tag:             tag,
			ResolvedVersion: resolved,
			NewestVersion:   newestVersion,
			Policy:          policy,
			State:           c.State,
			Maintenance:     maintenance,
			HasUpdate:       pendingNames[name],
			IsSelf:          c.Labels["sentinel.self"] == "true",
			Stack:           c.Labels["com.docker.compose.project"],
			Registry:        registry.RegistryHost(c.Image),
		})
	}

	// Build Swarm Services section if available.
	var svcViews []serviceView
	if s.deps.Swarm != nil && s.deps.Swarm.IsSwarmMode() {
		details, svcErr := s.deps.Swarm.ListServiceDetail(r.Context())
		if svcErr != nil {
			s.deps.Log.Warn("failed to list service details", "error", svcErr)
		}
		for _, d := range details {
			svcViews = append(svcViews, s.buildServiceView(d, pendingNames))
		}
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
	for _, sv := range svcViews {
		if sv.RunningReplicas > 0 {
			running++
		}
		if sv.HasUpdate {
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
	// Apply saved stack order if available, falling back to alphabetical.
	savedJSON, _ := s.deps.SettingsStore.LoadSetting("stack_order")
	var savedOrder []string
	if savedJSON != "" {
		if err := json.Unmarshal([]byte(savedJSON), &savedOrder); err != nil {
			s.deps.Log.Warn("failed to parse saved stack order, using defaults", "error", err)
			savedOrder = nil
		}
	}
	if len(savedOrder) > 0 {
		rank := make(map[string]int, len(savedOrder))
		for i, name := range savedOrder {
			rank[name] = i
		}
		nextRank := len(savedOrder)
		sort.SliceStable(stackOrder, func(i, j int) bool {
			// Standalone ("") always last.
			if stackOrder[i] == "" {
				return false
			}
			if stackOrder[j] == "" {
				return true
			}
			nameI := stackOrder[i]
			nameJ := stackOrder[j]
			ri, okI := rank[nameI]
			rj, okJ := rank[nameJ]
			if !okI {
				ri = nextRank
			}
			if !okJ {
				rj = nextRank
			}
			if ri != rj {
				return ri < rj
			}
			// Both unsaved — alphabetical.
			return nameI < nameJ
		})
	} else {
		// Default: named stacks alphabetically, standalone ("") last.
		sort.Slice(stackOrder, func(i, j int) bool {
			if stackOrder[i] == "" {
				return false
			}
			if stackOrder[j] == "" {
				return true
			}
			return stackOrder[i] < stackOrder[j]
		})
	}
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
			if c.State == "running" {
				group.RunningCount++
			} else {
				group.StoppedCount++
			}
			if c.HasUpdate {
				group.HasPending = true
				group.PendingCount++
			}
		}
		stacks = append(stacks, group)
	}

	data := pageData{
		Page:              "dashboard",
		Containers:        views,
		Stacks:            stacks,
		SwarmServices:     svcViews,
		TotalContainers:   len(views) + len(svcViews),
		RunningContainers: running,
		PendingUpdates:    pending,
		QueueCount:        len(s.deps.Queue.List()),
	}

	// Build host groups when cluster mode is active. Each host gets its own
	// accordion section on the dashboard containing its stacks/containers.
	if s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		// "local" group — this server's containers.
		localGroup := hostGroup{
			ID:        "local",
			Name:      "local",
			Connected: true,
			Stacks:    data.Stacks,
			Count:     len(data.Containers),
		}
		data.HostGroups = []hostGroup{localGroup}

		// Remote groups from cluster.
		hosts := s.deps.Cluster.AllHosts()
		remoteContainers := s.deps.Cluster.AllHostContainers()

		// Group remote containers by host ID, extracting tag/registry
		// the same way we do for local containers.
		byHost := make(map[string][]containerView)
		for _, rc := range remoteContainers {
			tag := registry.ExtractTag(rc.Image)
			if tag == "" {
				if idx := strings.LastIndex(rc.Image, "/"); idx >= 0 {
					tag = rc.Image[idx+1:]
				} else {
					tag = rc.Image
				}
			}
			// Resolve policy the same way we do for local containers:
			// label first, then DB override.
			policy := containerPolicy(rc.Labels)
			if s.deps.Policy != nil {
				if p, ok := s.deps.Policy.GetPolicyOverride(rc.Name); ok {
					policy = p
				}
			}

			cv := containerView{
				Name:     rc.Name,
				Image:    rc.Image,
				Tag:      tag,
				Registry: registry.RegistryHost(rc.Image),
				Policy:   policy,
				State:    rc.State,
				HostID:   rc.HostID,
				HostName: rc.HostName,
			}
			byHost[rc.HostID] = append(byHost[rc.HostID], cv)
		}

		for _, h := range hosts {
			containers := byHost[h.ID]
			// Build a single "Standalone" stack for remote containers
			// (we don't have stack labels for remote containers yet).
			var remoteStacks []stackGroup
			if len(containers) > 0 {
				sg := stackGroup{
					Name:       "Standalone",
					Containers: containers,
				}
				for _, c := range containers {
					if c.State == "running" {
						sg.RunningCount++
					} else {
						sg.StoppedCount++
					}
				}
				remoteStacks = []stackGroup{sg}
			}
			data.HostGroups = append(data.HostGroups, hostGroup{
				ID:        h.ID,
				Name:      h.Name,
				Connected: h.Connected,
				Stacks:    remoteStacks,
				Count:     len(containers),
			})

			// Update fleet-wide totals.
			data.TotalContainers += len(containers)
			for _, c := range containers {
				if c.State == "running" {
					data.RunningContainers++
				}
			}
		}
	}

	s.withAuth(r, &data)
	s.withCluster(&data)

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
	s.withAuth(r, &data)
	s.withCluster(&data)

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
	s.withAuth(r, &data)
	s.withCluster(&data)

	s.renderTemplate(w, "history.html", data)
}

// handleSettings renders the settings page.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	data := pageData{
		Page:       "settings",
		Settings:   s.deps.Config.Values(),
		QueueCount: len(s.deps.Queue.List()),
	}
	s.withAuth(r, &data)
	s.withCluster(&data)
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
	s.withAuth(r, &data)
	s.withCluster(&data)
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

// handleContainerRow returns a single container's table row HTML plus dashboard stats.
// Used by the frontend to do targeted row replacement instead of full page reloads.
func (s *Server) handleContainerRow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}

	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	pendingNames := make(map[string]bool)
	for _, p := range s.deps.Queue.List() {
		pendingNames[p.ContainerName] = true
	}

	var targetView *containerView
	running, pending := 0, 0
	for _, c := range containers {
		n := containerName(c)
		if c.State == "running" {
			running++
		}
		if pendingNames[n] {
			pending++
		}
		if n == name {
			maintenance, _ := s.deps.Store.GetMaintenance(n)
			policy := containerPolicy(c.Labels)
			if s.deps.Policy != nil {
				if p, ok := s.deps.Policy.GetPolicyOverride(n); ok {
					policy = p
				}
			}
			tag := registry.ExtractTag(c.Image)
			if tag == "" {
				if idx := strings.LastIndex(c.Image, "/"); idx >= 0 {
					tag = c.Image[idx+1:]
				} else {
					tag = c.Image
				}
			}
			var newestVersion string
			if pend, ok := s.deps.Queue.Get(n); ok && len(pend.NewerVersions) > 0 {
				newestVersion = pend.NewerVersions[0]
			}
			v := containerView{
				ID:            c.ID,
				Name:          n,
				Image:         c.Image,
				Tag:           tag,
				NewestVersion: newestVersion,
				Policy:        policy,
				State:         c.State,
				Maintenance:   maintenance,
				HasUpdate:     pendingNames[n],
				IsSelf:        c.Labels["sentinel.self"] == "true",
				Stack:         c.Labels["com.docker.compose.project"],
				Registry:      registry.RegistryHost(c.Image),
			}
			targetView = &v
		}
	}

	// Fallback: search cluster remote containers if not found locally.
	if targetView == nil && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		for _, rc := range s.deps.Cluster.AllHostContainers() {
			if rc.Name == name {
				policy := containerPolicy(rc.Labels)
				if s.deps.Policy != nil {
					if p, ok := s.deps.Policy.GetPolicyOverride(rc.Name); ok {
						policy = p
					}
				}
				tag := registry.ExtractTag(rc.Image)
				if tag == "" {
					if idx := strings.LastIndex(rc.Image, "/"); idx >= 0 {
						tag = rc.Image[idx+1:]
					} else {
						tag = rc.Image
					}
				}
				v := containerView{
					Name:     rc.Name,
					Image:    rc.Image,
					Tag:      tag,
					Policy:   policy,
					State:    rc.State,
					HostID:   rc.HostID,
					HostName: rc.HostName,
					Registry: registry.RegistryHost(rc.Image),
				}
				targetView = &v
				break
			}
		}
	}

	if targetView == nil {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}

	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "container-row", targetView); err != nil {
		s.deps.Log.Error("failed to render container row", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "render failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"html":    buf.String(),
		"total":   len(containers),
		"running": running,
		"pending": pending,
	})
}

// handleDashboardStats returns lightweight container counts for live stat card updates.
func (s *Server) handleDashboardStats(w http.ResponseWriter, r *http.Request) {
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	pendingNames := make(map[string]bool)
	for _, p := range s.deps.Queue.List() {
		pendingNames[p.ContainerName] = true
	}

	total, running, pending := len(containers), 0, 0
	for _, c := range containers {
		if c.State == "running" {
			running++
		}
		if pendingNames[containerName(c)] {
			pending++
		}
	}

	// Include Swarm services in total count.
	if s.deps.Swarm != nil && s.deps.Swarm.IsSwarmMode() {
		services, err := s.deps.Swarm.ListServices(r.Context())
		if err == nil {
			total += len(services)
			for _, svc := range services {
				if pendingNames[svc.Name] {
					pending++
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":   total,
		"running": running,
		"pending": pending,
	})
}

// handleCluster renders the cluster management page with host cards and enrollment.
func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	if !s.deps.Cluster.Enabled() {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}

	hosts := s.deps.Cluster.AllHosts()

	// Mark connected state from the live connected set.
	connected := s.deps.Cluster.ConnectedHosts()
	connectedSet := make(map[string]bool, len(connected))
	for _, id := range connected {
		connectedSet[id] = true
	}
	for i := range hosts {
		hosts[i].Connected = connectedSet[hosts[i].ID]
	}

	// Compute aggregate stats for stat cards.
	connectedCount := 0
	containerCount := 0
	for _, h := range hosts {
		if h.Connected {
			connectedCount++
		}
		containerCount += h.Containers
	}

	data := pageData{
		Page:                  "cluster",
		QueueCount:            len(s.deps.Queue.List()),
		ClusterHosts:          hosts,
		ClusterConnectedCount: connectedCount,
		ClusterContainerCount: containerCount,
		ServerVersion:         s.deps.Version,
	}
	s.withAuth(r, &data)
	s.withCluster(&data)
	s.renderTemplate(w, "cluster.html", data)
}

// containerDetailData holds all data for the per-container detail page.
type containerDetailData struct {
	Container    containerView
	History      []UpdateRecord
	Snapshots    []SnapshotEntry
	Versions     []string
	HasSnapshot  bool
	ChangelogURL string

	ClusterEnabled bool

	// Auth context.
	CurrentUser *auth.User
	AuthEnabled bool
	CSRFToken   string
}

// withCluster populates ClusterEnabled by checking the controller
// and falling back to the DB setting.
func (s *Server) withCluster(data *pageData) {
	if s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		data.ClusterEnabled = true
		return
	}
	if s.deps.SettingsStore != nil {
		v, _ := s.deps.SettingsStore.LoadSetting(store.SettingClusterEnabled)
		data.ClusterEnabled = v == "true"
	}
}

// withAuth populates auth context fields on pageData from the request.
func (s *Server) withAuth(r *http.Request, data *pageData) {
	rc := auth.GetRequestContext(r.Context())
	if rc != nil {
		data.CurrentUser = rc.User
		data.AuthEnabled = rc.AuthEnabled
	}
	if cookie, err := r.Cookie(auth.CSRFCookieName); err == nil {
		data.CSRFToken = cookie.Value
	}
}

// withAuthDetail populates auth context fields on containerDetailData from the request.
func (s *Server) withAuthDetail(r *http.Request, data *containerDetailData) {
	rc := auth.GetRequestContext(r.Context())
	if rc != nil {
		data.CurrentUser = rc.User
		data.AuthEnabled = rc.AuthEnabled
	}
	if cookie, err := r.Cookie(auth.CSRFCookieName); err == nil {
		data.CSRFToken = cookie.Value
	}
}

// handleContainerDetail renders the per-container detail page.
func (s *Server) handleContainerDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		s.renderError(w, http.StatusBadRequest, "Bad Request", "Container name is required.")
		return
	}

	// Find container by name.
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
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

	// Resolve the actual version behind non-semver tags like "latest".
	var detailResolved string
	if _, isSemver := registry.ParseSemVer(detailTag); !isSemver {
		if v := found.Labels["org.opencontainers.image.version"]; v != "" && v != detailTag {
			detailResolved = v
		}
	}

	view := containerView{
		ID:              found.ID,
		Name:            containerName(*found),
		Image:           found.Image,
		Tag:             detailTag,
		ResolvedVersion: detailResolved,
		Policy:          detailPolicy,
		State:           found.State,
		Maintenance:     maintenance,
		IsSelf:          found.Labels["sentinel.self"] == "true",
		Registry:        registry.RegistryHost(found.Image),
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
		Container:      view,
		History:        history,
		Snapshots:      snapshots,
		Versions:       versions,
		HasSnapshot:    len(snapshots) > 0,
		ChangelogURL:   ChangelogURL(found.Image),
		ClusterEnabled: s.deps.Cluster != nil && s.deps.Cluster.Enabled(),
	}
	s.withAuthDetail(r, &data)

	s.renderTemplate(w, "container.html", data)
}

// serviceDetailData holds all data for the per-service detail page.
type serviceDetailData struct {
	Service      serviceView
	History      []UpdateRecord
	Versions     []string
	ChangelogURL string

	ClusterEnabled bool

	// Auth context.
	CurrentUser *auth.User
	AuthEnabled bool
	CSRFToken   string
}

// withAuthServiceDetail populates auth context on serviceDetailData.
func (s *Server) withAuthServiceDetail(r *http.Request, data *serviceDetailData) {
	rc := auth.GetRequestContext(r.Context())
	if rc != nil {
		data.CurrentUser = rc.User
		data.AuthEnabled = rc.AuthEnabled
	}
	if cookie, err := r.Cookie(auth.CSRFCookieName); err == nil {
		data.CSRFToken = cookie.Value
	}
}

// handleServiceDetail renders the per-service detail page.
func (s *Server) handleServiceDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		s.renderError(w, http.StatusBadRequest, "Bad Request", "Service name is required.")
		return
	}

	if s.deps.Swarm == nil || !s.deps.Swarm.IsSwarmMode() {
		s.renderError(w, http.StatusBadRequest, "Not Available", "Swarm mode is not active.")
		return
	}

	details, err := s.deps.Swarm.ListServiceDetail(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list service details", "error", err)
		s.renderError(w, http.StatusInternalServerError, "Server Error", "Failed to load services.")
		return
	}

	var found *ServiceDetail
	for i := range details {
		if details[i].Name == name {
			found = &details[i]
			break
		}
	}
	if found == nil {
		s.renderError(w, http.StatusNotFound, "Service Not Found", "The service \""+name+"\" was not found.")
		return
	}

	pendingNames := make(map[string]bool)
	for _, p := range s.deps.Queue.List() {
		pendingNames[p.ContainerName] = true
	}
	view := s.buildServiceView(*found, pendingNames)

	// Gather history.
	history, err := s.deps.Store.ListHistoryByContainer(name, 50)
	if err != nil {
		s.deps.Log.Warn("failed to list history for service", "name", name, "error", err)
	}
	if history == nil {
		history = []UpdateRecord{}
	}

	// Gather versions.
	var versions []string
	if s.deps.Registry != nil {
		versions, err = s.deps.Registry.ListVersions(r.Context(), found.Image)
		if err != nil {
			s.deps.Log.Warn("failed to list versions", "name", name, "error", err)
			versions = nil
		}
	}

	data := serviceDetailData{
		Service:        view,
		History:        history,
		Versions:       versions,
		ChangelogURL:   ChangelogURL(found.Image),
		ClusterEnabled: s.deps.Cluster != nil && s.deps.Cluster.Enabled(),
	}
	s.withAuthServiceDetail(r, &data)

	s.renderTemplate(w, "service.html", data)
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
