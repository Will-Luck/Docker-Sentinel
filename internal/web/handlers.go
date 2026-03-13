package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// handleQueue renders the pending update queue page.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	items := s.deps.Queue.List()
	if items == nil {
		items = []PendingUpdate{}
	}

	sources := s.loadReleaseSources()
	releaseNotes := make(map[string]releaseNote)
	selfKeys := make(map[string]bool)
	for _, item := range items {
		if len(item.NewerVersions) > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			info := registry.FetchReleaseNotesWithSources(ctx, item.CurrentImage, item.NewerVersions[0], sources)
			cancel()
			if info != nil {
				releaseNotes[item.Key()] = releaseNote{URL: info.URL, Body: info.Body}
			}
		}
		// Check if this queue item is the Sentinel container itself.
		if s.isProtectedContainer(r.Context(), item.ContainerName) {
			selfKeys[item.Key()] = true
		}
	}

	data := pageData{
		Page:              "queue",
		Queue:             items,
		QueueReleaseNotes: releaseNotes,
		QueueSelfKeys:     selfKeys,
		QueueCount:        len(items),
	}
	s.withAuth(r, &data)
	s.withCluster(&data)
	s.withPortainer(&data)

	s.renderTemplate(w, "queue.html", data)
}

// handleHistory renders the update history page.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	records, err := s.deps.Store.ListHistory(50, "")
	if err != nil {
		s.deps.Log.Error("failed to list history", "error", err)
		http.Error(w, "failed to load history", http.StatusInternalServerError)
		return
	}

	if records == nil {
		records = []UpdateRecord{}
	}

	var nextCursor string
	if len(records) > 0 {
		nextCursor = records[len(records)-1].Timestamp.UTC().Format(time.RFC3339Nano)
	}

	data := pageData{
		Page:       "history",
		History:    records,
		QueueCount: len(s.deps.Queue.List()),
		NextCursor: nextCursor,
	}
	s.withAuth(r, &data)
	s.withCluster(&data)
	s.withPortainer(&data)

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
	s.withPortainer(&data)
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
			s.renderError(w, http.StatusInternalServerError, "Database Error",
				"Failed to load activity logs. The database may be temporarily unavailable.")
			return
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
	s.withPortainer(&data)
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
	if !isValidContainerName(name) {
		writeError(w, http.StatusBadRequest, "invalid container name")
		return
	}
	hostFilter := r.URL.Query().Get("host")

	containers, err := s.deps.Docker.ListAllContainers(r.Context())
	if err != nil {
		s.deps.Log.Error("failed to list containers", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	pendingNames := make(map[string]bool)
	for _, p := range s.deps.Queue.List() {
		pendingNames[p.Key()] = true
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
		if n == name && hostFilter == "" {
			maintenance, err := s.deps.Store.GetMaintenance(n)
			if err != nil {
				s.deps.Log.Debug("failed to load maintenance state", "name", n, "error", err)
			}
			policy := s.resolvedPolicy(c.Labels, n)
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
			var resolved string
			if _, isSemver := registry.ParseSemVer(tag); !isSemver {
				if v := c.Labels["org.opencontainers.image.version"]; v != "" && v != tag {
					resolved = v
				}
			}
			var severity string
			if pendingNames[n] {
				if newestVersion == "" {
					severity = "build"
				} else {
					severity = classifySeverity(tag, newestVersion)
					if severity == "" && resolved != "" {
						severity = classifySeverity(resolved, newestVersion)
					}
				}
			}
			v := containerView{
				ID:              c.ID,
				Name:            n,
				Image:           c.Image,
				Tag:             tag,
				ResolvedVersion: resolved,
				NewestVersion:   newestVersion,
				Policy:          policy,
				State:           c.State,
				Maintenance:     maintenance,
				HasUpdate:       pendingNames[n],
				DigestOnly:      pendingNames[n] && newestVersion == "",
				Severity:        severity,
				IsSelf:          c.Labels["sentinel.self"] == "true",
				Stack:           c.Labels["com.docker.compose.project"],
				Registry:        registry.RegistryHost(c.Image),
				Ports:           c.Ports,
				HostAddress:     s.localHostAddr(r),
			}
			v.PortURLs = s.resolvePortURLs(n, s.localHostAddr(r), "", c.Ports)
			targetView = &v
		}
	}

	// Fallback: search cluster remote containers if not found locally.
	if targetView == nil && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		for _, rc := range s.deps.Cluster.AllHostContainers() {
			if rc.Name == name && (hostFilter == "" || rc.HostID == hostFilter) {
				policy := s.resolvedPolicy(rc.Labels, rc.HostID+"::"+rc.Name)
				tag := registry.ExtractTag(rc.Image)
				if tag == "" {
					if idx := strings.LastIndex(rc.Image, "/"); idx >= 0 {
						tag = rc.Image[idx+1:]
					} else {
						tag = rc.Image
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
				var rcSeverity string
				if hasUpdate {
					if newestVersion == "" {
						rcSeverity = "build"
					} else {
						rcSeverity = classifySeverity(tag, newestVersion)
						if rcSeverity == "" {
							if v := rc.Labels["org.opencontainers.image.version"]; v != "" && v != tag {
								rcSeverity = classifySeverity(v, newestVersion)
							}
						}
					}
				}
				// Resolve agent IP for port links and NPM lookup.
				var rcHostAddr string
				if s.deps.Cluster != nil {
					if h, ok := s.deps.Cluster.GetHost(rc.HostID); ok {
						rcHostAddr = extractIP(h.Address)
					}
				}
				v := containerView{
					Name:          rc.Name,
					Image:         rc.Image,
					Tag:           tag,
					NewestVersion: newestVersion,
					Policy:        policy,
					State:         rc.State,
					HasUpdate:     hasUpdate,
					DigestOnly:    hasUpdate && newestVersion == "",
					Severity:      rcSeverity,
					IsSelf:        rc.Labels["sentinel.self"] == "true",
					HostID:        rc.HostID,
					HostName:      rc.HostName,
					HostAddress:   rcHostAddr,
					Registry:      registry.RegistryHost(rc.Image),
					Maintenance:   s.isRemoteUpdating(rc.HostID, rc.Name),
					Ports:         rc.Ports,
				}
				v.PortURLs = s.resolvePortURLs(rc.Name, rcHostAddr, rc.HostID, rc.Ports)
				targetView = &v
				break
			}
		}
	}

	// Fallback: search Portainer endpoint containers.
	if targetView == nil && strings.HasPrefix(hostFilter, "portainer:") && s.deps.Portainer != nil {
		parts := strings.SplitN(strings.TrimPrefix(hostFilter, "portainer:"), ":", 2)
		if len(parts) == 2 {
			instanceID := parts[0]
			epID, _ := strconv.Atoi(parts[1])
			pContainers, pErr := s.deps.Portainer.EndpointContainers(r.Context(), instanceID, epID)
			if pErr == nil {
				for _, c := range pContainers {
					if c.Name != name {
						continue
					}
					tag := registry.ExtractTag(c.Image)
					if tag == "" {
						if idx := strings.LastIndex(c.Image, "/"); idx >= 0 {
							tag = c.Image[idx+1:]
						} else {
							tag = c.Image
						}
					}
					queueKey := hostFilter + "::" + c.Name
					policy := s.resolvedPolicy(c.Labels, queueKey)
					var newestVersion string
					var hasUpdate bool
					if pend, ok := s.deps.Queue.Get(queueKey); ok {
						hasUpdate = true
						if len(pend.NewerVersions) > 0 {
							newestVersion = pend.NewerVersions[0]
						}
					}
					var severity string
					if hasUpdate {
						if newestVersion == "" {
							severity = "build"
						} else {
							severity = classifySeverity(tag, newestVersion)
						}
					}
					v := containerView{
						Name:          c.Name,
						Image:         c.Image,
						Tag:           tag,
						NewestVersion: newestVersion,
						Registry:      registry.RegistryHost(c.Image),
						Policy:        policy,
						State:         c.State,
						HasUpdate:     hasUpdate,
						DigestOnly:    hasUpdate && newestVersion == "",
						Severity:      severity,
						HostID:        hostFilter,
						HostName:      c.InstanceName,
						Maintenance:   s.isRemoteUpdating(hostFilter, c.Name),
					}
					targetView = &v
					break
				}
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
		s.deps.Log.Error("failed to list containers for stats", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}

	total, running := len(containers), 0
	for _, c := range containers {
		if c.State == "running" {
			running++
		}
	}

	// Include Swarm services in total count.
	if s.deps.Swarm != nil && s.deps.Swarm.IsSwarmMode() {
		services, err := s.deps.Swarm.ListServices(r.Context())
		if err == nil {
			total += len(services)
		}
	}

	// Include remote cluster containers.
	if s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		for _, rc := range s.deps.Cluster.AllHostContainers() {
			total++
			if rc.State == "running" {
				running++
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":   total,
		"running": running,
		"pending": len(s.deps.Queue.List()),
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

	// Compute image tag for enrollment snippets.
	imageTag := s.deps.Version
	imageTag = strings.TrimPrefix(imageTag, "v")
	if idx := strings.IndexByte(imageTag, ' '); idx >= 0 {
		imageTag = imageTag[:idx]
	}
	if imageTag == "" || imageTag == "dev" {
		imageTag = "latest"
	}

	data := pageData{
		Page:                  "cluster",
		QueueCount:            len(s.deps.Queue.List()),
		ClusterHosts:          hosts,
		ClusterConnectedCount: connectedCount,
		ClusterContainerCount: containerCount,
		ServerVersion:         s.deps.Version,
		ClusterPort:           s.deps.ClusterPort,
		ImageTag:              imageTag,
	}
	s.withAuth(r, &data)
	s.withCluster(&data)
	s.withPortainer(&data)
	s.renderTemplate(w, "cluster.html", data)
}

// containerDetailData holds all data for the per-container detail page.
type containerDetailData struct {
	Container     containerView
	History       []UpdateRecord
	Snapshots     []SnapshotEntry
	Versions      []string
	HasSnapshot   bool
	ChangelogURL  string
	PortOverrides map[string]PortOverride // per-port custom URL/path overrides, keyed by port string

	PortainerEnabled bool
	ClusterEnabled   bool

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
		v, err := s.deps.SettingsStore.LoadSetting(store.SettingClusterEnabled)
		if err != nil {
			s.deps.Log.Debug("failed to load cluster enabled setting", "error", err)
		}
		data.ClusterEnabled = v == "true"
	}
}

// isPortainerEnabled checks whether Portainer integration is active.
func (s *Server) isPortainerEnabled() bool {
	if s.deps.Portainer != nil {
		return true
	}
	if s.deps.PortainerInstances != nil {
		instances, err := s.deps.PortainerInstances.ListPortainerInstances()
		if err == nil {
			for _, inst := range instances {
				if inst.Enabled {
					return true
				}
			}
		}
	}
	return false
}

// withPortainer populates PortainerEnabled from the live provider or DB setting.
func (s *Server) withPortainer(data *pageData) {
	data.PortainerEnabled = s.isPortainerEnabled()
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
	data.ShowSecurityTab = !data.AuthEnabled ||
		(data.CurrentUser != nil && data.CurrentUser.RoleID == "admin")
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
	if !isValidContainerName(name) {
		s.renderError(w, http.StatusBadRequest, "Bad Request", "Container name is required.")
		return
	}
	hostFilter := r.URL.Query().Get("host")

	var view containerView
	var image string // for changelog/version lookups

	if strings.HasPrefix(hostFilter, "portainer:") && s.deps.Portainer != nil {
		// Portainer container: parse "portainer:instanceID:epID" or legacy "portainer:epID".
		parts := strings.SplitN(strings.TrimPrefix(hostFilter, "portainer:"), ":", 2)
		var instanceID, epIDStr string
		if len(parts) == 2 {
			instanceID = parts[0]
			epIDStr = parts[1]
		} else {
			epIDStr = parts[0]
		}
		epID, err := strconv.Atoi(epIDStr)
		if err != nil {
			s.renderError(w, http.StatusBadRequest, "Bad Request", "Invalid Portainer endpoint ID.")
			return
		}
		containers, err := s.deps.Portainer.EndpointContainers(r.Context(), instanceID, epID)
		if err != nil {
			s.deps.Log.Error("failed to list Portainer containers", "endpoint", epID, "error", err)
			s.renderError(w, http.StatusInternalServerError, "Server Error", "Failed to load Portainer containers.")
			return
		}
		var pc *PortainerContainerInfo
		for _, c := range containers {
			if c.Name == name {
				pc = &c
				break
			}
		}
		if pc == nil {
			s.renderError(w, http.StatusNotFound, "Container Not Found",
				"The container \""+name+"\" was not found on Portainer endpoint "+epIDStr+". It may have been removed.")
			return
		}

		queueKey := hostFilter + "::" + name
		policy := s.resolvedPolicy(pc.Labels, queueKey)
		tag := registry.ExtractTag(pc.Image)
		if tag == "" {
			if idx := strings.LastIndex(pc.Image, "/"); idx >= 0 {
				tag = pc.Image[idx+1:]
			} else {
				tag = pc.Image
			}
		}
		var resolved string
		if _, isSemver := registry.ParseSemVer(tag); !isSemver {
			if v := pc.Labels["org.opencontainers.image.version"]; v != "" && v != tag {
				resolved = v
			}
		}
		var newestVersion string
		var hasUpdate bool
		if pend, ok := s.deps.Queue.Get(queueKey); ok {
			hasUpdate = true
			if len(pend.NewerVersions) > 0 {
				newestVersion = pend.NewerVersions[0]
			}
		}
		var detailSeverity string
		if hasUpdate {
			if newestVersion == "" {
				detailSeverity = "build"
			} else {
				detailSeverity = classifySeverity(tag, newestVersion)
				if detailSeverity == "" && resolved != "" {
					detailSeverity = classifySeverity(resolved, newestVersion)
				}
			}
		}

		view = containerView{
			Name:            pc.Name,
			Image:           pc.Image,
			Tag:             tag,
			ResolvedVersion: resolved,
			NewestVersion:   newestVersion,
			Policy:          policy,
			State:           pc.State,
			HasUpdate:       hasUpdate,
			DigestOnly:      hasUpdate && newestVersion == "",
			Severity:        detailSeverity,
			IsSelf:          pc.Labels["sentinel.self"] == "true",
			HostID:          hostFilter,
			HostName:        pc.EndpointName,
			Registry:        registry.RegistryHost(pc.Image),
		}
		image = pc.Image
	} else if hostFilter != "" && s.deps.Cluster != nil && s.deps.Cluster.Enabled() {
		// Remote container: look up from cluster cache.
		var rc *RemoteContainer
		for _, c := range s.deps.Cluster.AllHostContainers() {
			if c.Name == name && c.HostID == hostFilter {
				rc = &c
				break
			}
		}
		if rc == nil {
			s.renderError(w, http.StatusNotFound, "Container Not Found",
				"The container \""+name+"\" was not found on host \""+hostFilter+"\". It may have been removed.")
			return
		}

		policy := s.resolvedPolicy(rc.Labels, rc.HostID+"::"+rc.Name)
		tag := registry.ExtractTag(rc.Image)
		if tag == "" {
			if idx := strings.LastIndex(rc.Image, "/"); idx >= 0 {
				tag = rc.Image[idx+1:]
			} else {
				tag = rc.Image
			}
		}
		var resolved string
		if _, isSemver := registry.ParseSemVer(tag); !isSemver {
			if v := rc.Labels["org.opencontainers.image.version"]; v != "" && v != tag {
				resolved = v
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

		var detailSeverity string
		if hasUpdate {
			if newestVersion == "" {
				detailSeverity = "build"
			} else {
				detailSeverity = classifySeverity(tag, newestVersion)
				if detailSeverity == "" && resolved != "" {
					detailSeverity = classifySeverity(resolved, newestVersion)
				}
			}
		}

		// Resolve agent IP for port links and NPM lookup.
		var detailHostAddr string
		if s.deps.Cluster != nil {
			if h, ok := s.deps.Cluster.GetHost(rc.HostID); ok {
				detailHostAddr = extractIP(h.Address)
			}
		}
		view = containerView{
			Name:            rc.Name,
			Image:           rc.Image,
			Tag:             tag,
			ResolvedVersion: resolved,
			NewestVersion:   newestVersion,
			Policy:          policy,
			State:           rc.State,
			HasUpdate:       hasUpdate,
			DigestOnly:      hasUpdate && newestVersion == "",
			Severity:        detailSeverity,
			IsSelf:          rc.Labels["sentinel.self"] == "true",
			HostID:          rc.HostID,
			HostName:        rc.HostName,
			HostAddress:     detailHostAddr,
			Registry:        registry.RegistryHost(rc.Image),
			Maintenance:     s.isRemoteUpdating(rc.HostID, rc.Name),
			Ports:           rc.Ports,
		}
		image = rc.Image
	} else {
		// Local container: look up from Docker.
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
			s.renderError(w, http.StatusNotFound, "Container Not Found",
				"The container \""+name+"\" was not found. It may have been removed.")
			return
		}

		maintenance, err := s.deps.Store.GetMaintenance(name)
		if err != nil {
			s.deps.Log.Debug("failed to load maintenance state", "name", name, "error", err)
		}

		detailPolicy := s.resolvedPolicy(found.Labels, name)

		detailTag := registry.ExtractTag(found.Image)
		if detailTag == "" {
			if idx := strings.LastIndex(found.Image, "/"); idx >= 0 {
				detailTag = found.Image[idx+1:]
			} else {
				detailTag = found.Image
			}
		}

		var detailResolved string
		if _, isSemver := registry.ParseSemVer(detailTag); !isSemver {
			if v := found.Labels["org.opencontainers.image.version"]; v != "" && v != detailTag {
				detailResolved = v
			}
		}

		var localNewest string
		var localHasUpdate bool
		if pend, ok := s.deps.Queue.Get(name); ok {
			localHasUpdate = true
			if len(pend.NewerVersions) > 0 {
				localNewest = pend.NewerVersions[0]
			}
		}
		var localSeverity string
		if localHasUpdate {
			if localNewest == "" {
				localSeverity = "build"
			} else {
				localSeverity = classifySeverity(detailTag, localNewest)
				if localSeverity == "" && detailResolved != "" {
					localSeverity = classifySeverity(detailResolved, localNewest)
				}
			}
		}

		view = containerView{
			ID:              found.ID,
			Name:            containerName(*found),
			Image:           found.Image,
			Tag:             detailTag,
			ResolvedVersion: detailResolved,
			NewestVersion:   localNewest,
			Policy:          detailPolicy,
			State:           found.State,
			Maintenance:     maintenance,
			HasUpdate:       localHasUpdate,
			DigestOnly:      localHasUpdate && localNewest == "",
			Severity:        localSeverity,
			IsSelf:          found.Labels["sentinel.self"] == "true",
			Registry:        registry.RegistryHost(found.Image),
			Ports:           found.Ports,
			HostAddress:     s.localHostAddr(r),
		}
		image = found.Image
	}

	// Use the scoped key for history/snapshot lookups so Portainer and cluster
	// containers find their records (stored as "portainer:3::name" or "hostID::name").
	historyKey := name
	if hostFilter != "" {
		historyKey = hostFilter + "::" + name
	}

	// Gather history.
	history, err := s.deps.Store.ListHistoryByContainer(historyKey, 50)
	if err != nil {
		s.deps.Log.Warn("failed to list history for container", "name", historyKey, "error", err)
	}
	if history == nil {
		history = []UpdateRecord{}
	}

	// Gather snapshots (nil-check dependency).
	var snapshots []SnapshotEntry
	if s.deps.Snapshots != nil {
		storeEntries, err := s.deps.Snapshots.ListSnapshots(historyKey)
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
		versions, err = s.deps.Registry.ListVersions(r.Context(), image)
		if err != nil {
			s.deps.Log.Warn("failed to list versions", "name", name, "error", err)
			versions = nil
		}
	}

	// Load per-port custom URL overrides (host-aware key for remote containers).
	var portOverrides map[string]PortOverride
	if s.deps.PortConfigs != nil {
		pcKey := portConfigKey(hostFilter, name)
		if pc, err := s.deps.PortConfigs.GetPortConfig(pcKey); err == nil && pc != nil {
			portOverrides = pc.Ports
		}
	}

	data := containerDetailData{
		Container:        view,
		History:          history,
		Snapshots:        snapshots,
		Versions:         versions,
		HasSnapshot:      len(snapshots) > 0,
		ChangelogURL:     ChangelogURL(image),
		PortOverrides:    portOverrides,
		PortainerEnabled: s.isPortainerEnabled(),
		ClusterEnabled:   s.deps.Cluster != nil && s.deps.Cluster.Enabled(),
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

	PortainerEnabled bool
	ClusterEnabled   bool

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
	if !isValidContainerName(name) {
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
		pendingNames[p.Key()] = true
	}
	view := s.buildServiceView(*found, pendingNames, s.localHostAddr(r))

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
		Service:          view,
		History:          history,
		Versions:         versions,
		ChangelogURL:     ChangelogURL(found.Image),
		PortainerEnabled: s.isPortainerEnabled(),
		ClusterEnabled:   s.deps.Cluster != nil && s.deps.Cluster.Enabled(),
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, "error.html", errorPageData{Title: title, Message: message}); err != nil {
		s.deps.Log.Error("error template render failed", "error", err)
		fmt.Fprintf(w, "Internal server error")
	}
}
