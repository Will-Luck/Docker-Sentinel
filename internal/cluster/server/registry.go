// Package server implements the gRPC server for cluster communication.
// It handles agent enrollment, bidirectional streaming, and host tracking.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// persistInterval controls how often UpdateLastSeen writes to BoltDB.
// In-memory LastSeen is always current; disk is only updated when this
// interval has elapsed since the last persist (or on disconnect).
const persistInterval = 5 * time.Minute

// HostState is the server-side view of a connected agent.
// It pairs the persisted HostInfo with ephemeral runtime state (container list,
// connectivity status) that only exists while the server is running.
type HostState struct {
	Info          cluster.HostInfo
	Containers    []cluster.ContainerInfo // latest known container list
	Connected     bool                    // true while the agent stream is active
	LastReport    time.Time               // when the last container list was received
	DisconnectAt  time.Time
	DisconnectErr string
	DisconnectCat string    // "network", "cert", "server", ""
	lastPersist   time.Time // when LastSeen was last written to BoltDB
	// EngineSharedWith lists other hosts that report the same Docker
	// engine ID: the same daemon enrolled more than once.
	EngineSharedWith []string
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

// Locking: r.mu guards the map and is never held across a store call, so
// one slow database write cannot hold up other hosts. Writes go through
// persist, which checks inside the write transaction that the host is
// still registered, so a save racing with Remove never writes a removed
// host back.

// Register adds a newly enrolled host to the registry and persists it.
func (r *Registry) Register(info cluster.HostInfo) error {
	r.mu.Lock()
	r.hosts[info.ID] = &HostState{
		Info:      info,
		Connected: false,
	}
	r.mu.Unlock()

	if err := r.persist(info.ID); err != nil {
		r.mu.Lock()
		delete(r.hosts, info.ID)
		r.mu.Unlock()
		return fmt.Errorf("persist host %s: %w", info.ID, err)
	}

	r.log.Info("registered new host", "id", info.ID, "name", info.Name)
	return nil
}

// UpdateAddress stores the agent's IP so the dashboard can link ports
// to the correct host. Called on each Channel() connection.
func (r *Registry) UpdateAddress(hostID, addr string) {
	r.mu.Lock()
	hs, ok := r.hosts[hostID]
	if ok {
		hs.Info.Address = addr
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	if err := r.persist(hostID); err != nil {
		r.log.Warn("persist host address", "error", err)
	}
}

// UpdateLastSeen updates the host's LastSeen timestamp in memory on every
// heartbeat, but only persists to BoltDB when persistInterval has elapsed
// since the last write. Use PersistLastSeen for forced writes (e.g. on disconnect).
func (r *Registry) UpdateLastSeen(hostID string, t time.Time) error {
	r.mu.Lock()
	hs, ok := r.hosts[hostID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("host %s not found", hostID)
	}
	hs.Info.LastSeen = t
	due := time.Since(hs.lastPersist) >= persistInterval
	if due {
		hs.lastPersist = t
	}
	r.mu.Unlock()

	if !due {
		return nil
	}
	return r.persist(hostID)
}

// PersistLastSeen forces a BoltDB write of the host's current LastSeen.
// Called on disconnect so the timestamp survives a server restart.
func (r *Registry) PersistLastSeen(hostID string) error {
	r.mu.Lock()
	hs, ok := r.hosts[hostID]
	if ok {
		hs.lastPersist = hs.Info.LastSeen
	}
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("host %s not found", hostID)
	}
	return r.persist(hostID)
}

// SetConnected marks a host as connected or disconnected.
// This is ephemeral state -- not persisted. On server restart all hosts
// start as disconnected until their agents reconnect.
func (r *Registry) SetConnected(hostID string, connected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if hs, ok := r.hosts[hostID]; ok {
		hs.Connected = connected
		if connected {
			hs.DisconnectAt = time.Time{}
			hs.DisconnectErr = ""
			hs.DisconnectCat = ""
		}
	}
}

func classifyDisconnect(err error) (cat, msg string) {
	if err == nil {
		return "server", "stream closed cleanly"
	}
	s := err.Error()
	st, ok := status.FromError(err)
	if ok {
		switch st.Code() {
		case codes.PermissionDenied:
			return "cert", "permission denied: " + st.Message()
		case codes.NotFound:
			return "cert", "host not registered on server"
		case codes.Canceled:
			return "server", "stream cancelled"
		case codes.DeadlineExceeded:
			return "network", "connection timed out"
		case codes.Unavailable:
			return "network", st.Message()
		}
	}
	if errors.Is(err, io.EOF) || strings.Contains(s, "EOF") {
		return "network", "connection lost (EOF)"
	}
	if strings.Contains(s, "connection reset") {
		return "network", "connection reset by peer"
	}
	if strings.Contains(s, "transport is closing") {
		return "network", "transport closed"
	}
	return "server", err.Error()
}

func (r *Registry) SetDisconnected(hostID string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if hs, ok := r.hosts[hostID]; ok {
		hs.Connected = false
		hs.DisconnectAt = time.Now()
		cat, msg := classifyDisconnect(err)
		hs.DisconnectCat = cat
		hs.DisconnectErr = msg
	}
}

// UpdateEngineID stores the Docker Engine ID reported by an agent.
// Persisted to BoltDB so the cluster page can show it.
func (r *Registry) UpdateEngineID(hostID, engineID string) error {
	r.mu.Lock()
	hs, ok := r.hosts[hostID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("host %s not found", hostID)
	}
	if hs.Info.EngineID == engineID {
		r.mu.Unlock()
		return nil // no change
	}
	hs.Info.EngineID = engineID
	r.mu.Unlock()

	if err := r.persist(hostID); err != nil {
		return fmt.Errorf("persist engine ID: %w", err)
	}
	r.log.Info("stored engine ID", "hostID", hostID, "engine_id", engineID)
	return nil
}

// UpdateAgentInfo stores the version and features an agent reports and,
// when name is not empty, the host name it now runs with
// (SENTINEL_HOST_NAME, which may have changed since it enrolled). Persisted
// only when they change; nil features leave the stored ones.
func (r *Registry) UpdateAgentInfo(hostID, version, name string, features []string) error {
	r.mu.Lock()
	hs, ok := r.hosts[hostID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("host %s not found", hostID)
	}
	changed := false
	if version != "" && hs.Info.AgentVersion != version {
		hs.Info.AgentVersion = version
		changed = true
	}
	if features != nil && !slices.Equal(hs.Info.Features, features) {
		hs.Info.Features = slices.Clone(features)
		changed = true
	}
	oldName := hs.Info.Name
	if name != "" && oldName != name {
		hs.Info.Name = name
		changed = true
	}
	r.mu.Unlock()
	if !changed {
		return nil
	}
	if name != "" && oldName != name {
		r.log.Info("agent host renamed", "hostID", hostID, "from", oldName, "to", name)
	}
	return r.persist(hostID)
}

// RefreshEngineSharing recomputes, for every host, which other hosts report
// the same Docker engine ID, and returns the ones sharing hostID's engine.
func (r *Registry) RefreshEngineSharing(hostID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	byEngine := make(map[string][]string)
	for id, hs := range r.hosts {
		if hs.Info.EngineID != "" {
			byEngine[hs.Info.EngineID] = append(byEngine[hs.Info.EngineID], id)
		}
	}
	for id, hs := range r.hosts {
		var others []string
		for _, other := range byEngine[hs.Info.EngineID] {
			if other != id {
				others = append(others, other)
			}
		}
		sort.Strings(others)
		hs.EngineSharedWith = others
	}
	if hs, ok := r.hosts[hostID]; ok {
		return append([]string(nil), hs.EngineSharedWith...)
	}
	return nil
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

// Get returns a deep copy of the host state for the given ID, or nil if not found.
// The returned value is safe to read without holding the registry lock.
func (r *Registry) Get(hostID string) (*HostState, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	hs, ok := r.hosts[hostID]
	if !ok {
		return nil, false
	}

	// Deep copy to avoid callers mutating live state.
	cp := *hs
	if hs.Containers != nil {
		cp.Containers = make([]cluster.ContainerInfo, len(hs.Containers))
		copy(cp.Containers, hs.Containers)
	}
	cp.EngineSharedWith = append([]string(nil), hs.EngineSharedWith...)
	return &cp, true
}

// A renewed agent certificate goes through two steps. SetPendingCertSerial
// records it when the hub signs it; the certificate the agent is using
// stays valid. PromoteCertSerial makes it current when the agent first
// connects with it, and only then revokes the serial it replaces. If the
// reply carrying the new certificate is lost, the agent keeps working on
// the old one and asks again.

// SetPendingCertSerial records serial as the host's renewed certificate,
// issued but not yet seen in use. A previous pending serial is revoked: a
// newer renewal has replaced it before the agent ever connected with it.
func (r *Registry) SetPendingCertSerial(hostID, serial string) error {
	r.mu.Lock()
	hs, ok := r.hosts[hostID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("host %s not found", hostID)
	}
	prev := hs.Info.PendingCertSerial
	hs.Info.PendingCertSerial = serial
	r.mu.Unlock()

	if prev != "" && prev != serial {
		if err := r.store.AddRevokedCert(prev); err != nil {
			return fmt.Errorf("revoke superseded renewal: %w", err)
		}
	}
	return r.persist(hostID)
}

// ClearPendingCertSerial forgets serial if it is still the host's pending
// renewal (the caller has revoked it).
func (r *Registry) ClearPendingCertSerial(hostID, serial string) error {
	r.mu.Lock()
	hs, ok := r.hosts[hostID]
	cleared := ok && hs.Info.PendingCertSerial == serial
	if cleared {
		hs.Info.PendingCertSerial = ""
	}
	r.mu.Unlock()

	if !cleared {
		return nil
	}
	return r.persist(hostID)
}

// PromoteCertSerial is called with the serial an agent presented on
// connect. When that is the host's pending renewal it becomes the current
// serial, and the serial it replaces is revoked and returned.
func (r *Registry) PromoteCertSerial(hostID, presented string) (replaced string, promoted bool, err error) {
	r.mu.RLock()
	hs, ok := r.hosts[hostID]
	if ok {
		replaced = hs.Info.CertSerial
	}
	pending := ok && presented != "" && hs.Info.PendingCertSerial == presented
	r.mu.RUnlock()
	if !pending {
		return "", false, nil
	}

	// Revoke first: if that fails nothing has changed, and the swap is
	// tried again on the next connect.
	if replaced != "" && replaced != presented {
		if err := r.store.AddRevokedCert(replaced); err != nil {
			return "", false, fmt.Errorf("revoke replaced cert: %w", err)
		}
	}

	r.mu.Lock()
	hs, ok = r.hosts[hostID]
	swap := ok && hs.Info.PendingCertSerial == presented && hs.Info.CertSerial == replaced
	if swap {
		hs.Info.CertSerial = presented
		hs.Info.PendingCertSerial = ""
	}
	r.mu.Unlock()
	if !swap {
		return "", false, nil
	}
	if err := r.persist(hostID); err != nil {
		return "", false, err
	}
	return replaced, true, nil
}

// certMatch says how a presented certificate serial relates to a host.
type certMatch int

const (
	certUnknown certMatch = iota // neither of the host's serials
	certCurrent                  // the host's current certificate
	certPending                  // its renewed certificate, not used before
)

// MatchCertSerial classifies the serial an agent presented. A host record
// from before serials were tracked (CertSerial empty) adopts the presented
// serial as current.
func (r *Registry) MatchCertSerial(hostID, presented string) (certMatch, error) {
	r.mu.Lock()
	hs, ok := r.hosts[hostID]
	if !ok || presented == "" {
		r.mu.Unlock()
		return certUnknown, nil
	}
	switch presented {
	case hs.Info.CertSerial:
		r.mu.Unlock()
		return certCurrent, nil
	case hs.Info.PendingCertSerial:
		r.mu.Unlock()
		return certPending, nil
	}
	adopt := hs.Info.CertSerial == ""
	if adopt {
		hs.Info.CertSerial = presented
	}
	r.mu.Unlock()

	if !adopt {
		return certUnknown, nil
	}
	r.log.Info("recorded the certificate serial of a host enrolled before serials were tracked", "id", hostID, "serial", presented)
	return certCurrent, r.persist(hostID)
}

// RevokeAndRemove removes the host and revokes every certificate it holds
// (its current one and any pending renewal). The host leaves the map and
// its serials are read in one hold of the registry lock, so a renewal
// racing with it either lands first, and its serial is revoked here, or
// finds the host gone and revokes its own certificate. A removed host can
// no longer connect even if a revocation write fails.
func (r *Registry) RevokeAndRemove(hostID string) (revoked []string, err error) {
	r.mu.Lock()
	var serials []string
	if hs, ok := r.hosts[hostID]; ok {
		serials = []string{hs.Info.CertSerial, hs.Info.PendingCertSerial}
	}
	delete(r.hosts, hostID)
	r.mu.Unlock()

	var revokeErr error
	for _, serial := range serials {
		if serial == "" {
			continue
		}
		if err := r.store.AddRevokedCert(serial); err != nil {
			revokeErr = fmt.Errorf("revoke cert %s: %w", serial, err)
			continue
		}
		revoked = append(revoked, serial)
	}
	if err := r.store.DeleteClusterHost(hostID); err != nil {
		return revoked, fmt.Errorf("delete host %s from store: %w", hostID, err)
	}
	r.log.Info("revoked and removed host", "id", hostID)
	return revoked, revokeErr
}

// persist writes the host's current record to the store, in a write
// transaction that first checks the host is still registered. Callers do
// not hold r.mu.
func (r *Registry) persist(hostID string) error {
	return r.store.SaveClusterHostWith(hostID, func() ([]byte, error) {
		r.mu.RLock()
		defer r.mu.RUnlock()
		hs, ok := r.hosts[hostID]
		if !ok {
			return nil, nil
		}
		data, err := json.Marshal(hs.Info)
		if err != nil {
			return nil, fmt.Errorf("marshal host info: %w", err)
		}
		return data, nil
	})
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
// Valid transitions: active -> paused -> decommissioned.
func (r *Registry) SetState(hostID string, state cluster.HostState) error {
	r.mu.Lock()
	hs, ok := r.hosts[hostID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("host %s not found", hostID)
	}
	old := hs.Info.State
	hs.Info.State = state
	r.mu.Unlock()

	if err := r.persist(hostID); err != nil {
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
