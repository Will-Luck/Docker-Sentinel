package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

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

	// Capture the agent's IP from the gRPC peer address so the dashboard
	// can link port chips to the correct host.
	if p, ok := peer.FromContext(stream.Context()); ok {
		if tcpAddr, ok := p.Addr.(*net.TCPAddr); ok {
			s.registry.UpdateAddress(hostID, tcpAddr.IP.String())
		}
	}

	var streamErr error

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
		prefix := hostID + ":"
		for key, ch := range s.pending {
			if strings.HasPrefix(key, prefix) {
				delete(s.pending, key)
				close(ch)
			}
		}
		s.pendingMu.Unlock()

		s.registry.SetDisconnected(hostID, streamErr)
		// Force-persist LastSeen on disconnect so it survives server restart.
		if err := s.registry.PersistLastSeen(hostID); err != nil {
			s.log.Warn("failed to persist last seen on disconnect", "hostID", hostID, "error", err)
		}

		cat, _ := classifyDisconnect(streamErr)
		s.log.Info("agent disconnected", "hostID", hostID, "category", cat, "error", streamErr)
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
			streamErr = err
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return err
		}
		// If our context was cancelled (stream replaced by a newer connection),
		// exit so the agent detects the broken stream and reconnects cleanly.
		if ctx.Err() != nil {
			return nil
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
	if err := s.registry.UpdateLastSeen(hostID, time.Now()); err != nil {
		s.log.Warn("failed to update last seen on state report", "hostID", hostID, "error", err)
	}

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

	case *proto.AgentMessage_FetchLogsResult:
		s.handleFetchLogsResult(hostID, msg, p.FetchLogsResult)

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
	as.mu.Lock()
	as.version = hb.AgentVersion
	as.features = hb.SupportedFeatures
	as.mu.Unlock()

	if err := s.registry.UpdateLastSeen(hostID, time.Now()); err != nil {
		s.log.Warn("failed to update last seen on heartbeat", "hostID", hostID, "error", err)
	}
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
		s.deliverPending(hostID, cl.RequestId, msg)
	}
}

func (s *Server) handleUpdateResult(hostID string, msg *proto.AgentMessage, ur *proto.UpdateResult) {
	s.log.Info("update result",
		"hostID", hostID,
		"container", ur.ContainerName,
		"outcome", ur.Outcome,
		"error", ur.Error,
		"requestID", ur.RequestId,
	)

	sseMsg := fmt.Sprintf("update %s: %s", ur.ContainerName, ur.Outcome)
	if ur.Outcome != "success" && ur.Error != "" {
		sseMsg = fmt.Sprintf("update %s failed: %s", ur.ContainerName, ur.Error)
	}
	s.bus.Publish(events.SSEEvent{
		Type:          events.EventContainerUpdate,
		ContainerName: ur.ContainerName,
		HostID:        hostID,
		Message:       sseMsg,
		Timestamp:     time.Now(),
	})

	// Route to a pending synchronous caller if one is waiting.
	if ur.RequestId != "" {
		s.deliverPending(hostID, ur.RequestId, msg)
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

	// Update the cached container state so subsequent row fetches reflect
	// the action immediately, without waiting for a full container list refresh.
	if ar.Outcome == "success" {
		var newState string
		switch ar.Action {
		case "stop":
			newState = "exited"
		case "start", "restart":
			newState = "running"
		}
		if newState != "" {
			s.registry.UpdateContainerState(hostID, ar.ContainerName, newState)
		}
	}

	s.bus.Publish(events.SSEEvent{
		Type:          events.EventContainerState,
		ContainerName: ar.ContainerName,
		HostID:        hostID,
		Message:       fmt.Sprintf("%s %s: %s", ar.Action, ar.ContainerName, ar.Outcome),
		Timestamp:     time.Now(),
	})

	// Route to a pending synchronous caller if one is waiting.
	if ar.RequestId != "" {
		s.deliverPending(hostID, ar.RequestId, msg)
	}
}

func (s *Server) handleFetchLogsResult(hostID string, msg *proto.AgentMessage, lr *proto.FetchLogsResult) {
	s.log.Debug("fetch logs result", "hostID", hostID, "container", lr.ContainerName, "requestID", lr.RequestId)
	if lr.RequestId != "" {
		s.deliverPending(hostID, lr.RequestId, msg)
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

// handleOfflineJournal replays journal entries from an agent that was offline.
// Each entry is persisted to history (if a HistoryRecorder is wired in) and
// published as an SSE event so the dashboard updates in real time.
func (s *Server) handleOfflineJournal(hostID string, oj *proto.OfflineJournal) {
	s.log.Info("offline journal received",
		"hostID", hostID,
		"entries", len(oj.Entries),
	)

	// Look up the host name for enriched history records and SSE events.
	var hostName string
	if hs, ok := s.registry.Get(hostID); ok {
		hostName = hs.Info.Name
	}

	for _, e := range oj.Entries {
		ts := time.Now()
		if e.GetTimestamp() != nil {
			ts = e.GetTimestamp().AsTime()
		}

		var dur time.Duration
		if e.GetDuration() != nil {
			dur = e.GetDuration().AsDuration()
		}

		rec := store.UpdateRecord{
			Timestamp:     ts,
			ContainerName: e.Container,
			OldImage:      e.OldImage,
			NewImage:      e.NewImage,
			OldDigest:     e.OldDigest,
			NewDigest:     e.NewDigest,
			Outcome:       e.Outcome,
			Error:         e.Error,
			Duration:      dur,
			HostID:        hostID,
			HostName:      hostName,
		}

		if s.history != nil {
			if err := s.history.RecordUpdate(rec); err != nil {
				s.log.Warn("failed to persist journal entry",
					"hostID", hostID,
					"container", e.Container,
					"error", err,
				)
			}
		}

		msg := fmt.Sprintf("journal: %s %s → %s (%s)", e.Container, e.OldImage, e.NewImage, e.Outcome)
		if e.Outcome != "success" && e.Error != "" {
			msg = fmt.Sprintf("journal: %s failed: %s", e.Container, e.Error)
		}

		s.bus.Publish(events.SSEEvent{
			Type:          events.EventContainerUpdate,
			ContainerName: e.Container,
			HostID:        hostID,
			HostName:      hostName,
			Message:       msg,
			Timestamp:     ts,
		})
	}

	s.log.Info("replayed journal entries",
		"hostID", hostID,
		"count", len(oj.Entries),
	)
}

// handleCertRenewal processes a certificate renewal CSR from an agent whose
// cert is approaching expiry. Signs the new CSR and sends back the fresh cert.
//
// If the serial number update fails after signing, the newly issued cert is
// revoked immediately to prevent an untracked certificate from remaining valid.
func (s *Server) handleCertRenewal(hostID string, as *agentStream, csr *proto.CertRenewalCSR) {
	certPEM, serial, err := s.ca.SignCSR(csr.Csr, hostID)
	if err != nil {
		s.log.Error("cert renewal failed", "hostID", hostID, "error", err)
		return
	}

	if err := s.registry.UpdateCertSerial(hostID, serial); err != nil {
		s.log.Error("cert renewal: failed to update serial, revoking new cert", "hostID", hostID, "serial", serial, "error", err)
		// Revoke the newly signed cert so it can't be used without being tracked.
		if rErr := s.store.AddRevokedCert(serial); rErr != nil {
			s.log.Error("cert renewal: failed to revoke orphaned cert", "hostID", hostID, "serial", serial, "error", rErr)
		}
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
