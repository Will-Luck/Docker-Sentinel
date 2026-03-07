package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// ---------------------------------------------------------------------------
// Synchronous request/response helpers
// ---------------------------------------------------------------------------

// deliverPending sends an agent response to a waiting synchronous caller.
// Non-blocking: if nobody is waiting (or the channel is already full), the
// message is silently dropped (the fire-and-forget handlers already processed
// the response above).
func (s *Server) deliverPending(hostID, requestID string, msg *proto.AgentMessage) {
	key := hostID + ":" + requestID
	s.pendingMu.Lock()
	ch, ok := s.pending[key]
	if ok {
		delete(s.pending, key)
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

// registerPending creates a response channel for the given host+request pair.
// Must be called before sending the command to avoid a race where
// the agent responds before the channel is registered.
func (s *Server) registerPending(hostID, requestID string) (chan *proto.AgentMessage, error) {
	key := hostID + ":" + requestID
	ch := make(chan *proto.AgentMessage, 1)
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if _, exists := s.pending[key]; exists {
		return nil, fmt.Errorf("duplicate request %s for agent %s", requestID, hostID)
	}
	s.pending[key] = ch
	return ch, nil
}

// cancelPending removes the pending channel for a host+request pair.
// Used for cleanup when SendCommand fails after registration.
func (s *Server) cancelPending(hostID, requestID string, ch chan *proto.AgentMessage) {
	key := hostID + ":" + requestID
	s.pendingMu.Lock()
	if cur, ok := s.pending[key]; ok && cur == ch {
		delete(s.pending, key)
	}
	s.pendingMu.Unlock()
}

// awaitPending blocks on an already-registered response channel until a
// response arrives or the context is cancelled.
func (s *Server) awaitPending(ctx context.Context, hostID, requestID string, ch chan *proto.AgentMessage) (*proto.AgentMessage, error) {
	select {
	case resp, ok := <-ch:
		if !ok {
			// Channel was closed by the disconnect cleanup — agent dropped.
			return nil, fmt.Errorf("agent %s disconnected", hostID)
		}
		return resp, nil
	case <-ctx.Done():
		s.cancelPending(hostID, requestID, ch)
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
	ch, err := s.registerPending(hostID, reqID)
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
		s.cancelPending(hostID, reqID, ch)
		return nil, fmt.Errorf("send list containers: %w", err)
	}

	resp, err := s.awaitPending(ctx, hostID, reqID, ch)
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
	ch, err := s.registerPending(hostID, reqID)
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
		s.cancelPending(hostID, reqID, ch)
		return nil, fmt.Errorf("send update container: %w", err)
	}

	resp, err := s.awaitPending(ctx, hostID, reqID, ch)
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
	ch, err := s.registerPending(hostID, reqID)
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
		s.cancelPending(hostID, reqID, ch)
		return fmt.Errorf("send container action: %w", err)
	}

	resp, err := s.awaitPending(ctx, hostID, reqID, ch)
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

// FetchLogsSync sends a FetchLogsRequest to the agent and blocks until the
// agent responds with a FetchLogsResult or the context is cancelled.
func (s *Server) FetchLogsSync(ctx context.Context, hostID, containerName string, lines int) (string, error) {
	reqID := generateRequestID()
	ch, err := s.registerPending(hostID, reqID)
	if err != nil {
		return "", err
	}

	if lines <= 0 {
		lines = 50
	}
	if lines > 500 {
		lines = 500
	}

	msg := &proto.ServerMessage{
		RequestId: reqID,
		Payload: &proto.ServerMessage_FetchLogs{
			FetchLogs: &proto.FetchLogsRequest{
				ContainerName: containerName,
				Lines:         int32(lines), //nolint:gosec // clamped to 0-500 above
			},
		},
	}

	if err := s.SendCommand(hostID, msg); err != nil {
		s.cancelPending(hostID, reqID, ch)
		return "", fmt.Errorf("send fetch logs: %w", err)
	}

	resp, err := s.awaitPending(ctx, hostID, reqID, ch)
	if err != nil {
		return "", fmt.Errorf("await fetch logs: %w", err)
	}

	lr, ok := resp.Payload.(*proto.AgentMessage_FetchLogsResult)
	if !ok {
		return "", fmt.Errorf("unexpected response type %T (wanted FetchLogsResult)", resp.Payload)
	}

	if lr.FetchLogsResult.Error != "" {
		return "", fmt.Errorf("remote logs: %s", lr.FetchLogsResult.Error)
	}

	return lr.FetchLogsResult.Logs, nil
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

// hmacToken computes HMAC-SHA256 of the plaintext token using the dedicated
// HMAC key (loaded from hmac-key.bin in the CA directory). The key is a
// random 32-byte secret that never leaves the server, unlike the CA cert
// which is distributed to all enrolled agents.
func (s *Server) hmacToken(token string) []byte {
	mac := hmac.New(sha256.New, s.hmacKey)
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
		for _, pp := range pc.Ports {
			out[i].Ports = append(out[i].Ports, cluster.PortMapping{
				HostIP:        pp.HostIp,
				HostPort:      uint16(min(pp.HostPort, 65535)),      //nolint:gosec // port values are always ≤65535
				ContainerPort: uint16(min(pp.ContainerPort, 65535)), //nolint:gosec // port values are always ≤65535
				Protocol:      pp.Protocol,
			})
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
