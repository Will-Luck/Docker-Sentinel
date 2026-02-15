package web

import (
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"time"
)

// apiAbout returns instance, runtime, and integration info for the About tab.
func (s *Server) apiAbout(w http.ResponseWriter, r *http.Request) {
	type channelInfo struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}

	type aboutResponse struct {
		Version        string        `json:"version"`
		GoVersion      string        `json:"go_version"`
		DataDirectory  string        `json:"data_directory"`
		Uptime         string        `json:"uptime"`
		StartedAt      time.Time     `json:"started_at"`
		PollInterval   string        `json:"poll_interval"`
		LastScan       *time.Time    `json:"last_scan"`
		Containers     int           `json:"containers"`
		UpdatesApplied int           `json:"updates_applied"`
		Snapshots      int           `json:"snapshots"`
		Channels       []channelInfo `json:"channels"`
		Registries     []string      `json:"registries"`
	}

	resp := aboutResponse{
		Version:    s.deps.Version,
		GoVersion:  runtime.Version(),
		Uptime:     formatUptime(time.Since(s.startTime)),
		StartedAt:  s.startTime,
		Channels:   []channelInfo{},
		Registries: []string{},
	}

	// Data directory from config.
	if s.deps.Config != nil {
		vals := s.deps.Config.Values()
		if dbPath, ok := vals["SENTINEL_DB_PATH"]; ok {
			resp.DataDirectory = filepath.Dir(dbPath)
		}
	}

	// Poll interval.
	if s.deps.Config != nil {
		vals := s.deps.Config.Values()
		if pi, ok := vals["SENTINEL_POLL_INTERVAL"]; ok {
			resp.PollInterval = pi
		}
	}
	if s.deps.SettingsStore != nil {
		if saved, err := s.deps.SettingsStore.LoadSetting("poll_interval"); err == nil && saved != "" {
			resp.PollInterval = saved
		}
	}

	// Last scan.
	if s.deps.Scheduler != nil {
		t := s.deps.Scheduler.LastScanTime()
		if !t.IsZero() {
			resp.LastScan = &t
		}
	}

	// Container count.
	if s.deps.Docker != nil {
		containers, err := s.deps.Docker.ListAllContainers(r.Context())
		if err == nil {
			resp.Containers = len(containers)
		}
	}

	// History/snapshot counts from AboutStore.
	if s.deps.AboutStore != nil {
		if n, err := s.deps.AboutStore.CountHistory(); err == nil {
			resp.UpdatesApplied = n
		}
		if n, err := s.deps.AboutStore.CountSnapshots(); err == nil {
			resp.Snapshots = n
		}
	}

	// Notification channels (name + type only, no secrets).
	if s.deps.NotifyConfig != nil {
		channels, err := s.deps.NotifyConfig.GetNotificationChannels()
		if err == nil {
			for _, ch := range channels {
				if ch.Enabled {
					resp.Channels = append(resp.Channels, channelInfo{
						Name: ch.Name,
						Type: string(ch.Type),
					})
				}
			}
		}
	}

	// Registry credentials (hostnames only).
	if s.deps.RegistryCredentials != nil {
		creds, err := s.deps.RegistryCredentials.GetRegistryCredentials()
		if err == nil {
			for _, c := range creds {
				resp.Registries = append(resp.Registries, c.Registry)
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// formatUptime formats a duration into a human-readable "Xd Xh Xm" string.
func formatUptime(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
