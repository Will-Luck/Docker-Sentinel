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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
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
	RenameContainer(ctx context.Context, id string, newName string) error
	NetworkConnect(ctx context.Context, networkID string, containerID string) error
	PullImage(ctx context.Context, refStr string) error
	ImageDigest(ctx context.Context, imageRef string) (string, error)
	ExecContainer(ctx context.Context, id string, cmd []string, timeout int) (int, string, error)
	ContainerLogs(ctx context.Context, id string, lines int) (string, error)
	EngineID(ctx context.Context) (string, error)
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

	engineID string // Docker Engine ID for source dedup

	certErrLogged bool // suppress repeated CA mismatch guidance

	// Certificate renewal (renewal.go). credMu serialises reads and writes
	// of the credential files; renewMu guards renewal.
	now         func() time.Time // clock for renewal decisions; time.Now when nil
	credMu      sync.Mutex
	renewMu     sync.Mutex
	renewal     *pendingRenewal
	certRenewed chan struct{} // signalled once a renewed pair is on disk

	// inflight counts running command handlers, so a reconnect for a
	// renewed certificate can wait for them.
	inflight atomic.Int64

	// hubRenews is whether the hub said (in its StateAck) that it renews
	// certificates safely. Hubs before v2.17.0 do not.
	hubRenews atomic.Bool

	// hubKeepalive is whether the last hub said it accepts frequent
	// keepalive pings; the next connection is dialled to match.
	hubKeepalive atomic.Bool
	// keepaliveDialled is the keepalive interval (nanoseconds) the
	// current connection was dialled with.
	keepaliveDialled atomic.Int64

	// updating holds the names of containers being updated (beginUpdate).
	updatingMu sync.Mutex
	updating   map[string]bool

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

		now:         time.Now,
		certRenewed: make(chan struct{}, 1),
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

	// Collect Engine ID for source deduplication. Non-fatal if it fails —
	// the hub will just skip overlap detection for this agent.
	if eid, err := a.docker.EngineID(ctx); err != nil {
		a.log.Warn("failed to get Docker Engine ID", "error", err)
	} else {
		a.engineID = eid
		a.log.Info("collected Docker Engine ID", "engine_id", eid)
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

		// A planned reconnect (to use a renewed certificate, or to match
		// the server's keepalive policy): no backoff, and the agent was
		// never really offline.
		if errors.Is(err, errCertRenewed) || errors.Is(err, errKeepaliveChanged) {
			a.log.Info(err.Error())
			bo.reset()
			continue
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

		// Detect TLS certificate errors and log actionable guidance once:
		// an expired certificate on either side, or a server CA that was
		// regenerated (volume deleted, host migration) while the agent
		// still has the old CA certificate cached locally.
		if err != nil && !a.certErrLogged && isCertError(err) {
			a.certErrLogged = true
			msg, fix := certErrorGuidance(err)
			a.log.Error(msg, "fix", fix, "data_dir", a.cfg.DataDir)
		}

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
		EngineId:          a.engineID,
	})
	if err != nil {
		if status.Code(err) == codes.ResourceExhausted {
			return fmt.Errorf("report state: the container list is larger than the server accepts (%d MB): %w", cluster.MaxMessageSize>>20, err)
		}
		return fmt.Errorf("report state: %w", err)
	}
	if !ack.Accepted {
		return fmt.Errorf("state report rejected: %s", ack.Message)
	}
	a.log.Info("state report accepted", "message", ack.Message)
	a.hubRenews.Store(slices.Contains(ack.GetServerFeatures(), cluster.FeatureCertRenewal))

	// Match the keepalive interval to what this server accepts: a server
	// before v2.17.0 cuts off a client that pings every 30 seconds. If the
	// connection was dialled for the other kind of server, redial now.
	fastOK := slices.Contains(ack.GetServerFeatures(), cluster.FeatureKeepalive)
	a.hubKeepalive.Store(fastOK)
	want := keepaliveSlow
	if fastOK {
		want = keepaliveFast
	}
	if a.dialledKeepalive() != want {
		return errKeepaliveChanged
	}

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

	// A renewal signal left over from the last session is stale: this
	// session already connected with the files on disk.
	select {
	case <-a.certRenewed:
	default:
	}

	// Run heartbeat, receive and certificate renewal loops concurrently.
	// When any exits, the session is over.
	sessCtx, endSession := context.WithCancel(ctx)
	defer endSession()
	errCh := make(chan error, 3)
	go func() { errCh <- a.heartbeatLoop(sessCtx, stream) }()
	go func() { errCh <- a.receiveLoop(ctx, stream) }()
	go func() { errCh <- a.renewalLoop(sessCtx, stream) }()

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
	a.recoverStagedRenewal()

	a.credMu.Lock()
	cert, err := tls.LoadX509KeyPair(a.certPath, a.keyPath)
	a.credMu.Unlock()
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

	ka := keepaliveSlow
	if a.hubKeepalive.Load() {
		ka = keepaliveFast
	}
	conn, err := grpc.NewClient(
		a.cfg.ServerAddr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(cluster.MaxMessageSize),
			grpc.MaxCallSendMsgSize(cluster.MaxMessageSize),
		),
		// Ping the server when the connection is quiet, so a partition is
		// noticed in about 40 seconds instead of when TCP gives up. Only a
		// server that advertises it gets the fast pings.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: ka, Timeout: 10 * time.Second, PermitWithoutStream: true}),
	)
	if err != nil {
		return fmt.Errorf("dial server: %w", err)
	}

	a.keepaliveDialled.Store(int64(ka))
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

// isCertError returns true if the error indicates a TLS certificate
// problem: an expired certificate on either side, or a mismatch, typically
// caused by the server regenerating its CA after a volume deletion or host
// migration.
func isCertError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "certificate signed by unknown authority") ||
		strings.Contains(s, "certificate is not trusted") ||
		strings.Contains(s, "x509:") ||
		strings.Contains(s, "tls: failed to verify certificate") ||
		strings.Contains(s, "tls: expired certificate")
}

// certErrorGuidance describes a certificate error from isCertError and
// what to do about it.
func certErrorGuidance(err error) (msg, fix string) {
	s := err.Error()
	switch {
	case strings.Contains(s, "tls: expired certificate"):
		// The hub rejected this agent's certificate on its dates: expired,
		// or not valid yet because the hub's clock is behind.
		return "the server refuses this agent's certificate as expired or not yet valid",
			"first check that the clocks on both hosts are right. If they are, the certificate has expired: re-enrol this agent " +
				"(stop it, delete the cluster data directory, and restart with a fresh SENTINEL_ENROLL_TOKEN). " +
				"Agents renew their certificate automatically while it is still valid, so this means the agent could not reach the server for too long"
	case strings.Contains(s, "certificate has expired or is not yet valid") && strings.Contains(s, " is before "):
		// The hub's certificate starts later than this host's clock.
		return "this host's clock is behind the server's: the server's TLS certificate is not valid yet by this host's time",
			"correct the clock on this host (enable NTP); re-enrolling the agent will not help"
	case strings.Contains(s, "certificate has expired or is not yet valid"):
		// This agent rejected the hub's certificate on its dates.
		return "the server's TLS certificate has expired or is not yet valid, or the clocks on the two hosts disagree",
			"check the clock on both hosts; restarting the server gives it a fresh certificate"
	default:
		return "server CA mismatch -- the server's TLS certificate has changed since this agent enrolled",
			"re-enroll this agent: stop it, delete the cluster data directory, and restart with a fresh SENTINEL_ENROLL_TOKEN"
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

// Keepalive intervals for the agent's connection. keepaliveFast is used
// only with a server that advertises FeatureKeepalive; otherwise the agent
// stays within gRPC's default server policy. Variables so tests can
// shorten them.
var (
	keepaliveFast = 30 * time.Second
	keepaliveSlow = 5 * time.Minute
)

// errKeepaliveChanged ends a session so the agent redials with the
// keepalive interval the server accepts.
var errKeepaliveChanged = errors.New("reconnecting to match the server's keepalive policy")

// dialledKeepalive is the keepalive interval of the current connection.
func (a *Agent) dialledKeepalive() time.Duration {
	return time.Duration(a.keepaliveDialled.Load())
}
