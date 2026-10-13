package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	clusterserver "github.com/Will-Luck/Docker-Sentinel/internal/cluster/server"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/npm"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/web"
)

// clusterAdapter bridges cluster/server.Server to web.ClusterProvider.
type clusterAdapter struct {
	srv   *clusterserver.Server
	store *store.Store
}

func (a *clusterAdapter) AllHosts() []web.ClusterHost {
	infos := a.srv.AllHosts()
	result := make([]web.ClusterHost, 0, len(infos))
	for _, h := range infos {
		// Use GetHost to get full HostState (includes ephemeral
		// fields like Connected and in-memory Containers).
		if hs, ok := a.srv.GetHost(h.ID); ok {
			result = append(result, web.ClusterHost{
				ID:            hs.Info.ID,
				Name:          hs.Info.Name,
				Address:       hs.Info.Address,
				State:         string(hs.Info.State),
				Connected:     hs.Connected,
				EnrolledAt:    hs.Info.EnrolledAt,
				LastSeen:      hs.Info.LastSeen,
				AgentVersion:  hs.Info.AgentVersion,
				Containers:    len(hs.Containers),
				DisconnectAt:  hs.DisconnectAt,
				DisconnectErr: hs.DisconnectErr,
				DisconnectCat: hs.DisconnectCat,
				EngineID:      hs.Info.EngineID,
			})
		}
	}
	return result
}

func (a *clusterAdapter) GetHost(id string) (web.ClusterHost, bool) {
	hs, ok := a.srv.GetHost(id)
	if !ok {
		return web.ClusterHost{}, false
	}
	return web.ClusterHost{
		ID:            hs.Info.ID,
		Name:          hs.Info.Name,
		Address:       hs.Info.Address,
		State:         string(hs.Info.State),
		Connected:     hs.Connected,
		EnrolledAt:    hs.Info.EnrolledAt,
		LastSeen:      hs.Info.LastSeen,
		AgentVersion:  hs.Info.AgentVersion,
		Containers:    len(hs.Containers),
		DisconnectAt:  hs.DisconnectAt,
		DisconnectErr: hs.DisconnectErr,
		DisconnectCat: hs.DisconnectCat,
		EngineID:      hs.Info.EngineID,
	}, true
}

func (a *clusterAdapter) AllHostContainers() []web.RemoteContainer {
	var result []web.RemoteContainer
	for _, info := range a.srv.AllHosts() {
		hs, ok := a.srv.GetHost(info.ID)
		if !ok {
			continue
		}
		for _, c := range hs.Containers {
			var ports []web.PortMapping
			for _, p := range c.Ports {
				ports = append(ports, web.PortMapping{
					HostIP:        p.HostIP,
					HostPort:      p.HostPort,
					ContainerPort: p.ContainerPort,
					Protocol:      p.Protocol,
				})
			}
			result = append(result, web.RemoteContainer{
				Name:     c.Name,
				Image:    c.Image,
				State:    c.State,
				HostID:   info.ID,
				HostName: info.Name,
				Labels:   c.Labels,
				Ports:    ports,
			})
		}
	}
	return result
}

func (a *clusterAdapter) ConnectedHosts() []string {
	return a.srv.ConnectedHosts()
}

func (a *clusterAdapter) GenerateEnrollToken() (string, string, error) {
	return a.srv.GenerateEnrollToken(24 * time.Hour)
}

func (a *clusterAdapter) RemoveHost(id string) error {
	return a.srv.RemoveHost(id)
}

func (a *clusterAdapter) RevokeHost(id string) error {
	return a.srv.RevokeHost(id)
}

func (a *clusterAdapter) PauseHost(id string) error {
	return a.srv.PauseHost(id)
}

func (a *clusterAdapter) UpdateRemoteContainer(ctx context.Context, hostID, containerName, targetImage, targetDigest string) error {
	ur, err := a.srv.UpdateContainerSync(ctx, hostID, containerName, targetImage, targetDigest)
	if err != nil {
		return err
	}
	if ur.Outcome != "success" {
		if ur.Error != "" {
			return fmt.Errorf("%s", ur.Error)
		}
		return fmt.Errorf("update failed")
	}
	return nil
}

func (a *clusterAdapter) RemoteContainerAction(ctx context.Context, hostID, containerName, action string) error {
	return a.srv.ContainerActionSync(ctx, hostID, containerName, action)
}

func (a *clusterAdapter) RemoteContainerLogs(ctx context.Context, hostID, containerName string, lines int) (string, error) {
	return a.srv.FetchLogsSync(ctx, hostID, containerName, lines)
}

func (a *clusterAdapter) RollbackRemoteContainer(ctx context.Context, hostID, containerName string) error {
	// Look up the most recent successful update from history to find the old image.
	scopedName := store.ScopedKey(hostID, containerName)
	records, err := a.store.ListHistoryByContainer(scopedName, 10)
	if err != nil {
		return fmt.Errorf("lookup update history for %s: %w", scopedName, err)
	}
	// Find the most recent successful update with a valid old image.
	var oldImage string
	for _, rec := range records {
		if rec.Outcome == "success" && rec.OldImage != "" {
			oldImage = rec.OldImage
			break
		}
	}
	if oldImage == "" {
		return fmt.Errorf("no successful update history found for %s, cannot determine rollback image", containerName)
	}

	// Use the existing update mechanism to switch back to the old image.
	ur, err := a.srv.UpdateContainerSync(ctx, hostID, containerName, oldImage, "")
	if err != nil {
		return err
	}
	if ur.Outcome != "success" {
		if ur.Error != "" {
			return fmt.Errorf("%s", ur.Error)
		}
		return fmt.Errorf("rollback failed")
	}
	return nil
}

// clusterManager implements web.ClusterLifecycle for dynamic cluster
// start/stop from the settings API. Uses ClusterController.SetProvider()
// to swap the active provider atomically — no value-copy issues.
type clusterManager struct {
	// opMu serialises Start and Stop. mu guards srv only and is never held
	// across a blocking call, so the scheduler's post-scan callback, which
	// takes mu to read srv, cannot stall behind a slow shutdown.
	opMu    sync.Mutex
	mu      sync.Mutex
	srv     *clusterserver.Server
	db      *store.Store
	bus     *events.Bus
	log     *slog.Logger
	updater *engine.Updater
	ctrl    *web.ClusterController // stable pointer in Dependencies
	dataDir string                 // CA/cert storage directory
}

func (m *clusterManager) Start() error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.srv != nil {
		return nil // already running
	}

	// Read port from DB, fall back to default.
	port, _ := m.db.LoadSetting(store.SettingClusterPort)
	if port == "" {
		port = "9443"
	}

	ca, err := cluster.EnsureCA(m.dataDir)
	if err != nil {
		return fmt.Errorf("initialise CA: %w", err)
	}

	m.srv, err = clusterserver.New(ca, m.db, m.bus, m.log)
	if err != nil {
		return fmt.Errorf("create cluster server: %w", err)
	}
	m.srv.SetHistoryRecorder(m.db)

	// Read advertise addresses for TLS cert SANs. Inside Docker, the container
	// only sees its bridge network IPs, but agents connect via the host's
	// external IP. Check env var first, then DB setting.
	adv, _ := m.db.LoadSetting(store.SettingClusterAdvertise)
	if adv == "" {
		adv = os.Getenv("SENTINEL_CLUSTER_ADVERTISE")
	}
	var extraSANs []string
	for _, s := range strings.Split(adv, ",") {
		if t := strings.TrimSpace(s); t != "" {
			extraSANs = append(extraSANs, t)
		}
	}

	addr := net.JoinHostPort("", port)
	if err := m.srv.Start(addr, extraSANs...); err != nil {
		m.srv = nil
		return fmt.Errorf("start gRPC: %w", err)
	}

	// Wire cluster scanner into the engine for multi-host scanning.
	m.updater.SetClusterScanner(&clusterScannerAdapter{srv: m.srv})

	// Swap provider in controller — handlers see it immediately.
	m.ctrl.SetProvider(&clusterAdapter{srv: m.srv, store: m.db})

	m.log.Info("cluster gRPC server started", "addr", addr)
	return nil
}

func (m *clusterManager) Stop() {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	srv := m.srv
	if srv == nil {
		m.mu.Unlock()
		return
	}
	// Clear provider first so handlers stop dispatching.
	m.ctrl.SetProvider(nil)
	m.updater.SetClusterScanner(nil)
	m.srv = nil
	m.mu.Unlock()

	srv.Stop()

	m.log.Info("cluster gRPC server stopped")
}

// --- NPM adapter ---

type npmAdapter struct {
	resolver *npm.Resolver
}

func (a *npmAdapter) TestConnection(ctx context.Context) error {
	return a.resolver.Sync(ctx)
}

func (a *npmAdapter) Lookup(hostPort uint16) *web.NPMResolvedURL {
	r := a.resolver.Lookup(hostPort)
	if r == nil {
		return nil
	}
	return &web.NPMResolvedURL{
		URL:         r.URL,
		Domain:      r.Domain,
		ProxyHostID: r.ProxyHostID,
	}
}

func (a *npmAdapter) LookupForHost(hostPort uint16, hostAddr string) *web.NPMResolvedURL {
	r := a.resolver.LookupForHost(hostPort, hostAddr)
	if r == nil {
		return nil
	}
	return &web.NPMResolvedURL{
		URL:         r.URL,
		Domain:      r.Domain,
		ProxyHostID: r.ProxyHostID,
	}
}

func (a *npmAdapter) AllMappings() map[uint16]web.NPMResolvedURL {
	raw := a.resolver.AllMappings()
	result := make(map[uint16]web.NPMResolvedURL, len(raw))
	for k, v := range raw {
		result[k] = web.NPMResolvedURL{
			URL:         v.URL,
			Domain:      v.Domain,
			ProxyHostID: v.ProxyHostID,
		}
	}
	return result
}

func (a *npmAdapter) AllMappingsGrouped() map[string]map[uint16]web.NPMResolvedURL {
	raw := a.resolver.AllMappingsGrouped()
	result := make(map[string]map[uint16]web.NPMResolvedURL, len(raw))
	for host, portMap := range raw {
		inner := make(map[uint16]web.NPMResolvedURL, len(portMap))
		for port, v := range portMap {
			inner[port] = web.NPMResolvedURL{
				URL:         v.URL,
				Domain:      v.Domain,
				ProxyHostID: v.ProxyHostID,
			}
		}
		result[host] = inner
	}
	return result
}

func (a *npmAdapter) Sync(ctx context.Context) error {
	return a.resolver.Sync(ctx)
}

func (a *npmAdapter) LastSync() time.Time {
	return a.resolver.LastSync()
}

func (a *npmAdapter) LastError() error {
	return a.resolver.LastError()
}
