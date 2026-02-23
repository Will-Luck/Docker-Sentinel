package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// testServer creates a real gRPC server with a real CA, BoltDB, and event bus
// on a random port. Returns the server, its address, the store, and the event bus.
// Everything is cleaned up automatically via t.Cleanup.
func testServer(t *testing.T) (*Server, string, *store.Store, *events.Bus) {
	t.Helper()

	caDir := t.TempDir()
	ca, err := cluster.EnsureCA(caDir)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	dbPath := t.TempDir() + "/test.db"
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	bus := events.New()
	log := slog.Default()

	srv := New(ca, st, bus, log)

	// Find a free port.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close() // Close so the server can bind to the same port.

	if err := srv.Start(addr); err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	return srv, addr, st, bus
}

// enrollAgent performs the full enrollment flow against a running server:
// generates an ECDSA P-256 key, creates a PKCS#10 CSR, calls Enroll via
// an insecure (no client cert) gRPC connection, and returns everything
// the agent needs to connect with mTLS.
func enrollAgent(t *testing.T, addr, token string) (hostID string, certPEM, keyPEM, caPEM []byte) {
	t.Helper()

	// Generate agent key pair.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate agent key: %v", err)
	}

	// Create CSR.
	csrTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "test-agent"},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}

	// Connect with TLS but without a client cert for enrollment. The server
	// uses VerifyClientCertIfGiven, so it accepts connections without client
	// certs. We skip server cert verification because the agent doesn't
	// know the CA yet — that's the whole point of enrollment.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	enrollTLS := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // enrollment bootstraps trust
		MinVersion:         tls.VersionTLS13,
	}
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(credentials.NewTLS(enrollTLS)),
	)
	if err != nil {
		t.Fatalf("dial for enrollment: %v", err)
	}
	defer conn.Close()

	client := proto.NewEnrollmentServiceClient(conn)
	resp, err := client.Enroll(ctx, &proto.EnrollRequest{
		Token:    token,
		HostName: "test-agent",
		Csr:      csrDER,
	})
	if err != nil {
		t.Fatalf("Enroll RPC: %v", err)
	}

	// Marshal the agent's private key to PEM.
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal agent key: %v", err)
	}
	agentKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return resp.HostId, resp.AgentCert, agentKeyPEM, resp.CaCert
}

// agentTLSConn creates a gRPC connection with mTLS using the agent's
// certificate, key, and CA cert obtained during enrollment. The server's
// cert is verified against the CA.
func agentTLSConn(t *testing.T, addr string, certPEM, keyPEM, caPEM []byte) *grpc.ClientConn {
	t.Helper()

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse agent keypair: %v", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to add CA cert to pool")
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}

	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		t.Fatalf("dial with mTLS: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return conn
}

// noCertConn creates a gRPC connection with TLS but without a client cert.
// Uses InsecureSkipVerify because the caller doesn't (yet) know the CA.
// Suitable for enrollment and for testing unauthenticated access.
func noCertConn(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()

	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // test helper for enrollment
		MinVersion:         tls.VersionTLS13,
	}
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		t.Fatalf("dial (no cert): %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// ---------------------------------------------------------------------------
// TestEnrollment verifies the happy-path enrollment flow end-to-end:
// token generation, CSR signing, cert chain validation, host registration,
// and token consumption (one-time use).
// ---------------------------------------------------------------------------

func TestEnrollment(t *testing.T) {
	srv, addr, st, _ := testServer(t)

	// Generate enrollment token.
	token, _, err := srv.GenerateEnrollToken(5 * time.Minute)
	if err != nil {
		t.Fatalf("GenerateEnrollToken: %v", err)
	}

	// Enroll.
	hostID, certPEM, _, caPEM := enrollAgent(t, addr, token)

	// 1. Response fields should be non-empty.
	if hostID == "" {
		t.Error("host_id should not be empty")
	}
	if len(certPEM) == 0 {
		t.Error("agent_cert should not be empty")
	}
	if len(caPEM) == 0 {
		t.Error("ca_cert should not be empty")
	}

	// 2. CA cert should be valid PEM containing a CA certificate.
	caBlock, _ := pem.Decode(caPEM)
	if caBlock == nil {
		t.Fatal("ca_cert is not valid PEM")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	if !caCert.IsCA {
		t.Error("CA cert should have IsCA=true")
	}

	// 3. Agent cert should chain to the CA.
	agentBlock, _ := pem.Decode(certPEM)
	if agentBlock == nil {
		t.Fatal("agent_cert is not valid PEM")
	}
	agentCert, err := x509.ParseCertificate(agentBlock.Bytes)
	if err != nil {
		t.Fatalf("parse agent cert: %v", err)
	}

	caPool := x509.NewCertPool()
	caPool.AddCert(caCert)
	if _, err := agentCert.Verify(x509.VerifyOptions{
		Roots:     caPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("agent cert does not chain to CA: %v", err)
	}

	// 4. Agent cert CN should be the assigned host ID (server overrides
	// whatever the agent put in the CSR subject).
	if agentCert.Subject.CommonName != hostID {
		t.Errorf("agent cert CN: got %q, want %q", agentCert.Subject.CommonName, hostID)
	}

	// 5. Host should appear in the store.
	hosts, err := st.ListClusterHosts()
	if err != nil {
		t.Fatalf("ListClusterHosts: %v", err)
	}
	if _, ok := hosts[hostID]; !ok {
		t.Errorf("host %s not found in store", hostID)
	}

	// 6. Token should be consumed — second enrollment with the same token fails.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := noCertConn(t, addr)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "second-agent"},
	}, key)

	client := proto.NewEnrollmentServiceClient(conn)
	_, err = client.Enroll(ctx, &proto.EnrollRequest{
		Token:    token,
		HostName: "second-agent",
		Csr:      csrDER,
	})
	if err == nil {
		t.Fatal("second enrollment with same token should fail")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestEnrollment_InvalidToken verifies that enrollment with a bogus token
// is rejected with PermissionDenied.
// ---------------------------------------------------------------------------

func TestEnrollment_InvalidToken(t *testing.T) {
	_, addr, _, _ := testServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := noCertConn(t, addr)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "bogus-agent"},
	}, key)

	client := proto.NewEnrollmentServiceClient(conn)
	_, err := client.Enroll(ctx, &proto.EnrollRequest{
		// Bogus token: 64 hex chars (32 bytes), but not generated by the server.
		Token:    "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		HostName: "bogus-agent",
		Csr:      csrDER,
	})
	if err == nil {
		t.Fatal("enrollment with bogus token should fail")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestEnrollment_ExpiredToken verifies that a token with zero expiry (already
// expired at creation time) is rejected.
// ---------------------------------------------------------------------------

func TestEnrollment_ExpiredToken(t *testing.T) {
	srv, addr, _, _ := testServer(t)

	// Generate a token with 0 expiry — ExpiresAt == time.Now() at creation.
	// After a small sleep, the token will be in the past and the check
	// time.Now().After(tok.ExpiresAt) will be true.
	token, _, err := srv.GenerateEnrollToken(0)
	if err != nil {
		t.Fatalf("GenerateEnrollToken: %v", err)
	}

	// Small sleep to guarantee the token's ExpiresAt is in the past.
	time.Sleep(time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := noCertConn(t, addr)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "expired-agent"},
	}, key)

	client := proto.NewEnrollmentServiceClient(conn)
	_, enrollErr := client.Enroll(ctx, &proto.EnrollRequest{
		Token:    token,
		HostName: "expired-agent",
		Csr:      csrDER,
	})
	if enrollErr == nil {
		t.Fatal("enrollment with expired token should fail")
	}
	if s, ok := status.FromError(enrollErr); !ok || s.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", enrollErr)
	}
}

// ---------------------------------------------------------------------------
// TestChannel_Heartbeat verifies that a heartbeat sent by the agent is
// received by the server and updates the host's LastSeen timestamp.
// ---------------------------------------------------------------------------

func TestChannel_Heartbeat(t *testing.T) {
	srv, addr, _, _ := testServer(t)

	// Enroll an agent.
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	// Connect with mTLS.
	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	client := proto.NewAgentServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Channel(ctx)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}

	// Allow the server to register the stream.
	time.Sleep(50 * time.Millisecond)

	// Record LastSeen before heartbeat.
	hs, ok := srv.Registry().Get(hostID)
	if !ok {
		t.Fatalf("host %s not in registry", hostID)
	}
	lastSeenBefore := hs.Info.LastSeen

	// Small delay so the timestamp advances.
	time.Sleep(10 * time.Millisecond)

	// Send heartbeat.
	err = stream.Send(&proto.AgentMessage{
		Payload: &proto.AgentMessage_Heartbeat{
			Heartbeat: &proto.Heartbeat{
				Timestamp:         timestamppb.Now(),
				AgentVersion:      "1.0.0-test",
				SupportedFeatures: []string{"update", "list"},
				HostId:            hostID,
			},
		},
	})
	if err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}

	// Wait for the server to process it.
	time.Sleep(100 * time.Millisecond)

	// Verify LastSeen was updated.
	hs, ok = srv.Registry().Get(hostID)
	if !ok {
		t.Fatalf("host %s not in registry after heartbeat", hostID)
	}
	if !hs.Info.LastSeen.After(lastSeenBefore) {
		t.Error("LastSeen should have been updated after heartbeat")
	}

	// Verify the host is marked as connected.
	if !hs.Connected {
		t.Error("host should be marked as connected while stream is active")
	}
}

// ---------------------------------------------------------------------------
// TestChannel_ListContainers verifies the server-initiated list containers
// request-response flow over the bidirectional stream.
// ---------------------------------------------------------------------------

func TestChannel_ListContainers(t *testing.T) {
	srv, addr, _, _ := testServer(t)

	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	client := proto.NewAgentServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Channel(ctx)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}

	// Wait for the stream to be registered server-side.
	time.Sleep(50 * time.Millisecond)

	// In a goroutine, act as the agent: read from the stream, handle the
	// ListContainersRequest by sending back a ContainerList response.
	agentDone := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				agentDone <- err
				return
			}

			// Handle ListContainersRequest.
			if msg.GetListContainers() != nil {
				err := stream.Send(&proto.AgentMessage{
					Payload: &proto.AgentMessage_ContainerList{
						ContainerList: &proto.ContainerList{
							RequestId: msg.GetRequestId(),
							Containers: []*proto.ContainerInfo{
								{
									Id:    "abc123",
									Name:  "nginx",
									Image: "nginx:1.25",
									State: "running",
								},
								{
									Id:    "def456",
									Name:  "redis",
									Image: "redis:7",
									State: "running",
								},
							},
						},
					},
				})
				if err != nil {
					agentDone <- err
					return
				}
				agentDone <- nil
				return
			}
		}
	}()

	// Send the ListContainersRequest from the server side.
	requestID := "req-list-001"
	err = srv.SendCommand(hostID, &proto.ServerMessage{
		RequestId: requestID,
		Payload: &proto.ServerMessage_ListContainers{
			ListContainers: &proto.ListContainersRequest{},
		},
	})
	if err != nil {
		t.Fatalf("SendCommand: %v", err)
	}

	// Wait for the agent goroutine to finish.
	select {
	case err := <-agentDone:
		if err != nil {
			t.Fatalf("agent handler: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for agent to respond to list containers")
	}

	// Wait for the server to process the ContainerList response.
	time.Sleep(100 * time.Millisecond)

	// Verify: registry should now have 2 containers for this host.
	hs, ok := srv.Registry().Get(hostID)
	if !ok {
		t.Fatalf("host %s not in registry", hostID)
	}
	if len(hs.Containers) != 2 {
		t.Errorf("expected 2 containers, got %d", len(hs.Containers))
	}

	// Check container details.
	found := map[string]bool{}
	for _, c := range hs.Containers {
		found[c.Name] = true
	}
	if !found["nginx"] {
		t.Error("expected container 'nginx' in registry")
	}
	if !found["redis"] {
		t.Error("expected container 'redis' in registry")
	}
}

// ---------------------------------------------------------------------------
// TestChannel_UpdateContainer verifies the server-initiated update request
// and agent's UpdateResult response flow.
// ---------------------------------------------------------------------------

func TestChannel_UpdateContainer(t *testing.T) {
	srv, addr, _, bus := testServer(t)

	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	client := proto.NewAgentServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Channel(ctx)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Subscribe to SSE events to verify the update result is published.
	evtCh, evtCancel := bus.Subscribe()
	defer evtCancel()

	// Agent goroutine: handle UpdateContainerRequest, send back UpdateResult.
	agentDone := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				agentDone <- err
				return
			}

			if msg.GetUpdateContainer() != nil {
				req := msg.GetUpdateContainer()
				err := stream.Send(&proto.AgentMessage{
					Payload: &proto.AgentMessage_UpdateResult{
						UpdateResult: &proto.UpdateResult{
							RequestId:     msg.GetRequestId(),
							ContainerName: req.ContainerName,
							OldImage:      "nginx:1.24",
							NewImage:      req.TargetImage,
							Outcome:       "success",
						},
					},
				})
				if err != nil {
					agentDone <- err
					return
				}
				agentDone <- nil
				return
			}
		}
	}()

	// Send UpdateContainerRequest from server.
	requestID := "req-update-001"
	err = srv.SendCommand(hostID, &proto.ServerMessage{
		RequestId: requestID,
		Payload: &proto.ServerMessage_UpdateContainer{
			UpdateContainer: &proto.UpdateContainerRequest{
				ContainerName: "nginx",
				TargetImage:   "nginx:1.26",
			},
		},
	})
	if err != nil {
		t.Fatalf("SendCommand: %v", err)
	}

	// Wait for agent to respond.
	select {
	case err := <-agentDone:
		if err != nil {
			t.Fatalf("agent handler: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for agent update response")
	}

	// Wait for server to process and publish the SSE event.
	time.Sleep(100 * time.Millisecond)

	// Verify: an SSE event should have been published for the update.
	select {
	case evt := <-evtCh:
		if evt.Type != events.EventContainerUpdate {
			t.Errorf("expected event type %q, got %q", events.EventContainerUpdate, evt.Type)
		}
		if evt.ContainerName != "nginx" {
			t.Errorf("expected container name 'nginx', got %q", evt.ContainerName)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for SSE event from update result")
	}
}

// ---------------------------------------------------------------------------
// TestChannel_Disconnect verifies that when the agent's stream is cancelled
// (simulating a disconnect), the server marks the host as disconnected and
// publishes an SSE event.
// ---------------------------------------------------------------------------

func TestChannel_Disconnect(t *testing.T) {
	srv, addr, _, bus := testServer(t)

	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	client := proto.NewAgentServiceClient(conn)

	// Use a cancellable context so we can simulate disconnect.
	ctx, cancel := context.WithCancel(context.Background())

	stream, err := client.Channel(ctx)
	if err != nil {
		cancel()
		t.Fatalf("Channel: %v", err)
	}

	// Wait for connection to be established.
	time.Sleep(100 * time.Millisecond)

	// Verify connected.
	hs, ok := srv.Registry().Get(hostID)
	if !ok || !hs.Connected {
		t.Fatal("host should be connected before disconnect")
	}

	// Subscribe to events before disconnecting.
	evtCh, evtCancel := bus.Subscribe()
	defer evtCancel()

	// Simulate disconnect by cancelling the stream context.
	cancel()
	// Also close the send side to trigger server-side recv error.
	_ = stream.CloseSend()

	// Wait for the server to detect the disconnect and clean up.
	time.Sleep(300 * time.Millisecond)

	// Verify: host marked as disconnected.
	hs, ok = srv.Registry().Get(hostID)
	if !ok {
		t.Fatal("host should still exist in registry after disconnect")
	}
	if hs.Connected {
		t.Error("host should be marked as disconnected after stream cancel")
	}

	// Verify: disconnect SSE event published.
	found := false
	timeout := time.After(2 * time.Second)
	for !found {
		select {
		case evt := <-evtCh:
			if evt.Type == events.EventClusterHost && evt.HostName == hostID {
				found = true
			}
		case <-timeout:
			t.Error("timeout waiting for disconnect SSE event")
			found = true // break the loop
		}
	}
}

// ---------------------------------------------------------------------------
// TestChannel_Reconnect verifies that after disconnect, a reconnecting agent
// re-establishes the stream and the host is marked connected again.
// ---------------------------------------------------------------------------

func TestChannel_Reconnect(t *testing.T) {
	srv, addr, _, _ := testServer(t)

	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	client := proto.NewAgentServiceClient(conn)

	// First connection.
	ctx1, cancel1 := context.WithCancel(context.Background())
	stream1, err := client.Channel(ctx1)
	if err != nil {
		cancel1()
		t.Fatalf("Channel (first): %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Verify connected.
	hs, _ := srv.Registry().Get(hostID)
	if !hs.Connected {
		t.Fatal("host should be connected after first channel open")
	}

	// Disconnect.
	cancel1()
	_ = stream1.CloseSend()
	time.Sleep(300 * time.Millisecond)

	// Verify disconnected.
	hs, _ = srv.Registry().Get(hostID)
	if hs.Connected {
		t.Fatal("host should be disconnected after cancel")
	}

	// Reconnect with the same cert (new stream, same connection).
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	stream2, err := client.Channel(ctx2)
	if err != nil {
		t.Fatalf("Channel (reconnect): %v", err)
	}

	// Send a heartbeat on the new stream to confirm it works.
	err = stream2.Send(&proto.AgentMessage{
		Payload: &proto.AgentMessage_Heartbeat{
			Heartbeat: &proto.Heartbeat{
				Timestamp:    timestamppb.Now(),
				AgentVersion: "1.0.0-test",
				HostId:       hostID,
			},
		},
	})
	if err != nil {
		t.Fatalf("send heartbeat on reconnect: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Verify reconnected.
	hs, ok := srv.Registry().Get(hostID)
	if !ok {
		t.Fatal("host not in registry after reconnect")
	}
	if !hs.Connected {
		t.Error("host should be marked as connected after reconnecting")
	}
}

// ---------------------------------------------------------------------------
// TestReportState verifies the unary ReportState RPC: the agent sends a
// full container state snapshot and the server stores it in the registry.
// ---------------------------------------------------------------------------

func TestReportState(t *testing.T) {
	srv, addr, _, _ := testServer(t)

	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	client := proto.NewAgentServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ack, err := client.ReportState(ctx, &proto.StateReport{
		HostId: hostID,
		Containers: []*proto.ContainerInfo{
			{
				Id:    "c1",
				Name:  "postgres",
				Image: "postgres:16",
				State: "running",
			},
			{
				Id:    "c2",
				Name:  "redis",
				Image: "redis:7",
				State: "running",
			},
			{
				Id:    "c3",
				Name:  "nginx",
				Image: "nginx:1.25",
				State: "stopped",
			},
		},
		Timestamp:         timestamppb.Now(),
		AgentVersion:      "1.0.0-test",
		SupportedFeatures: []string{"update", "list"},
	})
	if err != nil {
		t.Fatalf("ReportState: %v", err)
	}

	// Verify: StateAck.Accepted should be true.
	if !ack.Accepted {
		t.Errorf("expected Accepted=true, got false (message: %s)", ack.Message)
	}

	// Verify: registry should have the reported containers.
	hs, ok := srv.Registry().Get(hostID)
	if !ok {
		t.Fatalf("host %s not in registry", hostID)
	}
	if len(hs.Containers) != 3 {
		t.Errorf("expected 3 containers, got %d", len(hs.Containers))
	}

	found := map[string]bool{}
	for _, c := range hs.Containers {
		found[c.Name] = true
	}
	for _, name := range []string{"postgres", "redis", "nginx"} {
		if !found[name] {
			t.Errorf("expected container %q in registry", name)
		}
	}
}

// ---------------------------------------------------------------------------
// TestDuplicateRequestID verifies that sending two UpdateResults with the
// same request_id does not cause errors — the server should handle it
// gracefully (log but not crash).
// ---------------------------------------------------------------------------

func TestDuplicateRequestID(t *testing.T) {
	srv, addr, _, _ := testServer(t)

	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	client := proto.NewAgentServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Channel(ctx)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// Send the same request_id in two UpdateResult messages.
	for i := 0; i < 2; i++ {
		err := stream.Send(&proto.AgentMessage{
			Payload: &proto.AgentMessage_UpdateResult{
				UpdateResult: &proto.UpdateResult{
					RequestId:     "dup-req-001",
					ContainerName: "nginx",
					Outcome:       "success",
				},
			},
		})
		if err != nil {
			t.Fatalf("send UpdateResult #%d: %v", i, err)
		}
	}

	// If we get here without a crash or stream error, the server handled
	// the duplicates gracefully. Verify the stream is still active by
	// sending a heartbeat.
	time.Sleep(100 * time.Millisecond)
	err = stream.Send(&proto.AgentMessage{
		Payload: &proto.AgentMessage_Heartbeat{
			Heartbeat: &proto.Heartbeat{
				Timestamp:    timestamppb.Now(),
				AgentVersion: "1.0.0-test",
				HostId:       hostID,
			},
		},
	})
	if err != nil {
		t.Errorf("stream should still be active after duplicate request IDs, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCertRevocation verifies that after revoking an agent's certificate
// serial, the agent's attempt to open a Channel is rejected. The server
// uses a CRL callback in the TLS config that checks the store.
// ---------------------------------------------------------------------------

func TestCertRevocation(t *testing.T) {
	srv, addr, st, _ := testServer(t)

	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	// Look up the agent's cert serial from the registry so we can revoke it.
	hs, ok := srv.Registry().Get(hostID)
	if !ok {
		t.Fatalf("host %s not in registry", hostID)
	}
	certSerial := hs.Info.CertSerial
	if certSerial == "" {
		// Fall back to parsing the cert directly.
		block, _ := pem.Decode(certPEM)
		cert, _ := x509.ParseCertificate(block.Bytes)
		certSerial = fmt.Sprintf("%x", cert.SerialNumber)
	}

	// Revoke the cert.
	if err := st.AddRevokedCert(certSerial); err != nil {
		t.Fatalf("AddRevokedCert: %v", err)
	}

	// Try to open a Channel with the revoked cert.
	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	client := proto.NewAgentServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The TLS-level CRL check happens during handshake. If that passes
	// (race condition with connection reuse), the server's Channel handler
	// also checks CRL explicitly. Either way, the call should fail.
	stream, err := client.Channel(ctx)
	if err != nil {
		// TLS handshake rejected the revoked cert — this is the expected
		// path when the connection is freshly established.
		return
	}

	// If the stream was opened (e.g. connection reuse bypassed TLS check),
	// try sending a message. The server's Channel handler checks CRL on entry
	// and should close the stream with PermissionDenied.
	err = stream.Send(&proto.AgentMessage{
		Payload: &proto.AgentMessage_Heartbeat{
			Heartbeat: &proto.Heartbeat{
				Timestamp: timestamppb.Now(),
				HostId:    hostID,
			},
		},
	})
	// After sending, try to receive — the server should have closed the stream.
	if err == nil {
		_, err = stream.Recv()
	}

	if err == nil {
		t.Fatal("Channel should fail with a revoked certificate")
	}

	// Accept either a TLS error or a gRPC PermissionDenied/Unavailable.
	if s, ok := status.FromError(err); ok {
		code := s.Code()
		if code != codes.PermissionDenied && code != codes.Unavailable && code != codes.Unauthenticated {
			t.Errorf("expected PermissionDenied/Unavailable/Unauthenticated, got %v: %v", code, err)
		}
	}
	// If it's not a gRPC status error (raw TLS error), that's also acceptable.
}

// ---------------------------------------------------------------------------
// TestChannel_NoClientCert verifies that attempting to open a Channel
// without mTLS (insecure credentials) fails with Unauthenticated.
// ---------------------------------------------------------------------------

func TestChannel_NoClientCert(t *testing.T) {
	_, addr, _, _ := testServer(t)

	conn := noCertConn(t, addr)
	client := proto.NewAgentServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.Channel(ctx)
	if err != nil {
		// Connection-level failure (TLS) — acceptable.
		return
	}

	// If the stream opens, try to receive. The server's Channel handler
	// calls extractHostID which requires a client cert, so it should
	// return Unauthenticated.
	err = stream.Send(&proto.AgentMessage{
		Payload: &proto.AgentMessage_Heartbeat{
			Heartbeat: &proto.Heartbeat{Timestamp: timestamppb.Now()},
		},
	})
	if err == nil {
		_, err = stream.Recv()
	}

	if err == nil {
		t.Fatal("Channel without client cert should fail")
	}
}

// ---------------------------------------------------------------------------
// TestReportState_NoClientCert verifies that ReportState without mTLS
// is rejected.
// ---------------------------------------------------------------------------

func TestReportState_NoClientCert(t *testing.T) {
	_, addr, _, _ := testServer(t)

	conn := noCertConn(t, addr)
	client := proto.NewAgentServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.ReportState(ctx, &proto.StateReport{
		HostId: "bogus-host",
		Containers: []*proto.ContainerInfo{
			{Id: "c1", Name: "test", Image: "test:latest", State: "running"},
		},
		Timestamp: timestamppb.Now(),
	})
	if err == nil {
		t.Fatal("ReportState without client cert should fail")
	}
}

// ---------------------------------------------------------------------------
// TestSendCommand_DisconnectedAgent verifies that sending a command to a
// host that is not currently connected returns an error.
// ---------------------------------------------------------------------------

func TestSendCommand_DisconnectedAgent(t *testing.T) {
	srv, _, _, _ := testServer(t)

	err := srv.SendCommand("nonexistent-host", &proto.ServerMessage{
		RequestId: "req-001",
		Payload: &proto.ServerMessage_ListContainers{
			ListContainers: &proto.ListContainersRequest{},
		},
	})
	if err == nil {
		t.Fatal("SendCommand to disconnected host should return error")
	}
}

// ---------------------------------------------------------------------------
// TestHandleCertRenewal_NoDataRace verifies that concurrent cert renewal and
// heartbeat processing do not produce a data race on HostState fields.
// Run with: go test -race ./internal/cluster/server/ -run TestHandleCertRenewal_NoDataRace
// ---------------------------------------------------------------------------

func TestHandleCertRenewal_NoDataRace(t *testing.T) {
	srv, addr, _, _ := testServer(t)

	token, _, err := srv.GenerateEnrollToken(5 * time.Minute)
	if err != nil {
		t.Fatalf("GenerateEnrollToken: %v", err)
	}

	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	client := proto.NewAgentServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := client.Channel(ctx)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}

	// Wait for the server to register the stream.
	time.Sleep(50 * time.Millisecond)

	// Agent goroutine: drain the stream so the server's send goroutine
	// doesn't block. Discard all messages — we only care about races.
	go func() {
		for {
			_, err := stream.Recv()
			if err != nil {
				return
			}
		}
	}()

	const iters = 50

	// Goroutine 1: repeatedly fire cert renewal CSRs at the server.
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		for i := 0; i < iters; i++ {
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				return
			}
			csrDER, err := x509.CreateCertificateRequest(rand.Reader,
				&x509.CertificateRequest{Subject: pkix.Name{CommonName: hostID}},
				key,
			)
			if err != nil {
				return
			}
			_ = stream.Send(&proto.AgentMessage{
				Payload: &proto.AgentMessage_CertRenewal{
					CertRenewal: &proto.CertRenewalCSR{Csr: csrDER},
				},
			})
			time.Sleep(time.Millisecond)
		}
	}()

	// Goroutine 2: concurrently call UpdateLastSeen, which takes a write lock
	// and mutates hs.Info.LastSeen — the field also touched by the old buggy
	// handleCertRenewal after the lock was released.
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		for i := 0; i < iters; i++ {
			_ = srv.Registry().UpdateLastSeen(hostID, time.Now())
			time.Sleep(time.Millisecond)
		}
	}()

	<-renewDone
	<-heartbeatDone

	// Wait for any in-flight renewals to be processed.
	time.Sleep(200 * time.Millisecond)

	// Verify the host is still in the registry and has a cert serial.
	hs, ok := srv.Registry().Get(hostID)
	if !ok {
		t.Fatalf("host %s not in registry after concurrent renewals", hostID)
	}
	if hs.Info.CertSerial == "" {
		t.Error("host should have a cert serial after renewal")
	}
}

// ---------------------------------------------------------------------------
// TestMultipleAgents verifies that multiple agents can enroll and connect
// concurrently without interfering with each other.
// ---------------------------------------------------------------------------

func TestMultipleAgents(t *testing.T) {
	srv, addr, _, _ := testServer(t)

	const numAgents = 3
	hostIDs := make([]string, numAgents)
	streams := make([]proto.AgentService_ChannelClient, numAgents)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Enroll and connect each agent.
	for i := 0; i < numAgents; i++ {
		token, _, err := srv.GenerateEnrollToken(5 * time.Minute)
		if err != nil {
			t.Fatalf("GenerateEnrollToken #%d: %v", i, err)
		}

		hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
		hostIDs[i] = hostID

		conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
		client := proto.NewAgentServiceClient(conn)

		stream, err := client.Channel(ctx)
		if err != nil {
			t.Fatalf("Channel #%d: %v", i, err)
		}
		streams[i] = stream
	}

	time.Sleep(100 * time.Millisecond)

	// Verify: all agents should be connected.
	connected := srv.ConnectedHosts()
	if len(connected) != numAgents {
		t.Errorf("expected %d connected hosts, got %d", numAgents, len(connected))
	}

	// Verify: each host is in the registry and connected.
	for i, id := range hostIDs {
		hs, ok := srv.Registry().Get(id)
		if !ok {
			t.Errorf("agent #%d (host %s) not in registry", i, id)
			continue
		}
		if !hs.Connected {
			t.Errorf("agent #%d (host %s) should be connected", i, id)
		}
	}

	// Verify: host IDs are all unique.
	seen := make(map[string]bool)
	for _, id := range hostIDs {
		if seen[id] {
			t.Errorf("duplicate host ID: %s", id)
		}
		seen[id] = true
	}
}

// ---------------------------------------------------------------------------
// TestRegisterPending_AllowsConcurrentPerHost verifies that multiple requests
// to the same host succeed when they use different request IDs.
// ---------------------------------------------------------------------------

func TestRegisterPending_AllowsConcurrentPerHost(t *testing.T) {
	s := &Server{
		pending: make(map[string]chan *proto.AgentMessage),
	}

	ch1, err := s.registerPending("h1", "req1")
	if err != nil {
		t.Fatalf("first registerPending: unexpected error: %v", err)
	}
	if ch1 == nil {
		t.Fatal("first registerPending: expected non-nil channel")
	}

	ch2, err := s.registerPending("h1", "req2")
	if err != nil {
		t.Fatalf("second registerPending: unexpected error: %v", err)
	}
	if ch2 == nil {
		t.Fatal("second registerPending: expected non-nil channel")
	}

	// Same host + same request ID should fail.
	_, err = s.registerPending("h1", "req1")
	if err == nil {
		t.Fatal("duplicate registerPending: expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestVerifyCRL_FailsClosed verifies that verifyCRL returns an error (fails
// closed) when the store's IsRevokedCert returns an error.
// ---------------------------------------------------------------------------

// errStore is a minimal ClusterStore stub whose IsRevokedCert always errors.
type errStore struct{ err error }

func (e *errStore) SaveClusterHost(string, []byte) error         { return nil }
func (e *errStore) GetClusterHost(string) ([]byte, error)        { return nil, nil }
func (e *errStore) ListClusterHosts() (map[string][]byte, error) { return nil, nil }
func (e *errStore) DeleteClusterHost(string) error               { return nil }
func (e *errStore) SaveEnrollToken(string, []byte) error         { return nil }
func (e *errStore) GetEnrollToken(string) ([]byte, error)        { return nil, nil }
func (e *errStore) DeleteEnrollToken(string) error               { return nil }
func (e *errStore) AddRevokedCert(string) error                  { return nil }
func (e *errStore) IsRevokedCert(string) (bool, error)           { return false, e.err }
func (e *errStore) ListRevokedCerts() (map[string]string, error) { return nil, nil }

func TestVerifyCRL_FailsClosed(t *testing.T) {
	storeErr := errors.New("store unavailable")
	s := &Server{
		store: &errStore{err: storeErr},
		log:   slog.Default(),
	}

	// Generate a self-signed cert to use as the raw DER bytes.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-agent"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	gotErr := s.verifyCRL([][]byte{certDER}, nil)
	if gotErr == nil {
		t.Fatal("verifyCRL should return an error when the store fails, but got nil")
	}
}
