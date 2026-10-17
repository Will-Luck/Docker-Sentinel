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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/selfupdate"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// ---------------------------------------------------------------------------
// Synchronous request/response helpers
// ---------------------------------------------------------------------------

// Upper bounds on how long the hub waits for an agent's reply. A connected
// agent that never answers (its Docker daemon is wedged, a handler hung)
// must not hold up the caller for ever: the scheduled scan waits on these
// calls one host at a time. The update bound allows for a large image pull.
// Variables so tests can shorten them.
var (
	listTimeout   = time.Minute
	updateTimeout = 15 * time.Minute
	actionTimeout = 2 * time.Minute
	logsTimeout   = time.Minute
)

// deliverPending sends an agent response to a waiting synchronous caller.
// Non-blocking: if nobody is waiting (or the channel is already full), the
// message is silently dropped (the fire-and-forget handlers already processed
// the response above).
func (s *Server) deliverPending(hostID, requestID string, msg *proto.AgentMessage) bool {
	key := hostID + ":" + requestID
	s.pendingMu.Lock()
	pr, ok := s.pending[key]
	if ok {
		delete(s.pending, key)
	}
	s.pendingMu.Unlock()

	if ok {
		// Non-blocking send. Channel is buffered (size 1), so this only
		// drops if something went very wrong (double delivery).
		select {
		case pr.ch <- msg:
		default:
		}
	}
	return ok
}

// pendingCaller reports whether a caller waits for request requestID of
// hostID, and whether it records and notifies what it asked for.
func (s *Server) pendingCaller(hostID, requestID string) (waiting, records bool) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	pr, ok := s.pending[hostID+":"+requestID]
	return ok, ok && pr.callerRecords
}

// requestPurpose returns what update request requestID of hostID was for
// ("update" or "rollback"), whether a caller still waits for it or the hub
// stopped waiting; "" when the hub does not know the request.
func (s *Server) requestPurpose(hostID, requestID string) string {
	if requestID == "" {
		return ""
	}
	key := hostID + ":" + requestID
	s.pendingMu.Lock()
	var purpose string
	pr, ok := s.pending[key]
	if ok {
		purpose = pr.purpose
	}
	s.pendingMu.Unlock()
	if ok {
		return purpose
	}
	s.lateMu.Lock()
	defer s.lateMu.Unlock()
	if lr, ok := s.late[key]; ok && time.Since(lr.at) <= lateRequestTTL {
		return lr.purpose
	}
	return ""
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
	s.pending[key] = &pendingRequest{ch: ch}
	return ch, nil
}

// cancelPending removes the pending channel for a host+request pair.
// Used for cleanup when sending fails after registration.
func (s *Server) cancelPending(hostID, requestID string, ch chan *proto.AgentMessage) {
	key := hostID + ":" + requestID
	s.pendingMu.Lock()
	if cur, ok := s.pending[key]; ok && cur.ch == ch {
		delete(s.pending, key)
	}
	s.pendingMu.Unlock()
}

// sendPending sends a request that registerPending has already recorded,
// on the host's current stream, and tags the pending entry with that
// stream so the stream's cleanup can fail it.
func (s *Server) sendPending(hostID, requestID string, msg *proto.ServerMessage) error {
	s.mu.RLock()
	as, ok := s.streams[hostID]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("agent %s not connected", s.hostLabel(hostID))
	}

	s.pendingMu.Lock()
	if as.closed {
		s.pendingMu.Unlock()
		return fmt.Errorf("agent %s disconnected", s.hostLabel(hostID))
	}
	if pr, ok := s.pending[hostID+":"+requestID]; ok {
		pr.stream = as
	}
	s.pendingMu.Unlock()

	select {
	case as.send <- msg:
		return nil
	default:
		return fmt.Errorf("agent %s send buffer full", s.hostLabel(hostID))
	}
}

// failPending closes the response channel of every request that went out on
// as, which is going away and so can never carry their replies. It also
// marks as closed so that no later request is tagged to it.
func (s *Server) failPending(as *agentStream) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	as.closed = true
	for key, pr := range s.pending {
		if pr.stream == as {
			delete(s.pending, key)
			close(pr.ch)
		}
	}
}

// awaitPending blocks on an already-registered response channel until a
// response arrives or the context is cancelled.
func (s *Server) awaitPending(ctx context.Context, hostID, requestID string, ch chan *proto.AgentMessage) (*proto.AgentMessage, error) {
	select {
	case resp, ok := <-ch:
		if !ok {
			// Channel was closed by the stream cleanup: the stream that
			// carried the request has gone.
			return nil, fmt.Errorf("agent %s disconnected", s.hostLabel(hostID))
		}
		return resp, nil
	case <-ctx.Done():
		s.cancelPending(hostID, requestID, ch)
		return nil, ctx.Err()
	}
}

// request sends msg to the agent under a fresh request ID and waits for the
// agent's reply, for at most timeout. what names the request in errors. If
// the wait runs out and purpose is set, the request is remembered so a
// reply that arrives later is recorded for what it was (recordLateUpdate).
func (s *Server) request(ctx context.Context, hostID, what, purpose string, timeout time.Duration, msg *proto.ServerMessage) (*proto.AgentMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reqID := generateRequestID()
	msg.RequestId = reqID

	// Register the response channel BEFORE sending, so a fast agent
	// response doesn't race with registration.
	ch, err := s.registerPending(hostID, reqID)
	if err != nil {
		return nil, err
	}
	s.pendingMu.Lock()
	if pr, ok := s.pending[hostID+":"+reqID]; ok {
		pr.callerRecords = callerRecords(ctx)
		pr.purpose = purpose
	}
	s.pendingMu.Unlock()

	if err := s.sendPending(hostID, reqID, msg); err != nil {
		s.cancelPending(hostID, reqID, ch)
		return nil, fmt.Errorf("send %s: %w", what, err)
	}

	resp, err := s.awaitPending(ctx, hostID, reqID, ch)
	if err != nil {
		err = fmt.Errorf("await %s: %w", what, err)
		if purpose == "" {
			return nil, err
		}
		// The stream dropped or the hub stopped waiting: the agent may
		// still do it, and say so later (late, or from its offline
		// journal). Its result then replaces the failure the caller
		// records at recordAt.
		recordAt := time.Now().UTC()
		s.rememberLate(hostID, reqID, purpose, recordAt)
		return nil, &cluster.UnansweredError{HostID: hostID, RequestID: reqID, RecordAt: recordAt, Err: err}
	}
	return resp, nil
}

// lateRequestTTL is how long the hub remembers a request it stopped
// waiting for.
const lateRequestTTL = time.Hour

type lateRequest struct {
	purpose string
	at      time.Time
	// recordAt is when the caller's failure row is recorded; the late
	// result is recorded at the same time, replacing it.
	recordAt time.Time
}

func (s *Server) rememberLate(hostID, requestID, purpose string, recordAt time.Time) {
	s.lateMu.Lock()
	defer s.lateMu.Unlock()
	if s.late == nil {
		s.late = make(map[string]lateRequest)
	}
	now := time.Now()
	for k, lr := range s.late {
		if now.Sub(lr.at) > lateRequestTTL {
			delete(s.late, k)
		}
	}
	s.late[hostID+":"+requestID] = lateRequest{purpose: purpose, at: now, recordAt: recordAt}
}

// takeLate returns (and forgets) the purpose of a request the hub stopped
// waiting for, or "".
func (s *Server) takeLate(hostID, requestID string) lateRequest {
	s.lateMu.Lock()
	defer s.lateMu.Unlock()
	key := hostID + ":" + requestID
	lr, ok := s.late[key]
	if !ok || time.Since(lr.at) > lateRequestTTL {
		return lateRequest{}
	}
	delete(s.late, key)
	return lr
}

// ListContainersSync sends a ListContainersRequest to the agent and blocks
// until the agent responds with a ContainerList or the context is cancelled.
// The request_id on the ServerMessage is used for correlation.
func (s *Server) ListContainersSync(ctx context.Context, hostID string) ([]cluster.ContainerInfo, error) {
	resp, err := s.request(ctx, hostID, "container list", "", listTimeout, &proto.ServerMessage{
		Payload: &proto.ServerMessage_ListContainers{
			ListContainers: &proto.ListContainersRequest{},
		},
	})
	if err != nil {
		return nil, err
	}

	cl, ok := resp.Payload.(*proto.AgentMessage_ContainerList)
	if !ok {
		return nil, fmt.Errorf("unexpected response type %T (wanted ContainerList)", resp.Payload)
	}
	if msg := cl.ContainerList.GetError(); msg != "" {
		return nil, fmt.Errorf("agent %s could not list its containers: %s", s.hostLabel(hostID), msg)
	}

	return protoToContainers(cl.ContainerList.Containers), nil
}

// UpdateContainerSync sends an UpdateContainerRequest to the agent and blocks
// until the agent responds with an UpdateResult or the context is cancelled.
func (s *Server) UpdateContainerSync(ctx context.Context, hostID, containerName, targetImage, targetDigest string) (*proto.UpdateResult, error) {
	return s.updateContainerSync(ctx, hostID, containerName, targetImage, targetDigest, "update")
}

// RollbackContainerSync moves a remote container back to image. On the
// agent it is an update; the hub records a late result as a rollback.
func (s *Server) RollbackContainerSync(ctx context.Context, hostID, containerName, image string) (*proto.UpdateResult, error) {
	return s.updateContainerSync(ctx, hostID, containerName, image, "", "rollback")
}

func (s *Server) updateContainerSync(ctx context.Context, hostID, containerName, targetImage, targetDigest, purpose string) (*proto.UpdateResult, error) {
	var registryAuth string
	if s.registryAuth != nil && targetImage != "" {
		registryAuth = s.registryAuth(targetImage)
	}
	req := &proto.UpdateContainerRequest{
		ContainerName: containerName,
		TargetImage:   targetImage,
		TargetDigest:  targetDigest,
		RegistryAuth:  registryAuth,
	}
	if purpose != "rollback" {
		if hs, ok := s.registry.Get(hostID); ok && hs.Info.State == cluster.HostPaused {
			return nil, fmt.Errorf("host %s is paused: no new updates are sent to it", hs.Info.Name)
		}
	}
	if prep := s.prep.Load(); prep != nil {
		if err := (*prep)(ctx, hostID, req, purpose == "rollback"); err != nil {
			return nil, err
		}
	}
	// From here the update is under way, whoever asked for it: every open
	// dashboard is told, and a row fetched until the agent answers shows
	// it updating (UpdateInFlight). The agent's result has its own event
	// (handleUpdateResult), which can reach a browser before the update is
	// out of flight, so a state event follows once it is.
	key := hostID + "::" + containerName
	s.inFlight.Add(key)
	s.bus.Publish(events.SSEEvent{
		Type:          events.EventContainerUpdate,
		ContainerName: containerName,
		HostID:        hostID,
		Message:       purpose + " started",
		Timestamp:     time.Now(),
	})
	defer func() {
		s.inFlight.Done(key)
		s.bus.Publish(events.SSEEvent{
			Type:          events.EventContainerState,
			ContainerName: containerName,
			HostID:        hostID,
			Timestamp:     time.Now(),
		})
	}()
	resp, err := s.request(ctx, hostID, "update result", purpose, updateTimeout, &proto.ServerMessage{
		Payload: &proto.ServerMessage_UpdateContainer{UpdateContainer: req},
	})
	var result *proto.UpdateResult
	if err == nil {
		ur, ok := resp.Payload.(*proto.AgentMessage_UpdateResult)
		if !ok {
			return nil, fmt.Errorf("unexpected response type %T (wanted UpdateResult)", resp.Payload)
		}
		result = ur.UpdateResult
		if result.GetOutcome() == cluster.OutcomePending {
			// The agent's helper does its update of itself: the hub
			// already waits for the outcome (handleUpdateResult), and
			// records it, and notifies it for a caller that notifies,
			// when it is known.
			return result, nil
		}
	}
	if !callerRecords(ctx) {
		s.recordForCaller(hostID, containerName, targetImage, purpose, result, err)
	}
	return result, err
}

// callerRecordsKey marks a context whose caller records the history of the
// remote update it starts (WithCallerRecords).
type callerRecordsKey struct{}

// WithCallerRecords marks ctx as that of a caller that records the history
// row of the remote update or rollback it starts (the dashboard, the API
// and the scan do). For an update started without it (the agent
// auto-update), the hub records the row itself.
func WithCallerRecords(ctx context.Context) context.Context {
	return context.WithValue(ctx, callerRecordsKey{}, true)
}

func callerRecords(ctx context.Context) bool {
	v, _ := ctx.Value(callerRecordsKey{}).(bool)
	return v
}

// recordForCaller records the history row of a remote update (or
// rollback) whose caller does not: the agent's result, or the failure to
// get one. An unanswered request's failure is written only if no row is at
// its time yet (its late result, recorded at the same time, wins).
func (s *Server) recordForCaller(hostID, name, target, purpose string, ur *proto.UpdateResult, err error) {
	if s.history == nil || (ur != nil && ur.GetOutcome() == "skipped") {
		return
	}
	// The container that changed: the agent's own, for a request naming
	// another Sentinel container on its host.
	if changed := ur.GetContainerName(); changed != "" {
		name = changed
	}
	rec := store.UpdateRecord{
		Timestamp:     time.Now(),
		ContainerName: store.ScopedKey(hostID, name),
		NewImage:      target,
		HostID:        hostID,
	}
	if hs, ok := s.registry.Get(hostID); ok {
		rec.HostName = hs.Info.Name
		for _, c := range hs.Containers {
			if c.Name == name {
				rec.OldImage, rec.OldDigest = c.Image, c.ImageDigest
			}
		}
	}
	if err != nil {
		rec.Outcome, rec.Error = "failed", err.Error()
		if purpose == "rollback" {
			rec.Outcome, rec.Error = cluster.RollbackRow("failed", err.Error())
		}
		rec.Timestamp = cluster.HistoryTime(err, rec.Timestamp)
		if ins, ok := s.history.(interface {
			RecordUpdateIfAbsent(store.UpdateRecord) (bool, error)
		}); ok && cluster.Unanswered(err) {
			if _, err := ins.RecordUpdateIfAbsent(rec); err != nil {
				s.log.Warn("failed to record a remote update", "hostID", hostID, "container", name, "error", err)
			}
			return
		}
	} else {
		if ur.GetOldImage() != "" {
			rec.OldImage, rec.OldDigest = ur.GetOldImage(), ur.GetOldDigest()
		}
		if ur.GetNewImage() != "" {
			rec.NewImage = ur.GetNewImage()
		}
		rec.NewDigest, rec.Outcome, rec.Error = ur.GetNewDigest(), ur.GetOutcome(), ur.GetError()
		if purpose == "rollback" {
			rec.Outcome, rec.Error = cluster.RollbackRow(ur.GetOutcome(), ur.GetError())
		}
		if d := ur.GetDuration(); d != nil {
			rec.Duration = d.AsDuration()
		}
	}
	if err := s.history.RecordUpdate(rec); err != nil {
		s.log.Warn("failed to record a remote update", "hostID", hostID, "container", name, "error", err)
	}
}

// DiscardSwapSync asks the agent on hostID to discard its record of a swap
// of name that it cannot finish, once the user has fixed the containers on
// the host. An agent before v3.0.0 cannot: ErrAgentTooOld, and nothing is
// sent. ErrNoSwapRecord when it has no record, ErrSwapBusy while an update
// or recovery holds the container.
func (s *Server) DiscardSwapSync(ctx context.Context, hostID, name string) error {
	if !s.hostHas(hostID, cluster.FeatureDiscardSwap) {
		return cluster.ErrAgentTooOld
	}
	resp, err := s.request(ctx, hostID, "container action result", "", actionTimeout, &proto.ServerMessage{
		Payload: &proto.ServerMessage_ContainerAction{
			ContainerAction: &proto.ContainerActionRequest{ContainerName: name, Action: cluster.FeatureDiscardSwap},
		},
	})
	if err != nil {
		return err
	}
	ar, ok := resp.Payload.(*proto.AgentMessage_ContainerActionResult)
	if !ok {
		return fmt.Errorf("unexpected response type %T (wanted ContainerActionResult)", resp.Payload)
	}
	switch r := ar.ContainerActionResult; r.GetOutcome() {
	case "success":
		return nil
	case "not_found":
		return cluster.ErrNoSwapRecord
	case "busy":
		return cluster.ErrSwapBusy
	default:
		if strings.Contains(r.GetError(), "unknown action") {
			return cluster.ErrAgentTooOld
		}
		return fmt.Errorf("discard the swap record of %s: %s", name, r.GetError())
	}
}

// hostHas reports whether the agent on hostID advertises feature: in its
// heartbeats on the current stream, else in its last state report.
func (s *Server) hostHas(hostID, feature string) bool {
	s.mu.RLock()
	as, ok := s.streams[hostID]
	s.mu.RUnlock()
	if ok {
		as.mu.RLock()
		has := slices.Contains(as.features, feature)
		as.mu.RUnlock()
		if has {
			return true
		}
	}
	hs, ok := s.registry.Get(hostID)
	return ok && slices.Contains(hs.Info.Features, feature)
}

// PullImageSync asks the agent on hostID to pull ref for the container
// name (a pull-only update), with the registry credentials the hub holds
// for ref, and returns how it went. An agent before v3.0.0 does not report
// pulls: it is asked all the same, and ErrPullUnconfirmed returned.
func (s *Server) PullImageSync(ctx context.Context, hostID, name, ref string) error {
	req := &proto.PullImageRequest{ImageRef: ref, ContainerName: name}
	if s.registryAuth != nil {
		req.RegistryAuth = s.registryAuth(ref)
	}
	msg := &proto.ServerMessage{Payload: &proto.ServerMessage_PullImage{PullImage: req}}
	if !s.hostHas(hostID, cluster.FeaturePullResult) {
		if err := s.SendCommand(hostID, msg); err != nil {
			return err
		}
		return cluster.ErrPullUnconfirmed
	}
	resp, err := s.request(ctx, hostID, "pull result", "", updateTimeout, msg)
	if err != nil {
		return err
	}
	ar, ok := resp.Payload.(*proto.AgentMessage_ContainerActionResult)
	if !ok {
		return fmt.Errorf("unexpected response type %T (wanted ContainerActionResult)", resp.Payload)
	}
	if r := ar.ContainerActionResult; r.GetOutcome() != "success" {
		return fmt.Errorf("pull %s on the agent: %s", ref, r.GetError())
	}
	return nil
}

// HostPaused reports whether hostID is paused: it gets no new updates.
func (s *Server) HostPaused(hostID string) bool {
	hs, ok := s.registry.Get(hostID)
	return ok && hs.Info.State == cluster.HostPaused
}

// UpdateInFlight reports whether the hub is waiting for an agent to update
// (or roll back) the container name on hostID.
func (s *Server) UpdateInFlight(hostID, name string) bool {
	return s.inFlight.Has(hostID + "::" + name)
}

// keyCounter counts the operations in flight per key.
type keyCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *keyCounter) Add(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = make(map[string]int)
	}
	c.n[key]++
}

func (c *keyCounter) Done(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[key]--; c.n[key] <= 0 {
		delete(c.n, key)
	}
}

func (c *keyCounter) Has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[key] > 0
}

// ContainerActionSync sends a ContainerActionRequest to the agent and blocks
// until the agent responds with a ContainerActionResult or the context is cancelled.
func (s *Server) ContainerActionSync(ctx context.Context, hostID, containerName, action string) error {
	resp, err := s.request(ctx, hostID, "container action result", "", actionTimeout, &proto.ServerMessage{
		Payload: &proto.ServerMessage_ContainerAction{
			ContainerAction: &proto.ContainerActionRequest{
				ContainerName: containerName,
				Action:        action,
			},
		},
	})
	if err != nil {
		return err
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
	if lines <= 0 {
		lines = 50
	}
	if lines > 500 {
		lines = 500
	}

	resp, err := s.request(ctx, hostID, "fetch logs", "", logsTimeout, &proto.ServerMessage{
		Payload: &proto.ServerMessage_FetchLogs{
			FetchLogs: &proto.FetchLogsRequest{
				ContainerName: containerName,
				Lines:         int32(lines), //nolint:gosec // clamped to 0-500 above
			},
		},
	})
	if err != nil {
		return "", err
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
	serial, ok := peerCertSerial(ctx)
	if !ok {
		return false, nil
	}
	return s.store.IsRevokedCert(serial)
}

// peerCertSerial returns the serial (lower-case hex, as stored) of the
// client certificate the peer presented, if any.
func peerCertSerial(ctx context.Context) (string, bool) {
	leaf := peerLeaf(ctx)
	if leaf == nil {
		return "", false
	}
	return fmt.Sprintf("%x", leaf.SerialNumber), true
}

// peerLeaf returns the client certificate the peer presented, or nil.
func peerLeaf(ctx context.Context) *x509.Certificate {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return nil
	}
	return tlsInfo.State.PeerCertificates[0]
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
	out := make([]cluster.ContainerInfo, 0, len(pcs))
	for _, pc := range pcs {
		// A self-update helper lives for a minute and is not a container
		// to show or update.
		if selfupdate.IsHelper(pc.Labels) {
			continue
		}
		out = append(out, cluster.ContainerInfo{
			ID:          pc.Id,
			Name:        pc.Name,
			Image:       pc.Image,
			ImageDigest: pc.ImageDigest,
			State:       pc.State,
			Labels:      pc.Labels,
		})
		c := &out[len(out)-1]
		if pc.Created != nil {
			c.Created = pc.Created.AsTime()
		}
		for _, pp := range pc.Ports {
			c.Ports = append(c.Ports, cluster.PortMapping{
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
