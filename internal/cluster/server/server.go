package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
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

// stopGracePeriod bounds how long Stop waits for in-flight RPCs (enrolment,
// state reports) to finish once the agent streams have been ended.
const stopGracePeriod = 5 * time.Second

// Causes recorded when the server itself ends an agent stream.
var (
	errServerStopping = errors.New("cluster server stopped")
	errStreamReplaced = errors.New("replaced by a newer connection from the same agent")
	errHostRemoved    = errors.New("host removed from the cluster")
)

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

	grpcSrv  *grpc.Server
	mu       sync.RWMutex            // protects streams and stopping
	streams  map[string]*agentStream // hostID -> active stream
	stopping bool                    // set by Stop; new agent streams are refused

	// pendingMu protects the pending map and agentStream.closed. Separate
	// from mu to avoid holding the streams lock while waiting for responses.
	pendingMu sync.Mutex
	// pending maps "hostID:requestID" to the request waiting for the
	// agent's response. Multiple concurrent requests per host are supported.
	pending map[string]*pendingRequest

	// lateMu guards late: requests the hub stopped waiting for, by
	// "hostID:requestID", with what they were for.
	lateMu sync.Mutex
	late   map[string]lateRequest

	// renewMu guards lastRenewal: when the hub last signed a renewal for
	// each host.
	renewMu     sync.Mutex
	lastRenewal map[string]time.Time

	// certMu guards the fields below: the server's own TLS certificate.
	certMu      sync.Mutex
	serverCert  *tls.Certificate
	certRenewAt time.Time
	certSANs    []string
}

// pendingRequest is a synchronous request waiting for an agent's reply.
type pendingRequest struct {
	ch chan *proto.AgentMessage
	// stream is the stream the request went out on, nil until it is sent.
	// Only that stream's cleanup fails the request: a reply can only come
	// back on the stream that carried the request.
	stream *agentStream
}

// agentStream tracks an active bidirectional stream with an agent.
// One per connected agent; removed on disconnect.
type agentStream struct {
	hostID string
	send   chan *proto.ServerMessage
	// cancel ends the stream's Channel handler, which closes the stream
	// on the agent. The cause becomes the host's disconnect reason.
	cancel context.CancelCauseFunc
	// done is closed when the stream ends.
	done <-chan struct{}
	// peerCert is the client certificate the agent connected with.
	peerCert *x509.Certificate

	// closed is set (under Server.pendingMu) once the stream's cleanup has
	// failed its pending requests, so no new request is tagged to it.
	closed bool

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
		pending:  make(map[string]*pendingRequest),
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

	// Issue an ephemeral server certificate from our CA. It is renewed in
	// place before it expires (serverCertificate).
	s.certMu.Lock()
	s.certSANs = extraSANs
	err := s.issueServerCertLocked()
	s.certMu.Unlock()
	if err != nil {
		return err
	}

	// Build client CA pool from our CA cert so we can verify agent certs.
	caCertPEM := s.ca.CACertPEM()
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCertPEM) {
		return fmt.Errorf("failed to add CA cert to pool")
	}

	tlsCfg := s.buildTLSConfig(caPool)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	creds := credentials.NewTLS(tlsCfg)
	s.grpcSrv = grpc.NewServer(
		grpc.Creds(creds),
		grpc.MaxRecvMsgSize(maxMessageSize),
		grpc.MaxSendMsgSize(maxMessageSize),
		grpc.StatsHandler(&oversizeLogger{log: s.log}),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: keepaliveTime, Timeout: keepaliveTimeout}),
		// Agents ping every 30 seconds; allow that, idle streams included.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 20 * time.Second, PermitWithoutStream: true}),
	)

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

// issueServerCertLocked issues a fresh server certificate and puts it in
// use. Caller holds certMu.
func (s *Server) issueServerCertLocked() error {
	certPEM, keyPEM, err := s.ca.IssueServerCert(s.certSANs...)
	if err != nil {
		return fmt.Errorf("issue server cert: %w", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("parse server keypair: %w", err)
	}
	if err := s.installServerCertLocked(cert); err != nil {
		return err
	}
	s.log.Info("issued cluster server certificate", "expires", cert.Leaf.NotAfter.Format(time.RFC3339))
	return nil
}

// installServerCertLocked puts cert in use and schedules its renewal two
// thirds of the way through its life, from its own dates. Caller holds
// certMu.
func (s *Server) installServerCertLocked(cert tls.Certificate) error {
	if cert.Leaf == nil {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return fmt.Errorf("parse server cert: %w", err)
		}
		cert.Leaf = leaf
	}
	life := cert.Leaf.NotAfter.Sub(cert.Leaf.NotBefore)
	s.serverCert = &cert
	s.certRenewAt = cert.Leaf.NotBefore.Add(life * 2 / 3)
	return nil
}

// serverCertificate is the listener's GetCertificate hook. A hub that runs
// for most of a year renews its certificate here, on the first handshake
// after the renewal point, so agents never meet an expired one.
func (s *Server) serverCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.certMu.Lock()
	defer s.certMu.Unlock()
	if s.serverCert == nil || !time.Now().Before(s.certRenewAt) {
		if err := s.issueServerCertLocked(); err != nil {
			if s.serverCert == nil {
				return nil, err
			}
			s.log.Error("could not renew the cluster server certificate, still using the current one", "error", err)
		}
	}
	return s.serverCert, nil
}

// buildTLSConfig assembles the mTLS configuration for the gRPC listener.
func (s *Server) buildTLSConfig(caPool *x509.CertPool) *tls.Config {
	return &tls.Config{
		GetCertificate: s.serverCertificate,
		ClientCAs:      caPool,
		MinVersion:     tls.VersionTLS13,

		// VerifyClientCertIfGiven allows enrollment without a client cert
		// while still verifying certs when they are presented. The
		// AgentService methods enforce cert presence explicitly.
		ClientAuth: tls.VerifyClientCertIfGiven,

		// Custom verification hook to check the certificate revocation list.
		// Runs after the standard chain verification succeeds.
		VerifyPeerCertificate: s.verifyCRL,

		// Session resumption skips VerifyPeerCertificate, which would let a
		// revoked agent cert reconnect for the lifetime of a session ticket.
		// Resumption buys nothing for long-lived gRPC connections, so turn
		// it off and force the CRL check on every handshake (gosec G123).
		SessionTicketsDisabled: true,
	}
}

// Stop shuts the gRPC server down. Agent streams never end on their own
// (agents keep them open for as long as they run), so Stop ends them first:
// each agent sees its stream close and goes back to reconnecting. It then
// lets other in-flight RPCs finish for up to stopGracePeriod before closing
// whatever connections remain.
func (s *Server) Stop() {
	if s.grpcSrv == nil {
		return
	}
	s.log.Info("stopping cluster gRPC server")

	s.mu.Lock()
	s.stopping = true
	for _, as := range s.streams {
		as.cancel(errServerStopping)
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.grpcSrv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(stopGracePeriod):
		s.log.Warn("cluster gRPC server did not drain in time, closing remaining connections")
		s.grpcSrv.Stop()
		<-done
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
	// Mid-renewal a host has two valid certificates: the one in use and
	// the renewed one it has not connected with yet. Both are revoked in
	// the same step that removes the host, so a renewal cannot slip in
	// between.
	revoked, err := s.registry.RevokeAndRemove(id)
	for _, serial := range revoked {
		s.log.Info("revoked certificate", "hostID", id, "serial", serial)
	}
	if err != nil && len(revoked) == 0 {
		return err
	}
	s.disconnectAgent(id)
	return err
}

// authoriseCert checks that the certificate a peer presented is one the
// hub issued to hostID: its current certificate, or its renewed one, which
// becomes current here (and the certificate it replaces is revoked).
func (s *Server) authoriseCert(ctx context.Context, hostID string) error {
	serial, _ := peerCertSerial(ctx)
	match, err := s.registry.MatchCertSerial(hostID, serial)
	if err != nil {
		s.log.Error("failed to record the agent's certificate serial", "hostID", hostID, "serial", serial, "error", err)
		return status.Error(codes.Internal, "certificate check unavailable")
	}
	switch match {
	case certCurrent:
		return nil
	case certPending:
		replaced, promoted, err := s.registry.PromoteCertSerial(hostID, serial)
		if err != nil {
			s.log.Error("failed to record the agent's renewed certificate", "hostID", hostID, "serial", serial, "error", err)
			return status.Error(codes.Internal, "certificate check unavailable")
		}
		if promoted {
			s.log.Info("agent is using its renewed certificate, old one revoked", "hostID", hostID, "serial", serial, "revoked", replaced)
		}
		return nil
	default:
		s.log.Warn("agent presented a certificate the hub did not issue to it", "hostID", hostID, "serial", serial)
		return status.Error(codes.PermissionDenied, "certificate is not the one issued to this host")
	}
}

// disconnectAgent cancels the active stream for a host, if one exists.
func (s *Server) disconnectAgent(id string) {
	s.mu.Lock()
	if as, ok := s.streams[id]; ok {
		as.cancel(errHostRemoved)
		delete(s.streams, id)
	}
	s.mu.Unlock()
}

// maxMessageSize is the largest gRPC message the cluster server sends or
// accepts. A variable so tests can lower it.
var maxMessageSize = cluster.MaxMessageSize

// renewalAcceptFraction: the hub signs a renewal only once the agent's
// current certificate has less than this fraction of its life left.
// Agents ask at one third; the margin allows for clock differences. A
// variable so tests can renew fresh certificates.
var renewalAcceptFraction = 0.5

// renewalMinInterval is the shortest time between two renewals the hub
// signs for one host. Agents wait ten minutes before asking again.
const renewalMinInterval = 5 * time.Minute

// refuseRenewal says why the hub will not sign this renewal request, or
// returns "" and counts it against the host's rate limit. The agent's
// current certificate must be due for renewal, and the request must be
// for a new key.
func (s *Server) refuseRenewal(hostID string, as *agentStream, csrDER []byte) string {
	if leaf := as.peerCert; leaf != nil {
		life := leaf.NotAfter.Sub(leaf.NotBefore)
		if left := time.Until(leaf.NotAfter); float64(left) > float64(life)*renewalAcceptFraction {
			return "the current certificate is not due for renewal"
		}
		csr, err := x509.ParseCertificateRequest(csrDER)
		if err != nil {
			return "the request is not a valid CSR"
		}
		if pub, ok := leaf.PublicKey.(interface{ Equal(crypto.PublicKey) bool }); ok && pub.Equal(csr.PublicKey) {
			return "the request reuses the key of the current certificate"
		}
	}

	s.renewMu.Lock()
	defer s.renewMu.Unlock()
	if last, ok := s.lastRenewal[hostID]; ok && time.Since(last) < renewalMinInterval {
		return "a certificate was renewed for this host minutes ago"
	}
	if s.lastRenewal == nil {
		s.lastRenewal = make(map[string]time.Time)
	}
	s.lastRenewal[hostID] = time.Now()
	return ""
}

// Keepalive: the hub pings an agent connection that has been idle for
// keepaliveTime and drops it if no answer comes within keepaliveTimeout,
// so a partitioned agent is noticed in well under a minute. Variables so
// tests can shorten them.
var (
	keepaliveTime    = 30 * time.Second
	keepaliveTimeout = 10 * time.Second
)
