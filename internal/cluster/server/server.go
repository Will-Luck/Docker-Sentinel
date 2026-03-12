package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// HistoryRecorder is the subset of store.Store needed for persisting update
// history. Defined as an interface so the cluster server doesn't import
// the store package directly, matching the ClusterStore pattern.
type HistoryRecorder interface {
	RecordUpdate(rec store.UpdateRecord) error
}

// ClusterStore is the subset of store.Store needed by the cluster server.
// Defined as an interface for dependency injection -- avoids importing the
// store package directly, matching the pattern used by the web package.
type ClusterStore interface {
	SaveClusterHost(id string, data []byte) error
	GetClusterHost(id string) ([]byte, error)
	ListClusterHosts() (map[string][]byte, error)
	DeleteClusterHost(id string) error
	SaveEnrollToken(id string, data []byte) error
	GetEnrollToken(id string) ([]byte, error)
	DeleteEnrollToken(id string) error
	AddRevokedCert(serial string) error
	IsRevokedCert(serial string) (bool, error)
	ListRevokedCerts() (map[string]string, error)
}

// sendBufferSize is the channel buffer for outbound messages to each agent.
// Large enough to absorb short bursts without blocking the server, but small
// enough that a truly stalled agent gets dropped rather than consuming memory.
const sendBufferSize = 64

// Server manages the gRPC server for cluster communication.
// It implements both EnrollmentServiceServer (unauthenticated, token-based)
// and AgentServiceServer (requires mTLS client certificate).
type Server struct {
	proto.UnimplementedEnrollmentServiceServer
	proto.UnimplementedAgentServiceServer

	ca       *cluster.CA
	registry *Registry
	store    ClusterStore
	history  HistoryRecorder
	bus      *events.Bus
	log      *slog.Logger
	hmacKey  []byte // 32-byte random key for HMAC-SHA256 token signing

	grpcSrv *grpc.Server
	mu      sync.RWMutex            // protects streams map
	streams map[string]*agentStream // hostID -> active stream

	// pendingMu protects the pending map. Separate from mu to avoid holding
	// the streams lock while waiting for responses.
	pendingMu sync.Mutex
	// pending maps "hostID:requestID" to a channel that receives the
	// corresponding response from the agent. Multiple concurrent requests
	// per host are supported.
	pending map[string]chan *proto.AgentMessage
}

// agentStream tracks an active bidirectional stream with an agent.
// One per connected agent; removed on disconnect.
type agentStream struct {
	hostID string
	send   chan *proto.ServerMessage
	cancel context.CancelFunc

	mu       sync.RWMutex // protects version and features
	features []string
	version  string
}

// New creates a cluster server. Call Start() to begin listening.
//
// The HMAC key used for enrollment token signing is loaded from
// hmac-key.bin in the CA directory. If the file doesn't exist, a
// fresh 32-byte random key is generated and persisted. This ensures
// the signing key is stable across restarts but not derivable from
// public material (unlike the previous approach of using the CA cert).
func New(ca *cluster.CA, store ClusterStore, bus *events.Bus, log *slog.Logger) (*Server, error) {
	key, err := loadOrGenerateHMACKey(ca.Dir())
	if err != nil {
		return nil, fmt.Errorf("hmac key: %w", err)
	}

	return &Server{
		ca:       ca,
		hmacKey:  key,
		registry: NewRegistry(store, log.With("component", "registry")),
		store:    store,
		bus:      bus,
		log:      log.With("component", "cluster-server"),
		streams:  make(map[string]*agentStream),
		pending:  make(map[string]chan *proto.AgentMessage),
	}, nil
}

// hmacKeyFile is the filename for the persisted HMAC signing key.
const hmacKeyFile = "hmac-key.bin"

// loadOrGenerateHMACKey loads a 32-byte HMAC key from dir/hmac-key.bin,
// or generates a new one if the file doesn't exist. The key is stored
// with 0600 permissions so only the Sentinel process can read it.
func loadOrGenerateHMACKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, hmacKeyFile)

	data, err := os.ReadFile(path)
	if err == nil && len(data) == 32 {
		return data, nil
	}

	// Generate fresh key.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate hmac key: %w", err)
	}

	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, fmt.Errorf("persist hmac key: %w", err)
	}

	return key, nil
}

// Registry returns the server's host registry for external inspection.
func (s *Server) Registry() *Registry {
	return s.registry
}

// SetHistoryRecorder wires in a history store for persisting replayed
// journal entries. Called after construction by main.go.
func (s *Server) SetHistoryRecorder(h HistoryRecorder) {
	s.history = h
}

// Start starts the gRPC server with mTLS on the given address.
// extraSANs are additional IPs or hostnames to include in the server
// certificate (e.g. the Docker host's external IP that agents connect to).
//
// TLS is configured with VerifyClientCertIfGiven so that enrollment
// (which happens before the agent has a cert) can proceed unauthenticated.
// AgentService methods check for a valid client cert and reject calls
// without one.
func (s *Server) Start(addr string, extraSANs ...string) error {
	// Load persisted hosts before accepting connections.
	if err := s.registry.LoadFromStore(); err != nil {
		return fmt.Errorf("load registry: %w", err)
	}

	// Issue an ephemeral server certificate from our CA.
	certPEM, keyPEM, err := s.ca.IssueServerCert(extraSANs...)
	if err != nil {
		return fmt.Errorf("issue server cert: %w", err)
	}

	serverCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("parse server keypair: %w", err)
	}

	// Build client CA pool from our CA cert so we can verify agent certs.
	caCertPEM := s.ca.CACertPEM()
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCertPEM) {
		return fmt.Errorf("failed to add CA cert to pool")
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		MinVersion:   tls.VersionTLS13,

		// VerifyClientCertIfGiven allows enrollment without a client cert
		// while still verifying certs when they are presented. The
		// AgentService methods enforce cert presence explicitly.
		ClientAuth: tls.VerifyClientCertIfGiven,

		// Custom verification hook to check the certificate revocation list.
		// Runs after the standard chain verification succeeds.
		VerifyPeerCertificate: s.verifyCRL,
	}

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	creds := credentials.NewTLS(tlsCfg)
	s.grpcSrv = grpc.NewServer(grpc.Creds(creds))

	proto.RegisterEnrollmentServiceServer(s.grpcSrv, s)
	proto.RegisterAgentServiceServer(s.grpcSrv, s)

	s.log.Info("cluster gRPC server starting", "addr", lis.Addr().String())

	// Serve in a goroutine so Start() returns immediately.
	go func() {
		if err := s.grpcSrv.Serve(lis); err != nil {
			s.log.Error("gRPC server exited", "error", err)
		}
	}()

	return nil
}

// Stop gracefully stops the gRPC server. Waits for active RPCs to finish.
func (s *Server) Stop() {
	if s.grpcSrv != nil {
		s.log.Info("stopping cluster gRPC server")
		s.grpcSrv.GracefulStop()
	}
}

// SendCommand sends a server message to a specific connected agent.
// Returns an error if the agent is not currently connected.
func (s *Server) SendCommand(hostID string, msg *proto.ServerMessage) error {
	s.mu.RLock()
	as, ok := s.streams[hostID]
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("agent %s not connected", hostID)
	}

	select {
	case as.send <- msg:
		return nil
	default:
		// Buffer full -- agent is not consuming messages fast enough.
		return fmt.Errorf("agent %s send buffer full", hostID)
	}
}

// ConnectedHosts returns the IDs of all currently connected agents.
func (s *Server) ConnectedHosts() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]string, 0, len(s.streams))
	for id := range s.streams {
		ids = append(ids, id)
	}
	return ids
}

// AllHosts delegates to the registry, returning info for every registered host.
func (s *Server) AllHosts() []cluster.HostInfo {
	return s.registry.AllHosts()
}

// GetHost delegates to the registry, returning the full host state.
func (s *Server) GetHost(id string) (*HostState, bool) {
	return s.registry.Get(id)
}

// RemoveHost deletes a host from the registry and disconnects its stream.
func (s *Server) RemoveHost(id string) error {
	s.disconnectAgent(id)
	return s.registry.Remove(id)
}

// PauseHost sets a host to "paused" state -- no new updates will be dispatched.
func (s *Server) PauseHost(id string) error {
	return s.registry.SetState(id, cluster.HostPaused)
}

// RevokeHost revokes the host's certificate, disconnects its stream, and
// removes it from the registry. The cert serial is added to the CRL so
// the agent can no longer authenticate.
func (s *Server) RevokeHost(id string) error {
	serial := s.registry.GetCertSerial(id)
	if serial != "" {
		if err := s.store.AddRevokedCert(serial); err != nil {
			return fmt.Errorf("revoke cert: %w", err)
		}
		s.log.Info("revoked certificate", "hostID", id, "serial", serial)
	}

	s.disconnectAgent(id)
	return s.registry.Remove(id)
}

// disconnectAgent cancels the active stream for a host, if one exists.
func (s *Server) disconnectAgent(id string) {
	s.mu.Lock()
	if as, ok := s.streams[id]; ok {
		as.cancel()
		delete(s.streams, id)
	}
	s.mu.Unlock()
}
