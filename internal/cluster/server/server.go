package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

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
	bus      *events.Bus
	log      *slog.Logger

	grpcSrv *grpc.Server
	mu      sync.RWMutex            // protects streams map
	streams map[string]*agentStream // hostID -> active stream

	// pendingMu protects the pending map. Separate from mu to avoid holding
	// the streams lock while waiting for responses.
	pendingMu sync.Mutex
	// pending maps hostID to a channel that receives the next response from
	// that agent. Only one outstanding synchronous request per host at a time.
	pending map[string]chan *proto.AgentMessage
}

// agentStream tracks an active bidirectional stream with an agent.
// One per connected agent; removed on disconnect.
type agentStream struct {
	hostID   string
	send     chan *proto.ServerMessage
	cancel   context.CancelFunc
	features []string
	version  string
}

// New creates a cluster server. Call Start() to begin listening.
func New(ca *cluster.CA, store ClusterStore, bus *events.Bus, log *slog.Logger) *Server {
	return &Server{
		ca:       ca,
		registry: NewRegistry(store, log.With("component", "registry")),
		store:    store,
		bus:      bus,
		log:      log.With("component", "cluster-server"),
		streams:  make(map[string]*agentStream),
		pending:  make(map[string]chan *proto.AgentMessage),
	}
}

// Registry returns the server's host registry for external inspection.
func (s *Server) Registry() *Registry {
	return s.registry
}

// Start starts the gRPC server with mTLS on the given address.
//
// TLS is configured with VerifyClientCertIfGiven so that enrollment
// (which happens before the agent has a cert) can proceed unauthenticated.
// AgentService methods check for a valid client cert and reject calls
// without one.
func (s *Server) Start(addr string) error {
	// Load persisted hosts before accepting connections.
	if err := s.registry.LoadFromStore(); err != nil {
		return fmt.Errorf("load registry: %w", err)
	}

	// Issue an ephemeral server certificate from our CA.
	certPEM, keyPEM, err := s.ca.IssueServerCert()
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

// ---------------------------------------------------------------------------
// EnrollmentService
// ---------------------------------------------------------------------------

// Enroll handles a new agent registration using a one-time token.
//
// Flow:
// 1. Validate the enrollment token (HMAC comparison)
// 2. Mark the token as used
// 3. Sign the agent's CSR with our CA
// 4. Persist the new host record
// 5. Return the host ID, CA cert, and signed agent cert
func (s *Server) Enroll(ctx context.Context, req *proto.EnrollRequest) (*proto.EnrollResponse, error) {
	if req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}
	if len(req.Csr) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CSR is required")
	}

	// Token ID is the first 8 hex characters (4 bytes) of the plaintext token.
	// This lets us look up the token record without storing the full value.
	if len(req.Token) < 8 {
		return nil, status.Error(codes.InvalidArgument, "token too short")
	}
	tokenID := req.Token[:8]

	// Look up and validate the token.
	tok, err := s.loadEnrollToken(tokenID)
	if err != nil {
		s.log.Warn("enrollment failed: token lookup", "tokenID", tokenID, "error", err)
		return nil, status.Error(codes.PermissionDenied, "invalid enrollment token")
	}
	if tok.Used {
		return nil, status.Error(codes.PermissionDenied, "token already used")
	}
	if time.Now().After(tok.ExpiresAt) {
		return nil, status.Error(codes.PermissionDenied, "token expired")
	}

	// Verify HMAC: hash the provided plaintext and compare with stored hash.
	expectedMAC := s.hmacToken(req.Token)
	if !hmac.Equal(expectedMAC, tok.Hash) {
		return nil, status.Error(codes.PermissionDenied, "invalid enrollment token")
	}

	// Mark token as used before issuing certs (prevent replay on error).
	tok.Used = true
	if err := s.saveEnrollToken(tok); err != nil {
		s.log.Error("failed to mark token used", "tokenID", tokenID, "error", err)
		return nil, status.Error(codes.Internal, "failed to consume token")
	}

	// Generate a unique host ID.
	hostID, err := generateHostID()
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to generate host ID")
	}

	// Sign the CSR. The CN is set to hostID by the CA (overrides whatever
	// the agent put in the CSR subject).
	certPEM, serial, err := s.ca.SignCSR(req.Csr, hostID)
	if err != nil {
		s.log.Error("failed to sign CSR", "hostID", hostID, "error", err)
		return nil, status.Error(codes.Internal, "failed to sign certificate")
	}

	// Persist the new host record.
	now := time.Now()
	info := cluster.HostInfo{
		ID:         hostID,
		Name:       req.HostName,
		State:      cluster.HostActive,
		CertSerial: serial,
		EnrolledAt: now,
		LastSeen:   now,
	}
	if err := s.registry.Register(info); err != nil {
		s.log.Error("failed to register host", "hostID", hostID, "error", err)
		return nil, status.Error(codes.Internal, "failed to register host")
	}

	// Publish a cluster event so SSE clients know about the new host.
	s.bus.Publish(events.SSEEvent{
		Type:      events.EventClusterHost,
		HostName:  req.HostName,
		Message:   fmt.Sprintf("host %s (%s) enrolled", hostID, req.HostName),
		Timestamp: now,
	})

	s.log.Info("agent enrolled", "hostID", hostID, "name", req.HostName, "serial", serial)

	return &proto.EnrollResponse{
		HostId:    hostID,
		CaCert:    s.ca.CACertPEM(),
		AgentCert: certPEM,
	}, nil
}

// GenerateEnrollToken creates a one-time enrollment token.
// The plaintext token is returned to the caller (shown to admin once); only
// the HMAC hash is persisted. Token ID is the first 8 hex chars for lookup.
func (s *Server) GenerateEnrollToken(expiry time.Duration) (token string, id string, err error) {
	// 32 random bytes = 64 hex chars. Plenty of entropy.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate random token: %w", err)
	}

	token = hex.EncodeToString(raw)
	id = token[:8]

	now := time.Now()
	tok := &cluster.EnrollToken{
		ID:        id,
		Hash:      s.hmacToken(token),
		CreatedAt: now,
		ExpiresAt: now.Add(expiry),
		Used:      false,
	}

	if err := s.saveEnrollToken(tok); err != nil {
		return "", "", fmt.Errorf("persist token: %w", err)
	}

	s.log.Info("enrollment token generated", "id", id, "expires", tok.ExpiresAt.Format(time.RFC3339))
	return token, id, nil
}

// ---------------------------------------------------------------------------
// AgentService
// ---------------------------------------------------------------------------

// Channel establishes a bidirectional stream for real-time commands and events.
//
// Authentication: requires a valid mTLS client certificate. The host ID is
// extracted from the certificate's CN field.
//
// The server spawns a send goroutine that drains the agentStream.send channel,
// while the main goroutine loops on Recv() to process incoming agent messages.
func (s *Server) Channel(stream grpc.BidiStreamingServer[proto.AgentMessage, proto.ServerMessage]) error {
	hostID, err := extractHostID(stream.Context())
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "no valid client certificate: %v", err)
	}

	// Check CRL -- agent's cert might have been revoked since it was issued.
	if revoked, err := s.isCertRevoked(stream.Context()); err != nil {
		s.log.Error("cert revocation check failed", "error", err)
		return status.Error(codes.Internal, "revocation check unavailable")
	} else if revoked {
		return status.Error(codes.PermissionDenied, "certificate has been revoked")
	}

	// Check that this host is actually registered.
	if _, ok := s.registry.Get(hostID); !ok {
		return status.Errorf(codes.NotFound, "host %s not registered", hostID)
	}

	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	as := &agentStream{
		hostID: hostID,
		send:   make(chan *proto.ServerMessage, sendBufferSize),
		cancel: cancel,
	}

	// Register the stream. If an old stream exists for this host (stale
	// connection), cancel it first to avoid duplicate streams.
	s.mu.Lock()
	if old, ok := s.streams[hostID]; ok {
		old.cancel()
		s.log.Warn("replaced stale stream", "hostID", hostID)
	}
	s.streams[hostID] = as
	s.mu.Unlock()

	s.registry.SetConnected(hostID, true)

	s.log.Info("agent connected", "hostID", hostID)
	s.bus.Publish(events.SSEEvent{
		Type:      events.EventClusterHost,
		HostName:  hostID,
		Message:   fmt.Sprintf("agent %s connected", hostID),
		Timestamp: time.Now(),
	})

	// Cleanup on disconnect -- runs when the recv loop exits.
	defer func() {
		s.mu.Lock()
		// Only remove if it's still our stream (not replaced by a newer one).
		if cur, ok := s.streams[hostID]; ok && cur == as {
			delete(s.streams, hostID)
		}
		s.mu.Unlock()

		s.pendingMu.Lock()
		if ch, ok := s.pending[hostID]; ok {
			delete(s.pending, hostID)
			close(ch)
		}
		s.pendingMu.Unlock()

		s.registry.SetConnected(hostID, false)
		_ = s.registry.UpdateLastSeen(hostID, time.Now())

		s.log.Info("agent disconnected", "hostID", hostID)
		s.bus.Publish(events.SSEEvent{
			Type:      events.EventClusterHost,
			HostName:  hostID,
			Message:   fmt.Sprintf("agent %s disconnected", hostID),
			Timestamp: time.Now(),
		})
	}()

	// Send goroutine: drains the send channel and writes to the stream.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-as.send:
				if !ok {
					return
				}
				if err := stream.Send(msg); err != nil {
					s.log.Warn("send to agent failed", "hostID", hostID, "error", err)
					cancel()
					return
				}
			}
		}
	}()

	// Receive loop: reads messages from the agent and dispatches them.
	for {
		msg, err := stream.Recv()
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil // clean disconnect
			}
			return err
		}
		s.handleAgentMessage(hostID, as, msg)
	}
}

// ReportState handles a full state snapshot from an agent.
// Used on initial connect and as a periodic reconciliation checkpoint.
func (s *Server) ReportState(ctx context.Context, report *proto.StateReport) (*proto.StateAck, error) {
	hostID, err := extractHostID(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "no valid client certificate: %v", err)
	}

	if revoked, err := s.isCertRevoked(ctx); err != nil {
		s.log.Error("cert revocation check failed", "error", err)
		return nil, status.Error(codes.Internal, "revocation check unavailable")
	} else if revoked {
		return nil, status.Error(codes.PermissionDenied, "certificate has been revoked")
	}

	containers := protoToContainers(report.Containers)
	s.registry.UpdateContainers(hostID, containers)
	_ = s.registry.UpdateLastSeen(hostID, time.Now())

	s.log.Info("state report received",
		"hostID", hostID,
		"containers", len(containers),
		"version", report.AgentVersion,
	)

	return &proto.StateAck{
		Accepted: true,
		Message:  "state received",
	}, nil
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

// DrainHost sets a host to "draining" state -- no new updates will be dispatched.
func (s *Server) DrainHost(id string) error {
	return s.registry.SetState(id, cluster.HostDraining)
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

// ---------------------------------------------------------------------------
// Agent message dispatch
// ---------------------------------------------------------------------------

// handleAgentMessage routes an incoming agent message to the appropriate handler.
func (s *Server) handleAgentMessage(hostID string, as *agentStream, msg *proto.AgentMessage) {
	switch p := msg.Payload.(type) {
	case *proto.AgentMessage_Heartbeat:
		s.handleHeartbeat(hostID, as, p.Heartbeat)

	case *proto.AgentMessage_ContainerList:
		s.handleContainerList(hostID, msg, p.ContainerList)

	case *proto.AgentMessage_UpdateResult:
		s.handleUpdateResult(hostID, msg, p.UpdateResult)

	case *proto.AgentMessage_ContainerActionResult:
		s.handleContainerActionResult(hostID, msg, p.ContainerActionResult)

	case *proto.AgentMessage_HookResult:
		s.handleHookResult(hostID, p.HookResult)

	case *proto.AgentMessage_RollbackResult:
		s.handleRollbackResult(hostID, p.RollbackResult)

	case *proto.AgentMessage_OfflineJournal:
		s.handleOfflineJournal(hostID, p.OfflineJournal)

	case *proto.AgentMessage_CertRenewal:
		s.handleCertRenewal(hostID, as, p.CertRenewal)

	default:
		s.log.Warn("unknown agent message type", "hostID", hostID)
	}
}

func (s *Server) handleHeartbeat(hostID string, as *agentStream, hb *proto.Heartbeat) {
	// Cache the agent's version and features for the lifetime of this stream.
	as.version = hb.AgentVersion
	as.features = hb.SupportedFeatures

	_ = s.registry.UpdateLastSeen(hostID, time.Now())
}

func (s *Server) handleContainerList(hostID string, msg *proto.AgentMessage, cl *proto.ContainerList) {
	containers := protoToContainers(cl.Containers)
	s.registry.UpdateContainers(hostID, containers)

	s.log.Debug("container list received",
		"hostID", hostID,
		"count", len(containers),
		"requestID", cl.RequestId,
	)

	// Route to a pending synchronous caller if one is waiting.
	if cl.RequestId != "" {
		s.deliverPending(hostID, msg)
	}
}

func (s *Server) handleUpdateResult(hostID string, msg *proto.AgentMessage, ur *proto.UpdateResult) {
	s.log.Info("update result",
		"hostID", hostID,
		"container", ur.ContainerName,
		"outcome", ur.Outcome,
		"requestID", ur.RequestId,
	)

	s.bus.Publish(events.SSEEvent{
		Type:          events.EventContainerUpdate,
		ContainerName: ur.ContainerName,
		HostName:      hostID,
		Message:       fmt.Sprintf("update %s: %s", ur.ContainerName, ur.Outcome),
		Timestamp:     time.Now(),
	})

	// Route to a pending synchronous caller if one is waiting.
	if ur.RequestId != "" {
		s.deliverPending(hostID, msg)
	}
}

func (s *Server) handleContainerActionResult(hostID string, msg *proto.AgentMessage, ar *proto.ContainerActionResult) {
	s.log.Info("container action result",
		"hostID", hostID,
		"container", ar.ContainerName,
		"action", ar.Action,
		"outcome", ar.Outcome,
		"requestID", ar.RequestId,
	)

	s.bus.Publish(events.SSEEvent{
		Type:          events.EventContainerState,
		ContainerName: ar.ContainerName,
		HostName:      hostID,
		Message:       fmt.Sprintf("%s %s: %s", ar.Action, ar.ContainerName, ar.Outcome),
		Timestamp:     time.Now(),
	})

	// Route to a pending synchronous caller if one is waiting.
	if ar.RequestId != "" {
		s.deliverPending(hostID, msg)
	}
}

func (s *Server) handleHookResult(hostID string, hr *proto.HookResult) {
	lvl := slog.LevelInfo
	if hr.ExitCode != 0 || hr.Error != "" {
		lvl = slog.LevelWarn
	}
	s.log.Log(context.Background(), lvl, "hook result",
		"hostID", hostID,
		"container", hr.ContainerName,
		"phase", hr.Phase,
		"exitCode", hr.ExitCode,
		"requestID", hr.RequestId,
	)
}

func (s *Server) handleRollbackResult(hostID string, rr *proto.RollbackResult) {
	s.log.Info("rollback result",
		"hostID", hostID,
		"container", rr.ContainerName,
		"outcome", rr.Outcome,
		"requestID", rr.RequestId,
	)

	s.bus.Publish(events.SSEEvent{
		Type:          events.EventContainerUpdate,
		ContainerName: rr.ContainerName,
		HostName:      hostID,
		Message:       fmt.Sprintf("rollback %s: %s", rr.ContainerName, rr.Outcome),
		Timestamp:     time.Now(),
	})
}

// handleOfflineJournal processes journal entries from an agent that was offline.
// Phase 6 will add full journal replay; for now we just log the entries.
func (s *Server) handleOfflineJournal(hostID string, oj *proto.OfflineJournal) {
	s.log.Info("offline journal received",
		"hostID", hostID,
		"entries", len(oj.Entries),
	)

	// TODO(phase6): replay journal entries into the server's history and
	// reconcile container state. For now, log each entry as an event.
	for _, e := range oj.Entries {
		s.bus.Publish(events.SSEEvent{
			Type:          events.EventContainerUpdate,
			ContainerName: e.Container,
			HostName:      hostID,
			Message:       fmt.Sprintf("journal: %s %s (%s)", e.Action, e.Container, e.Outcome),
			Timestamp:     time.Now(),
		})
	}
}

// handleCertRenewal processes a certificate renewal CSR from an agent whose
// cert is approaching expiry. Signs the new CSR and sends back the fresh cert.
func (s *Server) handleCertRenewal(hostID string, as *agentStream, csr *proto.CertRenewalCSR) {
	certPEM, serial, err := s.ca.SignCSR(csr.Csr, hostID)
	if err != nil {
		s.log.Error("cert renewal failed", "hostID", hostID, "error", err)
		return
	}

	if err := s.registry.UpdateCertSerial(hostID, serial); err != nil {
		s.log.Error("cert renewal: failed to update serial", "hostID", hostID, "error", err)
		return
	}

	// Non-blocking send — matches SendCommand pattern.
	msg := &proto.ServerMessage{
		Payload: &proto.ServerMessage_CertRenewalResponse{
			CertRenewalResponse: &proto.CertRenewalResponse{
				AgentCert: certPEM,
			},
		},
	}
	select {
	case as.send <- msg:
	default:
		s.log.Error("cert renewal: send buffer full", "hostID", hostID)
		return
	}

	s.log.Info("cert renewed", "hostID", hostID, "newSerial", serial)
}

// ---------------------------------------------------------------------------
// Synchronous request/response helpers
// ---------------------------------------------------------------------------

// deliverPending sends an agent response to a waiting synchronous caller.
// Non-blocking: if nobody is waiting (or the channel is already full), the
// message is silently dropped (the fire-and-forget handlers already processed
// the response above).
func (s *Server) deliverPending(hostID string, msg *proto.AgentMessage) {
	s.pendingMu.Lock()
	ch, ok := s.pending[hostID]
	if ok {
		delete(s.pending, hostID)
	}
	s.pendingMu.Unlock()

	if ok {
		// Non-blocking send. Channel is buffered (size 1), so this only
		// drops if something went very wrong (double delivery).
		select {
		case ch <- msg:
		default:
		}
	}
}

// registerPending creates a response channel for the given host.
// Must be called before sending the command to avoid a race where
// the agent responds before the channel is registered.
func (s *Server) registerPending(hostID string) (chan *proto.AgentMessage, error) {
	ch := make(chan *proto.AgentMessage, 1)
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if _, exists := s.pending[hostID]; exists {
		return nil, fmt.Errorf("agent %s already has an outstanding request", hostID)
	}
	s.pending[hostID] = ch
	return ch, nil
}

// cancelPending removes the pending channel for a host and returns it.
// Used for cleanup when SendCommand fails after registration.
func (s *Server) cancelPending(hostID string, ch chan *proto.AgentMessage) {
	s.pendingMu.Lock()
	if cur, ok := s.pending[hostID]; ok && cur == ch {
		delete(s.pending, hostID)
	}
	s.pendingMu.Unlock()
}

// awaitPending blocks on an already-registered response channel until a
// response arrives or the context is cancelled.
func (s *Server) awaitPending(ctx context.Context, hostID string, ch chan *proto.AgentMessage) (*proto.AgentMessage, error) {
	select {
	case resp, ok := <-ch:
		if !ok {
			// Channel was closed by the disconnect cleanup — agent dropped.
			return nil, fmt.Errorf("agent %s disconnected", hostID)
		}
		return resp, nil
	case <-ctx.Done():
		s.cancelPending(hostID, ch)
		return nil, ctx.Err()
	}
}

// ListContainersSync sends a ListContainersRequest to the agent and blocks
// until the agent responds with a ContainerList or the context is cancelled.
// The request_id on the ServerMessage is used for correlation.
func (s *Server) ListContainersSync(ctx context.Context, hostID string) ([]cluster.ContainerInfo, error) {
	reqID := generateRequestID()

	// Register the response channel BEFORE sending, so a fast agent
	// response doesn't race with registration.
	ch, err := s.registerPending(hostID)
	if err != nil {
		return nil, err
	}

	msg := &proto.ServerMessage{
		RequestId: reqID,
		Payload: &proto.ServerMessage_ListContainers{
			ListContainers: &proto.ListContainersRequest{},
		},
	}

	if err := s.SendCommand(hostID, msg); err != nil {
		s.cancelPending(hostID, ch)
		return nil, fmt.Errorf("send list containers: %w", err)
	}

	resp, err := s.awaitPending(ctx, hostID, ch)
	if err != nil {
		return nil, fmt.Errorf("await container list: %w", err)
	}

	cl, ok := resp.Payload.(*proto.AgentMessage_ContainerList)
	if !ok {
		return nil, fmt.Errorf("unexpected response type %T (wanted ContainerList)", resp.Payload)
	}

	return protoToContainers(cl.ContainerList.Containers), nil
}

// UpdateContainerSync sends an UpdateContainerRequest to the agent and blocks
// until the agent responds with an UpdateResult or the context is cancelled.
func (s *Server) UpdateContainerSync(ctx context.Context, hostID, containerName, targetImage, targetDigest string) (*proto.UpdateResult, error) {
	reqID := generateRequestID()

	// Register the response channel BEFORE sending, so a fast agent
	// response doesn't race with registration.
	ch, err := s.registerPending(hostID)
	if err != nil {
		return nil, err
	}

	msg := &proto.ServerMessage{
		RequestId: reqID,
		Payload: &proto.ServerMessage_UpdateContainer{
			UpdateContainer: &proto.UpdateContainerRequest{
				ContainerName: containerName,
				TargetImage:   targetImage,
				TargetDigest:  targetDigest,
			},
		},
	}

	if err := s.SendCommand(hostID, msg); err != nil {
		s.cancelPending(hostID, ch)
		return nil, fmt.Errorf("send update container: %w", err)
	}

	resp, err := s.awaitPending(ctx, hostID, ch)
	if err != nil {
		return nil, fmt.Errorf("await update result: %w", err)
	}

	ur, ok := resp.Payload.(*proto.AgentMessage_UpdateResult)
	if !ok {
		return nil, fmt.Errorf("unexpected response type %T (wanted UpdateResult)", resp.Payload)
	}

	return ur.UpdateResult, nil
}

// ContainerActionSync sends a ContainerActionRequest to the agent and blocks
// until the agent responds with a ContainerActionResult or the context is cancelled.
func (s *Server) ContainerActionSync(ctx context.Context, hostID, containerName, action string) error {
	reqID := generateRequestID()
	ch, err := s.registerPending(hostID)
	if err != nil {
		return err
	}

	msg := &proto.ServerMessage{
		RequestId: reqID,
		Payload: &proto.ServerMessage_ContainerAction{
			ContainerAction: &proto.ContainerActionRequest{
				ContainerName: containerName,
				Action:        action,
			},
		},
	}

	if err := s.SendCommand(hostID, msg); err != nil {
		s.cancelPending(hostID, ch)
		return fmt.Errorf("send container action: %w", err)
	}

	resp, err := s.awaitPending(ctx, hostID, ch)
	if err != nil {
		return fmt.Errorf("await container action result: %w", err)
	}

	ar, ok := resp.Payload.(*proto.AgentMessage_ContainerActionResult)
	if !ok {
		return fmt.Errorf("unexpected response type %T (wanted ContainerActionResult)", resp.Payload)
	}
	if ar.ContainerActionResult.Outcome != "success" {
		return fmt.Errorf("%s failed: %s", action, ar.ContainerActionResult.Error)
	}
	return nil
}

// generateRequestID creates a random 8-byte hex string for request correlation.
func generateRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// TLS verification helpers
// ---------------------------------------------------------------------------

// verifyCRL is the custom VerifyPeerCertificate callback for TLS.
// It runs after standard chain validation and checks the leaf cert's serial
// against the revocation list in BoltDB.
//
// When rawCerts is empty (enrollment without client cert), this is a no-op.
func (s *Server) verifyCRL(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return nil // no client cert presented -- enrollment path
	}

	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("parse client cert: %w", err)
	}

	serial := fmt.Sprintf("%x", leaf.SerialNumber)
	revoked, err := s.store.IsRevokedCert(serial)
	if err != nil {
		s.log.Error("CRL check failed, rejecting connection", "serial", serial, "error", err)
		return fmt.Errorf("CRL check unavailable")
	}
	if revoked {
		return fmt.Errorf("certificate %s has been revoked", serial)
	}

	return nil
}

// isCertRevoked checks whether the peer's client cert is revoked.
// Used by AgentService methods as a second check (belt and braces with the
// TLS-level verifyCRL).
func (s *Server) isCertRevoked(ctx context.Context) (bool, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return false, nil
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return false, nil
	}

	serial := fmt.Sprintf("%x", tlsInfo.State.PeerCertificates[0].SerialNumber)
	return s.store.IsRevokedCert(serial)
}

// extractHostID extracts the host ID from the peer's TLS client certificate CN.
// Returns an error if no client cert is present (unauthenticated connection).
func extractHostID(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("no peer in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", fmt.Errorf("no TLS info in peer")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return "", fmt.Errorf("no client certificate presented")
	}

	cn := tlsInfo.State.PeerCertificates[0].Subject.CommonName
	if cn == "" {
		return "", fmt.Errorf("client certificate CN is empty")
	}
	return cn, nil
}

// ---------------------------------------------------------------------------
// Token helpers
// ---------------------------------------------------------------------------

// hmacToken computes HMAC-SHA256 of the plaintext token using a key derived
// from the CA certificate. This avoids needing a separate HMAC secret -- the
// CA cert is stable and unique per Sentinel instance.
func (s *Server) hmacToken(token string) []byte {
	// Use the raw CA cert bytes as the HMAC key. The CA cert is unique to this
	// Sentinel instance and never leaves the server, making it a good choice
	// for key material without requiring a separate secret.
	key := s.ca.CACertPEM()
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(token))
	return mac.Sum(nil)
}

// loadEnrollToken deserialises an enrollment token from BoltDB.
func (s *Server) loadEnrollToken(id string) (*cluster.EnrollToken, error) {
	data, err := s.store.GetEnrollToken(id)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("token %s not found", id)
	}

	var tok cluster.EnrollToken
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("unmarshal token: %w", err)
	}
	return &tok, nil
}

// saveEnrollToken serialises and persists an enrollment token.
func (s *Server) saveEnrollToken(tok *cluster.EnrollToken) error {
	data, err := json.Marshal(tok)
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}
	return s.store.SaveEnrollToken(tok.ID, data)
}

// ---------------------------------------------------------------------------
// Proto conversion helpers
// ---------------------------------------------------------------------------

// protoToContainers converts proto ContainerInfo messages to cluster.ContainerInfo.
func protoToContainers(pcs []*proto.ContainerInfo) []cluster.ContainerInfo {
	out := make([]cluster.ContainerInfo, len(pcs))
	for i, pc := range pcs {
		out[i] = cluster.ContainerInfo{
			ID:          pc.Id,
			Name:        pc.Name,
			Image:       pc.Image,
			ImageDigest: pc.ImageDigest,
			State:       pc.State,
			Labels:      pc.Labels,
		}
		if pc.Created != nil {
			out[i].Created = pc.Created.AsTime()
		}
	}
	return out
}

// generateHostID creates a random 16-byte hex string for use as a host ID.
// Using raw crypto/rand rather than UUID to avoid an extra dependency.
func generateHostID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
