// Package server implements the gRPC server for cluster communication.
// It handles agent enrollment, bidirectional streaming, and host tracking.
package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
)

// HostState is the server-side view of a connected agent.
// It pairs the persisted HostInfo with ephemeral runtime state (container list,
// connectivity status) that only exists while the server is running.
type HostState struct {
	Info       cluster.HostInfo
	Containers []cluster.ContainerInfo // latest known container list
	Connected  bool                    // true while the agent stream is active
	LastReport time.Time               // when the last container list was received
}

// Registry tracks connected agent hosts and their container state.
// All host metadata is persisted to BoltDB; connectivity and container lists
// are ephemeral and rebuilt on agent reconnection.
type Registry struct {
	mu    sync.RWMutex
	hosts map[string]*HostState
	store ClusterStore
	log   *slog.Logger
}

// NewRegistry creates a Registry backed by the given store.
// Call LoadFromStore() after construction to hydrate from BoltDB.
func NewRegistry(store ClusterStore, log *slog.Logger) *Registry {
	return &Registry{
		hosts: make(map[string]*HostState),
		store: store,
		log:   log,
	}
}

// LoadFromStore reads all persisted host registrations from BoltDB and
// populates the in-memory map. Every host starts as Connected=false until
// its agent actually opens a Channel stream.
func (r *Registry) LoadFromStore() error {
	raw, err := r.store.ListClusterHosts()
	if err != nil {
		return fmt.Errorf("load cluster hosts: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for id, data := range raw {
		var info cluster.HostInfo
		if err := json.Unmarshal(data, &info); err != nil {
			r.log.Warn("skipping corrupt host record", "id", id, "error", err)
			continue
		}
		r.hosts[id] = &HostState{
			Info:      info,
			Connected: false, // not connected until stream opens
		}
	}

	r.log.Info("loaded cluster hosts from store", "count", len(r.hosts))
	return nil
}

// Register adds a newly enrolled host to the registry and persists it.
func (r *Registry) Register(info cluster.HostInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal host info: %w", err)
	}
	if err := r.store.SaveClusterHost(info.ID, data); err != nil {
		return fmt.Errorf("persist host %s: %w", info.ID, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.hosts[info.ID] = &HostState{
		Info:      info,
		Connected: false,
	}

	r.log.Info("registered new host", "id", info.ID, "name", info.Name)
	return nil
}

// UpdateLastSeen updates the host's LastSeen timestamp and persists it.
// Called on every heartbeat and on stream disconnect.
func (r *Registry) UpdateLastSeen(hostID string, t time.Time) error {
	r.mu.Lock()
	hs, ok := r.hosts[hostID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("host %s not found", hostID)
	}
	hs.Info.LastSeen = t
	data, err := json.Marshal(hs.Info)
	r.mu.Unlock()

	if err != nil {
		return fmt.Errorf("marshal host info: %w", err)
	}
	return r.store.SaveClusterHost(hostID, data)
}

// SetConnected marks a host as connected or disconnected.
// This is ephemeral state -- not persisted. On server restart all hosts
// start as disconnected until their agents reconnect.
func (r *Registry) SetConnected(hostID string, connected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if hs, ok := r.hosts[hostID]; ok {
		hs.Connected = connected
	}
}

// UpdateContainers replaces the known container list for a host.
// Only stored in memory -- the server doesn't persist remote container lists
// because the agent will re-report them on reconnection.
func (r *Registry) UpdateContainers(hostID string, containers []cluster.ContainerInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if hs, ok := r.hosts[hostID]; ok {
		hs.Containers = containers
		hs.LastReport = time.Now()
	}
}

// UpdateContainerState patches the State field of a single container in the
// cached list for the given host. Used to keep the cache consistent after
// an action (stop/start/restart) completes without waiting for a full
// container list refresh.
func (r *Registry) UpdateContainerState(hostID, containerName, newState string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	hs, ok := r.hosts[hostID]
	if !ok {
		return
	}
	for i := range hs.Containers {
		if hs.Containers[i].Name == containerName {
			hs.Containers[i].State = newState
			return
		}
	}
}

// Get returns the host state for the given ID, or nil if not found.
func (r *Registry) Get(hostID string) (*HostState, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	hs, ok := r.hosts[hostID]
	return hs, ok
}

// UpdateCertSerial atomically updates the stored cert serial for a host,
// revoking the old serial if one exists.
func (r *Registry) UpdateCertSerial(hostID, newSerial string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	hs, ok := r.hosts[hostID]
	if !ok {
		return fmt.Errorf("host %s not found", hostID)
	}

	if hs.Info.CertSerial != "" {
		if err := r.store.AddRevokedCert(hs.Info.CertSerial); err != nil {
			return fmt.Errorf("revoke old cert: %w", err)
		}
	}

	hs.Info.CertSerial = newSerial
	data, err := json.Marshal(hs.Info)
	if err != nil {
		return fmt.Errorf("marshal host info: %w", err)
	}
	return r.store.SaveClusterHost(hostID, data)
}

// GetCertSerial returns the current cert serial for a host, read under lock.
func (r *Registry) GetCertSerial(hostID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if hs, ok := r.hosts[hostID]; ok {
		return hs.Info.CertSerial
	}
	return ""
}

// AllHosts returns HostInfo for all registered hosts.
// Returned slice is a snapshot -- safe to use after the lock is released.
func (r *Registry) AllHosts() []cluster.HostInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]cluster.HostInfo, 0, len(r.hosts))
	for _, hs := range r.hosts {
		out = append(out, hs.Info)
	}
	return out
}

// SetState transitions a host to a new lifecycle state and persists it.
// Valid transitions: active -> draining -> decommissioned.
func (r *Registry) SetState(hostID string, state cluster.HostState) error {
	r.mu.Lock()
	hs, ok := r.hosts[hostID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("host %s not found", hostID)
	}

	old := hs.Info.State
	hs.Info.State = state
	data, err := json.Marshal(hs.Info)
	r.mu.Unlock()

	if err != nil {
		return fmt.Errorf("marshal host info: %w", err)
	}
	if err := r.store.SaveClusterHost(hostID, data); err != nil {
		return fmt.Errorf("persist host state: %w", err)
	}

	r.log.Info("host state changed", "id", hostID, "from", old, "to", state)
	return nil
}

// Remove deletes a host from both the in-memory map and BoltDB.
// Typically called after decommissioning (certs revoked, data cleaned up).
func (r *Registry) Remove(hostID string) error {
	r.mu.Lock()
	delete(r.hosts, hostID)
	r.mu.Unlock()

	if err := r.store.DeleteClusterHost(hostID); err != nil {
		return fmt.Errorf("delete host %s from store: %w", hostID, err)
	}

	r.log.Info("removed host from registry", "id", hostID)
	return nil
}
