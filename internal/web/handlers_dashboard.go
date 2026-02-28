package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
)

// pageData is the common data structure passed to all page templates.
type pageData struct {
	Page              string
	Containers        []containerView
	Stacks            []stackGroup
	Queue             []PendingUpdate
	QueueReleaseNotes map[string]string // keyed by queue key -> GitHub release URL
	History           []UpdateRecord
	Settings          map[string]string
	Logs              []LogEntry

	// Swarm services — separate section below standalone containers.
	SwarmServices []serviceView

	// Dashboard stats (computed by the handler).
	TotalContainers   int
	RunningContainers int
	PendingUpdates    int
	QueueCount        int // sidebar badge: number of items in queue

	// Per-tab stats for the dashboard tab navigation.
	TabStats []tabStats
	HasSwarm bool

	// Portainer state (populated by withPortainer helper).
	PortainerEnabled bool

	// Cluster state (populated by withCluster helper).
	ClusterEnabled bool
	ClusterHosts   []ClusterHost // populated only on /cluster page
	HostGroups     []hostGroup   // populated only on dashboard when cluster active (Task 9)

	// Cluster page stats (populated by handleCluster).
	ClusterConnectedCount int
	ClusterContainerCount int
	ServerVersion         string
	ClusterPort           string // gRPC port for enrollment snippets
	ImageTag              string // stripped version tag for GHCR image snippets

	// Pagination cursor for history load-more.
	NextCursor string

	// Auth context (populated by withAuth helper).
	CurrentUser     *auth.User
	AuthEnabled     bool
	CSRFToken       string
	ShowSecurityTab bool // true when auth off OR (auth on + admin)
}

// tabStats holds per-tab container counts for the dashboard tab navigation.
// ID is "local", "swarm", or the cluster host ID. Label is the human-readable
// display name shown on the tab.
type tabStats struct {
	ID      string // "local", "swarm", or host ID
	Label   string // "Local", "Swarm Services", or host name
	Total   int
	Running int
	Pending int
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
	DigestOnly      bool // true when update is digest-only (same tag, newer build)
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
	s.signalScanReady()
	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers", "error", err)
		http.Error(w, "failed to load containers", http.StatusInternalServerError)
		return
	}

	// Build the pending update lookup for "update available" badges.
	// Uses Key() (e.g. "hostID::name" for remote, bare "name" for local/service)
	// to avoid remote entries falsely matching swarm services with the same name.
	pendingNames := make(map[string]bool)
	for _, p := range s.deps.Queue.List() {
		pendingNames[p.Key()] = true
	}

	// Check if stopped containers should be shown (opt-in setting).
	showStopped := false
	if s.deps.SettingsStore != nil {
		if v, err := s.deps.SettingsStore.LoadSetting("show_stopped"); err == nil {
			showStopped = v == "true"
		}
	}

	views := make([]containerView, 0, len(containers))
	for _, c := range containers {
		// Filter out Swarm task containers — they'll appear in the Swarm Services section.
		if _, isTask := c.Labels["com.docker.swarm.task"]; isTask {
			continue
		}

		// Exclude stopped containers unless the setting is enabled.
		if !showStopped && c.State != "running" {
			continue
		}

		name := containerName(c)
		maintenance, err := s.deps.Store.GetMaintenance(name)
		if err != nil {
			s.deps.Log.Debug("failed to load maintenance state", "name", name, "error", err)
		}

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
			DigestOnly:      pendingNames[name] && newestVersion == "",
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
	savedJSON, err := s.deps.SettingsStore.LoadSetting("stack_order")
	if err != nil {
		s.deps.Log.Debug("failed to load stack order", "error", err)
	}
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
			// Skip Swarm task containers — same as local filtering.
			if _, isTask := rc.Labels["com.docker.swarm.task"]; isTask {
				continue
			}

			tag := registry.ExtractTag(rc.Image)
			if tag == "" {
				if idx := strings.LastIndex(rc.Image, "/"); idx >= 0 {
					tag = rc.Image[idx+1:]
				} else {
					tag = rc.Image
				}
			}
			// Resolve policy the same way we do for local containers:
			// label first, then DB override (keyed by hostID::name for remote).
			policy := containerPolicy(rc.Labels)
			if s.deps.Policy != nil {
				policyKey := rc.HostID + "::" + rc.Name
				if p, ok := s.deps.Policy.GetPolicyOverride(policyKey); ok {
					policy = p
				}
			}

			var newestVersion string
			var hasUpdate bool
			queueKey := rc.HostID + "::" + rc.Name
			if pend, ok := s.deps.Queue.Get(queueKey); ok {
				hasUpdate = true
				if len(pend.NewerVersions) > 0 {
					newestVersion = pend.NewerVersions[0]
				}
			}

			cv := containerView{
				Name:          rc.Name,
				Image:         rc.Image,
				Tag:           tag,
				NewestVersion: newestVersion,
				Registry:      registry.RegistryHost(rc.Image),
				Policy:        policy,
				State:         rc.State,
				HasUpdate:     hasUpdate,
				DigestOnly:    hasUpdate && newestVersion == "",
				IsSelf:        rc.Labels["sentinel.self"] == "true",
				HostID:        rc.HostID,
				HostName:      rc.HostName,
				Maintenance:   s.isRemoteUpdating(rc.HostID, rc.Name),
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
					if c.HasUpdate {
						sg.HasPending = true
						sg.PendingCount++
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
				if c.HasUpdate {
					data.PendingUpdates++
				}
			}
		}
	}

	// Compute per-tab stats for the dashboard tab navigation.
	// Tabs are only rendered when there are two or more distinct sources.
	data.HasSwarm = len(svcViews) > 0

	// Local tab — always present when there are local containers.
	if len(views) > 0 {
		localStats := tabStats{
			ID:    "local",
			Label: "Local",
			Total: len(views),
		}
		for _, v := range views {
			if v.State == "running" {
				localStats.Running++
			}
			if v.HasUpdate {
				localStats.Pending++
			}
		}
		data.TabStats = append(data.TabStats, localStats)
	}

	// Swarm tab — present when there are Swarm services.
	if len(svcViews) > 0 {
		swarmStats := tabStats{
			ID:    "swarm",
			Label: "Swarm",
			Total: len(svcViews),
		}
		for _, sv := range svcViews {
			if sv.RunningReplicas > 0 {
				swarmStats.Running++
			}
			if sv.HasUpdate {
				swarmStats.Pending++
			}
		}
		data.TabStats = append(data.TabStats, swarmStats)
	}

	// Cluster host tabs — one per remote host (skip "local", already added above).
	if s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		for _, hg := range data.HostGroups {
			if hg.ID == "local" {
				continue
			}
			ts := tabStats{
				ID:    hg.ID,
				Label: hg.Name,
				Total: hg.Count,
			}
			for _, sg := range hg.Stacks {
				ts.Running += sg.RunningCount
				ts.Pending += sg.PendingCount
			}
			data.TabStats = append(data.TabStats, ts)
		}
	}

	// Prepend "All" tab with aggregate stats.
	if len(data.TabStats) >= 2 {
		allStats := tabStats{ID: "all", Label: "All"}
		for _, ts := range data.TabStats {
			allStats.Total += ts.Total
			allStats.Running += ts.Running
			allStats.Pending += ts.Pending
		}
		data.TabStats = append([]tabStats{allStats}, data.TabStats...)
	}

	s.withAuth(r, &data)
	s.withCluster(&data)
	s.withPortainer(&data)

	s.renderTemplate(w, "index.html", data)
}
