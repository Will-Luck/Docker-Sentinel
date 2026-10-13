package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
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

	// The token is "<secret>[.<CA fingerprint>]": the fingerprint is for
	// the agent. Token ID is the first 8 hex characters (4 bytes) of the
	// secret. This lets us look up the token record without storing the
	// full value.
	secret, _, _ := strings.Cut(req.Token, ".")
	if len(secret) < 8 {
		return nil, status.Error(codes.InvalidArgument, "token too short")
	}
	tokenID := secret[:8]
	keyHash, err := csrKeyHash(req.Csr)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid CSR")
	}
	hostID, retry, err := s.consumeEnrollToken(ctx, tokenID, secret, keyHash, req)
	if err != nil {
		return nil, err
	}

	// Sign the CSR. The CN is set to hostID by the CA (overrides whatever
	// the agent put in the CSR subject).
	certPEM, serial, err := s.ca.SignCSR(req.Csr, hostID)
	if err != nil {
		s.log.Error("failed to sign CSR", "hostID", hostID, "error", err)
		return nil, status.Error(codes.Internal, "failed to sign certificate")
	}

	// A retry of an enrolment whose answer was lost: the host is already
	// registered, and takes the newly signed certificate.
	if retry {
		if err := s.registry.ReissueCert(hostID, serial); err != nil {
			s.log.Error("failed to re-sign a retried enrolment", "hostID", hostID, "error", err)
			return nil, status.Error(codes.PermissionDenied, "the host this token enrolled has been removed; generate a new token")
		}
		return &proto.EnrollResponse{HostId: hostID, CaCert: s.ca.CACertPEM(), AgentCert: certPEM}, nil
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

// enrollRetryWindow is how long after a token's use the same key may enrol
// with it again: the first answer may never have reached the agent.
const enrollRetryWindow = 10 * time.Minute

// csrKeyHash is the SHA-256 (hex) of a CSR's public key.
func csrKeyHash(csrDER []byte) (string, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

// consumeEnrollToken checks the one-time enrolment token and marks it used,
// in one step: concurrent enrolments with the same token cannot both pass
// the check before either marks it. It returns the host ID to issue: a new
// one, or, for a retry with the same key (keyHash) within enrollRetryWindow
// of the token's use, the one issued then (retry is set). A refused token
// is logged with the host that presented it.
func (s *Server) consumeEnrollToken(ctx context.Context, tokenID, secret, keyHash string, req *proto.EnrollRequest) (hostID string, retry bool, err error) {
	s.enrollMu.Lock()
	defer s.enrollMu.Unlock()

	from := ""
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		from = p.Addr.String()
	}
	refuse := func(reason, msg string) error {
		s.log.Warn("enrolment refused: "+reason, "tokenID", tokenID, "host_name", req.HostName, "from", from)
		return status.Error(codes.PermissionDenied, msg)
	}

	tok, err := s.loadEnrollToken(tokenID)
	if err != nil {
		s.log.Warn("enrollment failed: token lookup", "tokenID", tokenID, "host_name", req.HostName, "from", from, "error", err)
		return "", false, status.Error(codes.PermissionDenied, "invalid enrollment token")
	}
	// Verify HMAC: hash the provided plaintext and compare with stored hash.
	if !hmac.Equal(s.hmacToken(secret), tok.Hash) {
		return "", false, refuse("the token does not match", "invalid enrollment token")
	}
	now := time.Now()
	if tok.Used {
		if tok.HostID == "" || tok.KeyHash != keyHash {
			return "", false, refuse("the token was already used; generate a new one for each agent", "token already used")
		}
		if now.Sub(tok.UsedAt) > enrollRetryWindow {
			return "", false, refuse("the token was used more than 10 minutes ago", "token already used")
		}
		s.log.Info("enrolment retried with the same key and token, signing again for the same host",
			"tokenID", tokenID, "hostID", tok.HostID, "host_name", req.HostName, "from", from)
		return tok.HostID, true, nil
	}
	if now.After(tok.ExpiresAt) {
		return "", false, refuse("the token has expired", "token expired")
	}

	hostID, err = generateHostID()
	if err != nil {
		return "", false, status.Error(codes.Internal, "failed to generate host ID")
	}
	// Mark token as used before issuing certs (prevent replay on error).
	tok.Used, tok.UsedAt, tok.KeyHash, tok.HostID = true, now, keyHash, hostID
	if err := s.saveEnrollToken(tok); err != nil {
		s.log.Error("failed to mark token used", "tokenID", tokenID, "error", err)
		return "", false, status.Error(codes.Internal, "failed to consume token")
	}
	return hostID, false, nil
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

	secret := hex.EncodeToString(raw)
	id = secret[:8]
	// The token names its hub: "<secret>.<CA fingerprint>". The agent
	// checks the server it enrols with against the fingerprint before it
	// sends the token; the hub checks the secret.
	token = secret + "." + s.ca.Fingerprint()

	now := time.Now()
	tok := &cluster.EnrollToken{
		ID:        id,
		Hash:      s.hmacToken(secret),
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

	// Only a certificate the hub issued to this host gets in. An agent
	// that connects with its renewed one has it on disk and working: it
	// becomes current and the one it replaces is revoked.
	if err := s.authoriseCert(stream.Context(), hostID); err != nil {
		return err
	}

	// Capture the agent's IP from the gRPC peer address so the dashboard
	// can link port chips to the correct host.
	if p, ok := peer.FromContext(stream.Context()); ok {
		if tcpAddr, ok := p.Addr.(*net.TCPAddr); ok {
			s.registry.UpdateAddress(hostID, tcpAddr.IP.String())
		}
	}

	var streamErr error

	ctx, cancel := context.WithCancelCause(stream.Context())
	defer cancel(nil)

	as := &agentStream{
		hostID:   hostID,
		send:     make(chan *proto.ServerMessage, sendBufferSize),
		cancel:   cancel,
		done:     ctx.Done(),
		peerCert: peerLeaf(stream.Context()),
	}

	// Register the stream. If an old stream exists for this host (stale
	// connection), cancel it first to avoid duplicate streams.
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return status.Error(codes.Unavailable, "cluster server is stopping")
	}
	if old, ok := s.streams[hostID]; ok {
		old.cancel(errStreamReplaced)
		s.log.Warn("replaced stale stream", "hostID", hostID)
	}
	s.streams[hostID] = as
	// Under s.mu, like the disconnect in the cleanup below, so the two
	// cannot be applied out of order for one host.
	s.registry.SetConnected(hostID, true)
	s.mu.Unlock()

	s.log.Info("agent connected", "hostID", hostID)
	s.bus.Publish(events.SSEEvent{
		Type:      events.EventClusterHost,
		HostName:  hostID,
		Message:   fmt.Sprintf("agent %s connected", s.hostLabel(hostID)),
		Timestamp: time.Now(),
	})

	// Cleanup on disconnect -- runs when the recv loop exits.
	defer func() {
		s.mu.Lock()
		cur, ok := s.streams[hostID]
		replaced := ok && cur != as
		if ok && cur == as {
			delete(s.streams, hostID)
		}
		// Mark the host disconnected while still holding s.mu, so a new
		// stream for it cannot register between the check and the update.
		if !replaced {
			s.registry.SetDisconnected(hostID, streamErr)
		}
		s.mu.Unlock()

		// Requests that went out on this stream can never be answered.
		s.failPending(as)

		// A newer stream for the same agent has taken over: the host is
		// still connected, so leave its state and requests alone.
		if replaced {
			s.log.Info("stale agent stream closed", "hostID", hostID, "error", streamErr)
			return
		}

		// Force-persist LastSeen on disconnect so it survives server restart.
		if err := s.registry.PersistLastSeen(hostID); err != nil {
			s.log.Warn("failed to persist last seen on disconnect", "hostID", hostID, "error", err)
		}

		cat, _ := classifyDisconnect(streamErr)
		s.log.Info("agent disconnected", "hostID", hostID, "category", cat, "error", streamErr)
		s.bus.Publish(events.SSEEvent{
			Type:      events.EventClusterHost,
			HostName:  hostID,
			Message:   fmt.Sprintf("agent %s disconnected", s.hostLabel(hostID)),
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
					cancel(fmt.Errorf("send to agent: %w", err))
					return
				}
			}
		}
	}()

	// The settings the agent follows go first.
	s.syncSettings(as)

	// Receive loop: reads messages from the agent and dispatches them. It
	// runs in its own goroutine because Recv returns only when the agent
	// sends something or hangs up, and cancelling ctx (stream replaced,
	// host removed, server stopping) has to end this handler regardless.
	// Returning from the handler is what closes the stream on the agent.
	recvDone := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				recvDone <- err
				return
			}
			if ctx.Err() != nil {
				recvDone <- nil
				return
			}
			s.handleAgentMessage(hostID, as, msg)
		}
	}()

	select {
	case streamErr = <-recvDone:
	case <-ctx.Done():
		streamErr = context.Cause(ctx)
		if stream.Context().Err() != nil {
			// The transport went away, so Recv is about to fail too. Its
			// error says why (EOF, reset) better than the bare cancel does.
			select {
			case streamErr = <-recvDone:
			case <-time.After(time.Second):
			}
		}
	}
	if streamErr == nil || streamErr == io.EOF || ctx.Err() != nil {
		return nil
	}
	return streamErr
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
	if _, ok := s.registry.Get(hostID); !ok {
		return nil, status.Errorf(codes.NotFound, "host %s not registered", hostID)
	}
	if err := s.authoriseCert(ctx, hostID); err != nil {
		return nil, err
	}

	containers := protoToContainers(report.Containers)
	s.registry.UpdateContainers(hostID, containers)
	if err := s.registry.UpdateAgentInfo(hostID, report.AgentVersion, report.HostName, report.SupportedFeatures); err != nil {
		s.log.Warn("failed to store the agent's version", "hostID", hostID, "error", err)
	}
	if err := s.registry.UpdateLastSeen(hostID, time.Now()); err != nil {
		s.log.Warn("failed to update last seen on state report", "hostID", hostID, "error", err)
	}

	// Store Engine ID so the cluster page can show it.
	if report.EngineId != "" {
		if err := s.registry.UpdateEngineID(hostID, report.EngineId); err != nil {
			s.log.Warn("failed to store engine ID", "hostID", hostID, "error", err)
		}
		// The same daemon enrolled twice shows its containers under two
		// hosts, and approvals for both race on one daemon. Say so; which
		// host to remove is the user's call.
		if others := s.registry.RefreshEngineSharing(hostID); len(others) > 0 {
			s.log.Warn("the same Docker engine is enrolled as more than one host; remove the extra host on the cluster page",
				"hostID", hostID, "engine_id", report.EngineId, "other_hosts", strings.Join(others, ","))
		}
	}

	s.log.Info("state report received",
		"hostID", hostID,
		"containers", len(containers),
		"version", report.AgentVersion,
		"engine_id", report.EngineId,
	)

	return &proto.StateAck{
		Accepted:       true,
		Message:        "state received",
		ServerFeatures: []string{cluster.FeatureCertRenewal, cluster.FeatureKeepalive, cluster.FeatureUpdateSkipped},
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
	if err := s.registry.UpdateAgentInfo(hostID, hb.AgentVersion, "", hb.SupportedFeatures); err != nil {
		s.log.Warn("failed to store the agent's version", "hostID", hostID, "error", err)
	}

	if err := s.registry.UpdateLastSeen(hostID, time.Now()); err != nil {
		s.log.Warn("failed to update last seen on heartbeat", "hostID", hostID, "error", err)
	}

	// Settings changed since they were last sent reach the agent now.
	s.syncSettings(as)
}

func (s *Server) handleContainerList(hostID string, msg *proto.AgentMessage, cl *proto.ContainerList) {
	if cl.GetError() != "" {
		// The agent could not list its containers. Keep the last known
		// list; the waiting caller gets the error.
		s.log.Warn("agent could not list its containers", "hostID", hostID, "error", cl.GetError(), "requestID", cl.RequestId)
		if cl.RequestId != "" {
			s.deliverPending(hostID, cl.RequestId, msg)
		}
		return
	}

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

	// Route to a pending synchronous caller if one is waiting. If the
	// caller has given up (it timed out and recorded a failure), this is
	// still what the agent did: keep it in the history.
	// A skipped update changed nothing and leaves no history, as locally.
	if ur.RequestId != "" && !s.deliverPending(hostID, ur.RequestId, msg) && ur.Outcome != "skipped" {
		s.recordLateUpdate(hostID, ur)
	}
}

// recordLateUpdate writes an update result nobody was waiting for to the
// history, under the key every remote history row uses.
func (s *Server) recordLateUpdate(hostID string, ur *proto.UpdateResult) {
	if s.history == nil {
		return
	}
	var hostName string
	if hs, ok := s.registry.Get(hostID); ok {
		hostName = hs.Info.Name
	}
	var dur time.Duration
	if ur.GetDuration() != nil {
		dur = ur.GetDuration().AsDuration()
	}
	rec := store.UpdateRecord{
		Timestamp:     time.Now(),
		ContainerName: store.ScopedKey(hostID, ur.ContainerName),
		OldImage:      ur.OldImage,
		OldDigest:     ur.OldDigest,
		NewImage:      ur.NewImage,
		NewDigest:     ur.NewDigest,
		Outcome:       ur.Outcome,
		Error:         ur.Error,
		Duration:      dur,
		HostID:        hostID,
		HostName:      hostName,
	}
	// A rollback that finished late is a rollback, not an update to undo;
	// the result replaces the failure the caller recorded.
	if lr := s.takeLate(hostID, ur.RequestId); lr.purpose != "" {
		if lr.purpose == "rollback" {
			rec.Type = "rollback"
		}
		if !lr.recordAt.IsZero() {
			rec.Timestamp = lr.recordAt
		}
	}
	if err := s.history.RecordUpdate(rec); err != nil {
		s.log.Warn("failed to record late update result", "hostID", hostID, "container", ur.ContainerName, "error", err)
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
		if e.Outcome == "skipped" {
			continue // nothing changed; no history row, as locally
		}
		ts := time.Now()
		if e.GetTimestamp() != nil {
			ts = e.GetTimestamp().AsTime()
		}

		var dur time.Duration
		if e.GetDuration() != nil {
			dur = e.GetDuration().AsDuration()
		}

		// An update the hub stopped waiting for (the stream dropped): its
		// result replaces the failure the caller recorded.
		var recType string
		if lr := s.takeLate(hostID, e.GetId()); lr.purpose != "" {
			if lr.purpose == "rollback" {
				recType = "rollback"
			}
			if !lr.recordAt.IsZero() {
				ts = lr.recordAt
			}
		}

		rec := store.UpdateRecord{
			Type:          recType,
			Timestamp:     ts,
			ContainerName: store.ScopedKey(hostID, e.Container),
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

// renewalSendTimeout bounds how long the hub waits for room in an agent's
// send buffer to deliver a renewed certificate.
var renewalSendTimeout = 10 * time.Second

// handleCertRenewal processes a certificate renewal CSR from an agent whose
// cert is approaching expiry. Signs the new CSR and sends back the fresh cert.
//
// The agent's current certificate stays valid: the new serial is recorded
// as pending and replaces the old one only when the agent connects with it
// (Channel). A new certificate that cannot be tracked or delivered is
// revoked at once, so no untracked certificate stays valid; the agent
// simply asks again later.
func (s *Server) handleCertRenewal(hostID string, as *agentStream, csr *proto.CertRenewalCSR) {
	if reason := s.refuseRenewal(hostID, as, csr.Csr); reason != "" {
		s.log.Warn("cert renewal refused", "hostID", hostID, "reason", reason)
		return
	}

	certPEM, serial, err := s.ca.SignCSR(csr.Csr, hostID)
	if err != nil {
		s.log.Error("cert renewal failed", "hostID", hostID, "error", err)
		return
	}

	if err := s.registry.SetPendingCertSerial(hostID, serial); err != nil {
		s.log.Error("cert renewal: failed to record the new serial, revoking new cert", "hostID", hostID, "serial", serial, "error", err)
		s.revokeRenewal(hostID, serial)
		return
	}

	msg := &proto.ServerMessage{
		Payload: &proto.ServerMessage_CertRenewalResponse{
			CertRenewalResponse: &proto.CertRenewalResponse{
				AgentCert: certPEM,
			},
		},
	}
	// Wait for room rather than drop the reply: the agent cannot use a
	// certificate it never receives.
	timer := time.NewTimer(renewalSendTimeout)
	defer timer.Stop()
	select {
	case as.send <- msg:
		s.log.Info("cert renewed, waiting for the agent to use it", "hostID", hostID, "newSerial", serial)
		return
	case <-as.done:
	case <-timer.C:
	}
	s.log.Error("cert renewal: could not deliver the new certificate, revoking it", "hostID", hostID, "serial", serial)
	s.revokeRenewal(hostID, serial)
}

// revokeRenewal revokes a renewed certificate that was never delivered and
// forgets it as the host's pending renewal.
func (s *Server) revokeRenewal(hostID, serial string) {
	if err := s.store.AddRevokedCert(serial); err != nil {
		s.log.Error("cert renewal: failed to revoke undelivered cert", "hostID", hostID, "serial", serial, "error", err)
	}
	if err := s.registry.ClearPendingCertSerial(hostID, serial); err != nil {
		s.log.Warn("cert renewal: failed to clear pending serial", "hostID", hostID, "serial", serial, "error", err)
	}
}
