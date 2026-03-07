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
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// Supported agent features advertised during heartbeat and state reports.
var supportedFeatures = []string{
	"update",
	"hooks",
	"pull",
	"list",
	"logs",
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
	ContainerLogs(ctx context.Context, id string, lines int) (string, error)
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

	// sendMu serialises writes to the bidirectional gRPC stream.
	// gRPC stream Send() is not safe for concurrent use — multiple
	// goroutines (heartbeat, command handlers) call it simultaneously.
	sendMu sync.Mutex

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
