package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	clusterserver "github.com/Will-Luck/Docker-Sentinel/internal/cluster/server"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/npm"
	"github.com/Will-Luck/Docker-Sentinel/internal/portainer"
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

	addr := net.JoinHostPort("", port)
	if err := m.srv.Start(addr); err != nil {
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
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.srv == nil {
		return
	}

	// Clear provider first so handlers stop dispatching.
	m.ctrl.SetProvider(nil)
	m.updater.SetClusterScanner(nil)

	m.srv.Stop()
	m.srv = nil

	m.log.Info("cluster gRPC server stopped")
}

// multiPortainerAdapter bridges multiple portainer.Scanner instances to web.PortainerProvider.
type multiPortainerAdapter struct {
	mu       sync.RWMutex
	scanners map[string]*portainer.Scanner // keyed by instance ID
}

func newMultiPortainerAdapter() *multiPortainerAdapter {
	return &multiPortainerAdapter{scanners: make(map[string]*portainer.Scanner)}
}

func (a *multiPortainerAdapter) Set(id string, scanner *portainer.Scanner) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scanners[id] = scanner
}

func (a *multiPortainerAdapter) Remove(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.scanners, id)
}

func (a *multiPortainerAdapter) get(id string) (*portainer.Scanner, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	s, ok := a.scanners[id]
	if !ok {
		return nil, fmt.Errorf("portainer instance %q not connected", id)
	}
	return s, nil
}

func (a *multiPortainerAdapter) TestConnection(ctx context.Context, instanceID string) error {
	s, err := a.get(instanceID)
	if err != nil {
		return err
	}
	return s.Client().TestConnection(ctx)
}

func (a *multiPortainerAdapter) Endpoints(ctx context.Context, instanceID string) ([]web.PortainerEndpoint, error) {
	s, err := a.get(instanceID)
	if err != nil {
		return nil, err
	}
	eps, err := s.Endpoints(ctx)
	if err != nil {
		return nil, err
	}
	return convertPortainerEndpoints(eps, instanceID), nil
}

func (a *multiPortainerAdapter) AllEndpoints(ctx context.Context, instanceID string) ([]web.PortainerEndpoint, error) {
	s, err := a.get(instanceID)
	if err != nil {
		return nil, err
	}
	eps, err := s.AllEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	return convertPortainerEndpoints(eps, instanceID), nil
}

func (a *multiPortainerAdapter) EndpointContainers(ctx context.Context, instanceID string, endpointID int) ([]web.PortainerContainerInfo, error) {
	s, err := a.get(instanceID)
	if err != nil {
		return nil, err
	}
	ep, err := findEndpoint(ctx, s, endpointID)
	if err != nil {
		return nil, err
	}
	containers, err := s.EndpointContainers(ctx, ep)
	if err != nil {
		return nil, err
	}
	out := make([]web.PortainerContainerInfo, 0, len(containers))
	for _, c := range containers {
		out = append(out, web.PortainerContainerInfo{
			ID:           c.ID,
			Name:         c.Name,
			Image:        c.Image,
			State:        c.State,
			Labels:       c.Labels,
			EndpointID:   c.EndpointID,
			EndpointName: c.EndpointName,
			StackID:      c.StackID,
			StackName:    c.StackName,
			InstanceID:   instanceID,
		})
	}
	return out, nil
}

// findEndpoint looks up a single endpoint by ID from a scanner's full endpoint list.
func findEndpoint(ctx context.Context, scanner *portainer.Scanner, endpointID int) (portainer.Endpoint, error) {
	all, err := scanner.AllEndpoints(ctx)
	if err != nil {
		return portainer.Endpoint{}, err
	}
	for _, ep := range all {
		if ep.ID == endpointID {
			return ep, nil
		}
	}
	return portainer.Endpoint{}, fmt.Errorf("endpoint %d not found", endpointID)
}

func convertPortainerEndpoints(eps []portainer.Endpoint, instanceID string) []web.PortainerEndpoint {
	out := make([]web.PortainerEndpoint, 0, len(eps))
	for _, ep := range eps {
		status := "down"
		if ep.Status == portainer.StatusUp {
			status = "up"
		}
		out = append(out, web.PortainerEndpoint{
			ID:         ep.ID,
			Name:       ep.Name,
			URL:        ep.URL,
			Type:       int(ep.Type),
			Status:     status,
			InstanceID: instanceID,
		})
	}
	return out
}

// portainerInstanceStoreAdapter bridges store.Store to web.PortainerInstanceStore.
type portainerInstanceStoreAdapter struct {
	store *store.Store
}

func (a *portainerInstanceStoreAdapter) ListPortainerInstances() ([]web.PortainerInstanceConfig, error) {
	instances, err := a.store.ListPortainerInstances()
	if err != nil {
		return nil, err
	}
	out := make([]web.PortainerInstanceConfig, 0, len(instances))
	for _, inst := range instances {
		out = append(out, convertStoreInstance(inst))
	}
	return out, nil
}

func (a *portainerInstanceStoreAdapter) GetPortainerInstance(id string) (web.PortainerInstanceConfig, error) {
	inst, err := a.store.GetPortainerInstance(id)
	if err != nil {
		return web.PortainerInstanceConfig{}, err
	}
	return convertStoreInstance(inst), nil
}

func (a *portainerInstanceStoreAdapter) SavePortainerInstance(cfg web.PortainerInstanceConfig) error {
	inst := store.PortainerInstance{
		ID:      cfg.ID,
		Name:    cfg.Name,
		URL:     cfg.URL,
		Token:   cfg.Token,
		Enabled: cfg.Enabled,
	}
	if len(cfg.Endpoints) > 0 {
		inst.Endpoints = make(map[string]store.EndpointConfig, len(cfg.Endpoints))
		for k, v := range cfg.Endpoints {
			inst.Endpoints[k] = store.EndpointConfig{
				Enabled: v.Enabled,
				Blocked: v.Blocked,
				Reason:  v.Reason,
			}
		}
	}
	return a.store.SavePortainerInstance(inst)
}

func (a *portainerInstanceStoreAdapter) DeletePortainerInstance(id string) error {
	return a.store.DeletePortainerInstance(id)
}

func (a *portainerInstanceStoreAdapter) NextPortainerID() (string, error) {
	return a.store.NextPortainerID()
}

func convertStoreInstance(inst store.PortainerInstance) web.PortainerInstanceConfig {
	cfg := web.PortainerInstanceConfig{
		ID:      inst.ID,
		Name:    inst.Name,
		URL:     inst.URL,
		Token:   inst.Token,
		Enabled: inst.Enabled,
	}
	if len(inst.Endpoints) > 0 {
		cfg.Endpoints = make(map[string]web.EndpointCfg, len(inst.Endpoints))
		for k, v := range inst.Endpoints {
			cfg.Endpoints[k] = web.EndpointCfg{
				Enabled: v.Enabled,
				Blocked: v.Blocked,
				Reason:  v.Reason,
			}
		}
	}
	return cfg
}

// portainerScannerAdapter bridges portainer.Scanner to engine.PortainerScanner.
type portainerScannerAdapter struct {
	scanner *portainer.Scanner
}

func (a *portainerScannerAdapter) Endpoints(ctx context.Context) ([]engine.PortainerEndpointInfo, error) {
	eps, err := a.scanner.Endpoints(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]engine.PortainerEndpointInfo, 0, len(eps))
	for _, ep := range eps {
		out = append(out, engine.PortainerEndpointInfo{
			ID:   ep.ID,
			Name: ep.Name,
		})
	}
	return out, nil
}

func (a *portainerScannerAdapter) EndpointContainers(ctx context.Context, endpointID int) ([]engine.PortainerContainerResult, error) {
	ep, err := findEndpoint(ctx, a.scanner, endpointID)
	if err != nil {
		return nil, err
	}
	containers, err := a.scanner.EndpointContainers(ctx, ep)
	if err != nil {
		return nil, err
	}
	out := make([]engine.PortainerContainerResult, 0, len(containers))
	for _, c := range containers {
		out = append(out, engine.PortainerContainerResult{
			ID:         c.ID,
			Name:       c.Name,
			Image:      c.Image,
			State:      c.State,
			Labels:     c.Labels,
			EndpointID: c.EndpointID,
			StackID:    c.StackID,
			StackName:  c.StackName,
		})
	}
	return out, nil
}

func (a *portainerScannerAdapter) ResetCache() {
	a.scanner.ResetCache()
}

func (a *portainerScannerAdapter) RedeployStack(ctx context.Context, stackID, endpointID int) error {
	return a.scanner.RedeployStack(ctx, stackID, endpointID)
}

func (a *portainerScannerAdapter) UpdateStandaloneContainer(ctx context.Context, endpointID int, containerID, newImage string) error {
	return a.scanner.UpdateStandaloneContainer(ctx, endpointID, containerID, newImage)
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
