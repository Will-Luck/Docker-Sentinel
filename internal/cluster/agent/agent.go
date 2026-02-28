// Package agent implements the Sentinel agent that connects to a Sentinel
// server over gRPC, reports local container state, and executes update
// commands on the Docker host it runs on.
//
// The agent handles its full lifecycle: enrollment (one-time key exchange
// with PKCS#10 CSR), mTLS connection, heartbeat keepalive, bidirectional
// command streaming, and exponential-backoff reconnection.
package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// Supported agent features advertised during heartbeat and state reports.
var supportedFeatures = []string{
	"update",
	"hooks",
	"pull",
	"list",
}

// DockerAPI defines the subset of Docker operations the agent needs.
// This is intentionally narrow — the agent only needs container lifecycle
// operations, not swarm, image cleanup, or distribution checks.
type DockerAPI interface {
	ListContainers(ctx context.Context) ([]container.Summary, error)
	ListAllContainers(ctx context.Context) ([]container.Summary, error)
	InspectContainer(ctx context.Context, id string) (container.InspectResponse, error)
	StopContainer(ctx context.Context, id string, timeout int) error
	RemoveContainer(ctx context.Context, id string) error
	CreateContainer(ctx context.Context, name string, cfg *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig) (string, error)
	StartContainer(ctx context.Context, id string) error
	RestartContainer(ctx context.Context, id string) error
	PullImage(ctx context.Context, refStr string) error
	ImageDigest(ctx context.Context, imageRef string) (string, error)
	ExecContainer(ctx context.Context, id string, cmd []string, timeout int) (int, string, error)
}

// Config holds agent-specific configuration.
type Config struct {
	ServerAddr         string        // gRPC server address (host:port)
	EnrollToken        string        // one-time enrollment token (empty if already enrolled)
	HostName           string        // human-readable label for this agent
	DataDir            string        // directory for certs, keys, and state files
	GracePeriodOffline time.Duration // time before autonomous mode activates
	DockerSock         string        // Docker socket path (informational)
	Version            string        // agent binary version
}

// Agent connects to a Sentinel server and executes commands on the local
// Docker host. It is the client-side counterpart to the server's gRPC
// services (EnrollmentService + AgentService).
type Agent struct {
	cfg    Config
	docker DockerAPI
	hostID string // assigned by server during enrollment
	log    *slog.Logger

	conn       *grpc.ClientConn
	enrollment proto.EnrollmentServiceClient
	service    proto.AgentServiceClient

	// Paths to the agent's mTLS credentials on disk, all under DataDir.
	certPath string
	keyPath  string
	caPath   string

	mu             sync.RWMutex
	connected      bool
	containerCount int

	// offlineSince tracks when server connectivity was lost.
	// Zero value means currently connected.
	offlineSince time.Time

	dedup    *dedup
	policies *policyCache
	journal  *journal
}

// New creates a new Agent. Call Run to start the main loop.
func New(cfg Config, docker DockerAPI, log *slog.Logger) *Agent {
	return &Agent{
		cfg:      cfg,
		docker:   docker,
		log:      log,
		certPath: filepath.Join(cfg.DataDir, "agent.pem"),
		keyPath:  filepath.Join(cfg.DataDir, "agent-key.pem"),
		caPath:   filepath.Join(cfg.DataDir, "ca.pem"),
		dedup:    newDedup(1000),
		policies: newPolicyCache(),
	}
}

// Run starts the agent. It handles enrollment (if not already enrolled),
// connects to the server with mTLS, and enters the main heartbeat +
// command loop. Run blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	a.log.Info("agent starting", "server", a.cfg.ServerAddr, "host", a.cfg.HostName)

	if err := os.MkdirAll(a.cfg.DataDir, 0700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// Initialise the offline journal (loads any entries surviving a restart).
	jrnl, err := newJournal(a.cfg.DataDir)
	if err != nil {
		return fmt.Errorf("init journal: %w", err)
	}
	a.journal = jrnl
	if n := a.journal.Len(); n > 0 {
		a.log.Info("loaded offline journal from disk", "entries", n)
	}

	// Restore cached policies so autonomous mode has them immediately.
	if err := a.loadPolicyCache(); err != nil {
		a.log.Warn("failed to load policy cache, using defaults", "error", err)
	}

	// Step 1: Enroll if we don't have certs yet.
	if !a.isEnrolled() {
		if a.cfg.EnrollToken == "" {
			return fmt.Errorf("not enrolled and no enrollment token provided")
		}
		a.log.Info("not enrolled, starting enrollment")
		if err := a.enroll(ctx); err != nil {
			return fmt.Errorf("enrollment failed: %w", err)
		}
		a.log.Info("enrollment complete", "host_id", a.hostID)
	} else {
		// Load the host ID from the previously saved file.
		id, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "host-id"))
		if err != nil {
			return fmt.Errorf("read host id: %w", err)
		}
		a.hostID = strings.TrimSpace(string(id))
		a.log.Info("already enrolled", "host_id", a.hostID)
	}

	// Step 2: Main reconnection loop — reconnect with backoff on any error.
	// If the server stays unreachable past GracePeriodOffline, the agent
	// enters autonomous mode (monitoring only, no updates) while continuing
	// reconnection attempts in the background.
	bo := newBackoff()
	var autonomousCancel context.CancelFunc

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// If autonomous mode was running, cancel it — we're about to
		// attempt a real session. If the session fails we'll restart it.
		if autonomousCancel != nil {
			autonomousCancel()
			autonomousCancel = nil
			a.log.Info("autonomous mode suspended for reconnection attempt")
		}

		sessionStart := time.Now()
		err := a.runSession(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// If the session lasted more than a minute, the connection was
		// healthy — reset backoff so the next reconnect starts fast.
		if time.Since(sessionStart) > time.Minute {
			bo.reset()
		}

		// Track when we went offline for autonomous mode decisions.
		a.setOffline()

		// Enter autonomous mode if the grace period has elapsed.
		// The autonomous loop runs in its own goroutine and is
		// cancelled when the next reconnection attempt starts.
		if a.shouldEnterAutonomous() {
			autoCtx, cancel := context.WithCancel(ctx)
			autonomousCancel = cancel
			go func() {
				if err := a.runAutonomous(autoCtx); err != nil && autoCtx.Err() == nil {
					a.log.Error("autonomous mode exited with error", "error", err)
				}
			}()
		}

		wait := bo.next()
		a.log.Warn("session ended, reconnecting", "error", err, "backoff", wait)

		select {
		case <-ctx.Done():
			if autonomousCancel != nil {
				autonomousCancel()
			}
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// runSession connects to the server, reports initial state, opens the
// bidirectional channel, and runs the heartbeat + receive loops. Returns
// on any stream error (caller handles reconnect).
func (a *Agent) runSession(ctx context.Context) error {
	if err := a.connect(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer a.closeConn()

	// Send initial full state report.
	containers, err := a.listLocalContainers(ctx)
	if err != nil {
		return fmt.Errorf("list containers for state report: %w", err)
	}

	ack, err := a.service.ReportState(ctx, &proto.StateReport{
		HostId:            a.hostID,
		Containers:        containers,
		Timestamp:         timestamppb.Now(),
		AgentVersion:      a.cfg.Version,
		SupportedFeatures: supportedFeatures,
	})
	if err != nil {
		return fmt.Errorf("report state: %w", err)
	}
	if !ack.Accepted {
		return fmt.Errorf("state report rejected: %s", ack.Message)
	}
	a.log.Info("state report accepted", "message", ack.Message)

	// Open bidirectional channel.
	stream, err := a.service.Channel(ctx)
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}

	a.setConnected()
	a.log.Info("channel established")

	// Sync any offline journal entries from autonomous mode.
	if err := a.syncJournal(stream); err != nil {
		a.log.Error("journal sync failed, entries remain on disk", "error", err)
		// Non-fatal — continue with the session. Entries will be
		// retried on the next reconnection.
	}

	// Run heartbeat and receive loops concurrently. When either exits,
	// the session is over.
	errCh := make(chan error, 2)
	go func() { errCh <- a.heartbeatLoop(ctx, stream) }()
	go func() { errCh <- a.receiveLoop(ctx, stream) }()

	// First error tears down the session.
	return <-errCh
}

// isEnrolled returns true if the agent's certificate and key files exist.
func (a *Agent) isEnrolled() bool {
	_, certErr := os.Stat(a.certPath)
	_, keyErr := os.Stat(a.keyPath)
	_, caErr := os.Stat(a.caPath)
	return certErr == nil && keyErr == nil && caErr == nil
}

// enroll performs the one-time enrollment handshake with the server.
// Generates an ECDSA P-256 key pair, creates a PKCS#10 CSR, connects
// to the server WITHOUT mTLS, and exchanges the enrollment token for
// signed certificates.
func (a *Agent) enroll(ctx context.Context) error {
	// Generate agent key pair.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	// Create CSR with CN = HostName.
	csrTemplate := &x509.CertificateRequest{
		Subject: x509.CertificateRequest{}.Subject, // zero value
	}
	csrTemplate.Subject.CommonName = a.cfg.HostName
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, key)
	if err != nil {
		return fmt.Errorf("create csr: %w", err)
	}

	// Connect with TLS but no client cert for enrollment. The server
	// uses TLS on the gRPC port (VerifyClientCertIfGiven), so we must
	// speak TLS — but we skip server verification because we don't have
	// the CA cert yet (it comes back in the enrollment response).
	enrollTLS := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // bootstrapping — no CA yet
		MinVersion:         tls.VersionTLS13,
	}
	conn, err := grpc.NewClient(
		a.cfg.ServerAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(enrollTLS)),
	)
	if err != nil {
		return fmt.Errorf("dial for enrollment: %w", err)
	}
	defer conn.Close()

	client := proto.NewEnrollmentServiceClient(conn)

	resp, err := client.Enroll(ctx, &proto.EnrollRequest{
		Token:    a.cfg.EnrollToken,
		HostName: a.cfg.HostName,
		Csr:      csrDER,
	})
	if err != nil {
		return fmt.Errorf("enroll rpc: %w", err)
	}

	a.hostID = resp.HostId

	// Persist credentials to disk. Order matters: write key last so a
	// partial write leaves us in an "unenrolled" state that retries
	// cleanly.
	if err := os.WriteFile(a.caPath, resp.CaCert, 0600); err != nil {
		return fmt.Errorf("write ca cert: %w", err)
	}
	if err := os.WriteFile(a.certPath, resp.AgentCert, 0600); err != nil {
		return fmt.Errorf("write agent cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(a.keyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("write agent key: %w", err)
	}

	// Save host ID so we can reload it on restart.
	idPath := filepath.Join(a.cfg.DataDir, "host-id")
	if err := os.WriteFile(idPath, []byte(a.hostID), 0600); err != nil {
		return fmt.Errorf("write host id: %w", err)
	}

	return nil
}

// connect establishes a gRPC connection with mTLS using the agent's
// enrolled certificate and the server CA.
func (a *Agent) connect(ctx context.Context) error {
	cert, err := tls.LoadX509KeyPair(a.certPath, a.keyPath)
	if err != nil {
		return fmt.Errorf("load agent cert/key: %w", err)
	}

	caPEM, err := os.ReadFile(a.caPath)
	if err != nil {
		return fmt.Errorf("read ca cert: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("failed to parse ca cert")
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}

	conn, err := grpc.NewClient(
		a.cfg.ServerAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		return fmt.Errorf("dial server: %w", err)
	}

	a.conn = conn
	a.enrollment = proto.NewEnrollmentServiceClient(conn)
	a.service = proto.NewAgentServiceClient(conn)
	return nil
}

// closeConn tears down the gRPC connection.
func (a *Agent) closeConn() {
	if a.conn != nil {
		a.conn.Close()
		a.conn = nil
	}
}

// heartbeatLoop sends periodic heartbeats to the server. Returns on
// stream error or context cancellation.
func (a *Agent) heartbeatLoop(ctx context.Context, stream proto.AgentService_ChannelClient) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			msg := &proto.AgentMessage{
				Payload: &proto.AgentMessage_Heartbeat{
					Heartbeat: &proto.Heartbeat{
						Timestamp:         timestamppb.Now(),
						AgentVersion:      a.cfg.Version,
						SupportedFeatures: supportedFeatures,
						HostId:            a.hostID,
					},
				},
			}
			if err := stream.Send(msg); err != nil {
				return fmt.Errorf("send heartbeat: %w", err)
			}
			a.log.Debug("heartbeat sent")
		}
	}
}

// receiveLoop reads ServerMessages from the bidirectional stream and
// dispatches them to the appropriate handler. Each command is handled
// in its own goroutine to avoid blocking the receive loop; the dedup
// tracker prevents duplicate execution on reconnect/replay.
func (a *Agent) receiveLoop(ctx context.Context, stream proto.AgentService_ChannelClient) error {
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return fmt.Errorf("server closed stream")
		}
		if err != nil {
			return fmt.Errorf("receive: %w", err)
		}

		reqID := msg.GetRequestId()

		// Dedup: skip if we've already processed this request.
		if reqID != "" && a.dedup.isSeen(reqID) {
			a.log.Debug("skipping duplicate request", "request_id", reqID)
			continue
		}

		// Dispatch based on the oneof payload type.
		switch p := msg.Payload.(type) {
		case *proto.ServerMessage_Heartbeat:
			a.log.Debug("server heartbeat received")

		case *proto.ServerMessage_ListContainers:
			go a.safeHandle("list-containers", reqID, func() error {
				return a.handleListContainers(ctx, stream, reqID)
			})

		case *proto.ServerMessage_UpdateContainer:
			go a.safeHandle("update-container", reqID, func() error {
				return a.handleUpdateContainer(ctx, stream, p.UpdateContainer, reqID)
			})

		case *proto.ServerMessage_ContainerAction:
			go a.safeHandle("container-action", reqID, func() error {
				return a.handleContainerAction(ctx, stream, p.ContainerAction, reqID)
			})

		case *proto.ServerMessage_PullImage:
			go a.safeHandle("pull-image", reqID, func() error {
				return a.handlePullImage(ctx, p.PullImage)
			})

		case *proto.ServerMessage_RunHook:
			go a.safeHandle("run-hook", reqID, func() error {
				return a.handleRunHook(ctx, stream, p.RunHook, reqID)
			})

		case *proto.ServerMessage_Rollback:
			// Phase 5 — needs local snapshot store. Log and skip for now.
			a.log.Warn("rollback not yet implemented", "container", p.Rollback.GetContainerName())

		case *proto.ServerMessage_PolicySync:
			a.handlePolicySync(p.PolicySync)

		case *proto.ServerMessage_SettingsSync:
			a.handleSettingsSync(p.SettingsSync)

		case *proto.ServerMessage_CertRenewalResponse:
			go a.safeHandle("cert-renewal", reqID, func() error {
				return a.handleCertRenewal(p.CertRenewalResponse)
			})

		default:
			a.log.Warn("unknown server message type", "request_id", reqID)
		}
	}
}

// safeHandle wraps a command handler so that panics and errors are logged
// but never crash the agent. This is critical — one bad command must not
// take down the entire agent process.
func (a *Agent) safeHandle(op, reqID string, fn func() error) {
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("handler panic", "op", op, "request_id", reqID, "panic", r)
		}
	}()

	if err := fn(); err != nil {
		a.log.Error("handler failed", "op", op, "request_id", reqID, "error", err)
	}
}

// --- Command Handlers ---

// handleListContainers lists local containers and sends the result back
// to the server.
func (a *Agent) handleListContainers(ctx context.Context, stream proto.AgentService_ChannelClient, requestID string) error {
	a.log.Info("listing containers", "request_id", requestID)

	containers, err := a.listLocalContainers(ctx)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	a.mu.Lock()
	a.containerCount = len(containers)
	a.mu.Unlock()

	msg := &proto.AgentMessage{
		Payload: &proto.AgentMessage_ContainerList{
			ContainerList: &proto.ContainerList{
				RequestId:  requestID,
				Containers: containers,
			},
		},
	}
	return stream.Send(msg)
}

// handleUpdateContainer executes the full update lifecycle for a container.
// Flow: inspect -> pull -> stop -> remove -> create -> start -> report.
func (a *Agent) handleUpdateContainer(ctx context.Context, stream proto.AgentService_ChannelClient, req *proto.UpdateContainerRequest, requestID string) error {
	name := req.GetContainerName()
	targetImage := req.GetTargetImage()
	a.log.Info("updating container", "name", name, "target", targetImage, "request_id", requestID)

	start := time.Now()
	oldImage, oldDigest, newDigest, err := a.recreateContainer(ctx, name, targetImage)
	dur := time.Since(start)

	result := &proto.UpdateResult{
		RequestId:     requestID,
		ContainerName: name,
		OldImage:      oldImage,
		OldDigest:     oldDigest,
		NewImage:      targetImage,
		NewDigest:     newDigest,
		Duration:      durationpb.New(dur),
	}

	if err != nil {
		result.Outcome = "failed"
		result.Error = err.Error()
		a.log.Error("update failed", "name", name, "error", err, "duration", dur)
	} else {
		result.Outcome = "success"
		a.log.Info("update succeeded", "name", name, "old_image", oldImage, "new_digest", newDigest, "duration", dur)
	}

	msg := &proto.AgentMessage{
		Payload: &proto.AgentMessage_UpdateResult{
			UpdateResult: result,
		},
	}
	return stream.Send(msg)
}

// handleContainerAction executes a stop, start, or restart action on a
// named container and sends the result back to the server.
func (a *Agent) handleContainerAction(ctx context.Context, stream proto.AgentService_ChannelClient, req *proto.ContainerActionRequest, requestID string) error {
	name := req.GetContainerName()
	action := req.GetAction()
	a.log.Info("container action", "name", name, "action", action, "request_id", requestID)

	cID, err := a.findContainerID(ctx, name)
	if err == nil {
		switch action {
		case "stop":
			err = a.docker.StopContainer(ctx, cID, 10)
		case "start":
			err = a.docker.StartContainer(ctx, cID)
		case "restart":
			err = a.docker.RestartContainer(ctx, cID)
		default:
			err = fmt.Errorf("unknown action: %s", action)
		}
	}

	result := &proto.ContainerActionResult{
		RequestId:     requestID,
		ContainerName: name,
		Action:        action,
	}
	if err != nil {
		result.Outcome = "failed"
		result.Error = err.Error()
		a.log.Error("container action failed", "name", name, "action", action, "error", err)
	} else {
		result.Outcome = "success"
		a.log.Info("container action succeeded", "name", name, "action", action)
	}

	if err := stream.Send(&proto.AgentMessage{
		Payload: &proto.AgentMessage_ContainerActionResult{ContainerActionResult: result},
	}); err != nil {
		return err
	}

	// Push a fresh container list so the server cache and UI reflect the
	// new state immediately (e.g. a stopped container stays visible).
	return a.handleListContainers(ctx, stream, "")
}

// handlePullImage pulls an image on the local Docker host.
func (a *Agent) handlePullImage(ctx context.Context, req *proto.PullImageRequest) error {
	ref := req.GetImageRef()
	a.log.Info("pulling image", "ref", ref)

	if err := a.docker.PullImage(ctx, ref); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}

	a.log.Info("pull complete", "ref", ref)
	return nil
}

// handleRunHook executes a command inside a container and sends the
// result back to the server.
func (a *Agent) handleRunHook(ctx context.Context, stream proto.AgentService_ChannelClient, req *proto.RunHookRequest, requestID string) error {
	name := req.GetContainerName()
	phase := req.GetPhase()
	cmd := req.GetCommand()
	timeout := int(req.GetTimeoutSeconds())
	if timeout <= 0 {
		timeout = 30
	}

	a.log.Info("running hook", "container", name, "phase", phase, "command", cmd, "request_id", requestID)

	// Find the container ID by name.
	cID, err := a.findContainerID(ctx, name)
	if err != nil {
		return a.sendHookResult(stream, requestID, name, phase, -1, "", err.Error())
	}

	// Split command string into args. Simple shell-like split — hooks
	// are expected to be simple commands, not complex shell scripts.
	args := strings.Fields(cmd)
	if len(args) == 0 {
		return a.sendHookResult(stream, requestID, name, phase, -1, "", "empty command")
	}

	exitCode, output, execErr := a.docker.ExecContainer(ctx, cID, args, timeout)

	errStr := ""
	if execErr != nil {
		errStr = execErr.Error()
	}

	// Clamp exit code to int32 range. In practice Docker exit codes
	// are 0-255, so this is purely defensive.
	code := clampInt32(exitCode)

	return a.sendHookResult(stream, requestID, name, phase, code, output, errStr)
}

// handleCertRenewal saves a newly signed certificate received from the
// server during cert rotation.
func (a *Agent) handleCertRenewal(resp *proto.CertRenewalResponse) error {
	certPEM := resp.GetAgentCert()
	if len(certPEM) == 0 {
		return fmt.Errorf("empty cert in renewal response")
	}

	a.log.Info("saving renewed certificate")
	if err := os.WriteFile(a.certPath, certPEM, 0600); err != nil {
		return fmt.Errorf("write renewed cert: %w", err)
	}

	return nil
}

// --- Container Operations ---

// recreateContainer stops, removes, and recreates a container with a new
// image. Preserves all configuration from the current container inspect.
// Returns the old image, old digest, new digest, and any error.
func (a *Agent) recreateContainer(ctx context.Context, name, targetImage string) (oldImage, oldDigest, newDigest string, err error) {
	// Find and inspect the current container.
	cID, err := a.findContainerID(ctx, name)
	if err != nil {
		return "", "", "", fmt.Errorf("find container %s: %w", name, err)
	}

	inspect, err := a.docker.InspectContainer(ctx, cID)
	if err != nil {
		return "", "", "", fmt.Errorf("inspect %s: %w", name, err)
	}

	oldImage = inspect.Config.Image

	// Get current image digest for the audit trail.
	oldDigest, _ = a.docker.ImageDigest(ctx, oldImage)

	// Pull the target image.
	if err := a.docker.PullImage(ctx, targetImage); err != nil {
		return oldImage, oldDigest, "", fmt.Errorf("pull %s: %w", targetImage, err)
	}

	// Get the new image's digest.
	newDigest, _ = a.docker.ImageDigest(ctx, targetImage)

	// Stop the running container. 30s timeout is generous but safe.
	if err := a.docker.StopContainer(ctx, cID, 30); err != nil {
		return oldImage, oldDigest, newDigest, fmt.Errorf("stop %s: %w", name, err)
	}

	// Remove the old container.
	if err := a.docker.RemoveContainer(ctx, cID); err != nil {
		return oldImage, oldDigest, newDigest, fmt.Errorf("remove %s: %w", name, err)
	}

	// Rebuild the container config with the new image. We extract
	// Config, HostConfig, and NetworkingConfig from the inspect result.
	cfg, hostCfg, netCfg := configFromInspect(&inspect, targetImage)

	newID, err := a.docker.CreateContainer(ctx, name, cfg, hostCfg, netCfg)
	if err != nil {
		return oldImage, oldDigest, newDigest, fmt.Errorf("create %s: %w", name, err)
	}

	if err := a.docker.StartContainer(ctx, newID); err != nil {
		return oldImage, oldDigest, newDigest, fmt.Errorf("start %s: %w", name, err)
	}

	return oldImage, oldDigest, newDigest, nil
}

// configFromInspect extracts container creation parameters from an
// InspectResponse, replacing the image with targetImage. This preserves
// env vars, volumes, ports, networks, and all other configuration from
// the original container.
func configFromInspect(inspect *container.InspectResponse, targetImage string) (*container.Config, *container.HostConfig, *network.NetworkingConfig) {
	cfgCopy := *inspect.Config
	cfgCopy.Image = targetImage

	hostCfg := inspect.HostConfig

	// Rebuild NetworkingConfig from the inspect's network settings.
	// Only copy user-specified fields (IPAM, aliases, driver opts).
	// Copying runtime fields (Gateway, IPAddress, etc.) causes conflicts
	// when Docker tries to assign them on the new container.
	netCfg := &network.NetworkingConfig{}
	if inspect.NetworkSettings != nil && len(inspect.NetworkSettings.Networks) > 0 {
		netCfg.EndpointsConfig = make(map[string]*network.EndpointSettings, len(inspect.NetworkSettings.Networks))
		for name, ep := range inspect.NetworkSettings.Networks {
			netCfg.EndpointsConfig[name] = &network.EndpointSettings{
				IPAMConfig: ep.IPAMConfig,
				Aliases:    ep.Aliases,
				DriverOpts: ep.DriverOpts,
				NetworkID:  ep.NetworkID,
				MacAddress: ep.MacAddress,
			}
		}
	}

	return &cfgCopy, hostCfg, netCfg
}

// --- Helpers ---

// listLocalContainers fetches all containers (regardless of state) from the
// local Docker daemon and converts them to proto ContainerInfo messages.
// Using ListAllContainers ensures stopped containers remain visible on the
// dashboard after a stop action.
func (a *Agent) listLocalContainers(ctx context.Context) ([]*proto.ContainerInfo, error) {
	summaries, err := a.docker.ListAllContainers(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*proto.ContainerInfo, 0, len(summaries))
	for i := range summaries {
		// Skip Swarm task containers — they're managed by the Swarm
		// orchestrator and can't be updated through the recreate flow.
		if _, isTask := summaries[i].Labels["com.docker.swarm.task"]; isTask {
			continue
		}
		out = append(out, containerInfoFromSummary(&summaries[i]))
	}
	return out, nil
}

// containerInfoFromSummary converts a Docker container.Summary into a
// proto ContainerInfo suitable for sending over the wire.
func containerInfoFromSummary(c *container.Summary) *proto.ContainerInfo {
	name := ""
	if len(c.Names) > 0 {
		// Docker prefixes names with "/" — strip it for cleanliness.
		name = strings.TrimPrefix(c.Names[0], "/")
	}

	info := &proto.ContainerInfo{
		Id:    c.ID,
		Name:  name,
		Image: c.Image,
		State: string(c.State),
	}

	if len(c.Labels) > 0 {
		info.Labels = c.Labels
	}

	// container.Summary.Created is Unix timestamp (int64).
	if c.Created > 0 {
		info.Created = timestamppb.New(time.Unix(c.Created, 0))
	}

	return info
}

// findContainerID looks up a container by name and returns its ID.
// Uses ListAllContainers so it can locate stopped containers (e.g. to
// start them after a previous stop action).
func (a *Agent) findContainerID(ctx context.Context, name string) (string, error) {
	containers, err := a.docker.ListAllContainers(ctx)
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}

	for _, c := range containers {
		for _, n := range c.Names {
			// Docker names are prefixed with "/".
			if strings.TrimPrefix(n, "/") == name {
				return c.ID, nil
			}
		}
	}
	return "", fmt.Errorf("container %q not found", name)
}

// sendHookResult sends a HookResult message back to the server.
func (a *Agent) sendHookResult(stream proto.AgentService_ChannelClient, requestID, container, phase string, exitCode int32, output, errStr string) error {
	msg := &proto.AgentMessage{
		Payload: &proto.AgentMessage_HookResult{
			HookResult: &proto.HookResult{
				RequestId:     requestID,
				ContainerName: container,
				Phase:         phase,
				ExitCode:      exitCode,
				Output:        output,
				Error:         errStr,
			},
		},
	}
	return stream.Send(msg)
}

// setConnected marks the agent as connected and clears the offline timer.
func (a *Agent) setConnected() {
	a.mu.Lock()
	defer a.mu.Unlock()
	wasOffline := !a.offlineSince.IsZero()
	a.connected = true
	a.offlineSince = time.Time{}
	if wasOffline {
		a.log.Info("connection restored")
	}
}

// setOffline marks the agent as disconnected and starts the offline timer.
func (a *Agent) setOffline() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected = false
	if a.offlineSince.IsZero() {
		a.offlineSince = time.Now()
		a.log.Warn("lost connection to server")
	}
}

// --- Dedup ---

// dedup tracks recently processed request IDs to prevent duplicate
// execution on reconnection or message replay. Entries are automatically
// cleaned up after 5 minutes.
type dedup struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	maxSize int
}

func newDedup(maxSize int) *dedup {
	return &dedup{
		seen:    make(map[string]time.Time),
		maxSize: maxSize,
	}
}

// isSeen checks if a request ID has been processed recently. If not, it
// marks it as seen and returns false. Thread-safe.
func (d *dedup) isSeen(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.seen[id]; ok {
		return true
	}

	d.seen[id] = time.Now()

	// Periodic cleanup — run when map is getting large.
	if len(d.seen) > d.maxSize {
		d.cleanup()
	}

	return false
}

// cleanup removes entries older than 5 minutes. Must be called with
// d.mu held.
func (d *dedup) cleanup() {
	cutoff := time.Now().Add(-5 * time.Minute)
	for id, t := range d.seen {
		if t.Before(cutoff) {
			delete(d.seen, id)
		}
	}
}

// --- Backoff ---

// backoff implements exponential backoff for reconnection attempts.
// Caps at maxDelay.
type backoff struct {
	attempt  int
	base     time.Duration
	maxDelay time.Duration
}

func newBackoff() *backoff {
	return &backoff{
		base:     1 * time.Second,
		maxDelay: 30 * time.Second,
	}
}

// next returns the next backoff delay and increments the attempt counter.
// Sequence: 1s, 2s, 4s, 8s, 16s, 30s, 30s, ...
func (b *backoff) next() time.Duration {
	// Cap the shift to avoid overflow. 30 shifts on a nanosecond base
	// already exceeds any reasonable delay, and we clamp to maxDelay
	// anyway.
	shift := b.attempt
	if shift > 30 {
		shift = 30
	}
	delay := b.base << uint(shift) //nolint:gosec // capped above
	if delay > b.maxDelay || delay < 0 {
		delay = b.maxDelay
	}
	b.attempt++
	return delay
}

// reset clears the attempt counter after a successful long-running session.
func (b *backoff) reset() {
	b.attempt = 0
}

// clampInt32 clamps an int to the int32 range. Docker exit codes are
// 0-255 so this is purely defensive.
func clampInt32(v int) int32 {
	const (
		maxInt32 = 1<<31 - 1
		minInt32 = -1 << 31
	)
	if v > maxInt32 {
		return maxInt32
	}
	if v < minInt32 {
		return minInt32
	}
	return int32(v) //nolint:gosec // bounds checked above
}

// Connected reports whether the agent currently has an active server connection.
func (a *Agent) Connected() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.connected
}

// ContainerCount returns the cached number of containers on the local host.
func (a *Agent) ContainerCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.containerCount
}
