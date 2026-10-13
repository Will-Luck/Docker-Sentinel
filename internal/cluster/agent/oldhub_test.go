package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// fakeHub is a bare AgentService that records what an agent sends. With no
// features it answers like a hub before v2.17.0.
type fakeHub struct {
	proto.UnimplementedAgentServiceServer
	features []string
	channel  chan struct{}
	got      chan *proto.AgentMessage
}

func (h *fakeHub) ReportState(context.Context, *proto.StateReport) (*proto.StateAck, error) {
	return &proto.StateAck{Accepted: true, Message: "state received", ServerFeatures: h.features}, nil
}

func (h *fakeHub) Channel(stream grpc.BidiStreamingServer[proto.AgentMessage, proto.ServerMessage]) error {
	select {
	case h.channel <- struct{}{}:
	default:
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return nil
		}
		select {
		case h.got <- msg:
		default:
		}
	}
}

// startFakeHub serves h with mTLS from the CA in caDir.
func startFakeHub(t *testing.T, caDir string, h *fakeHub) string {
	t.Helper()
	ca, err := cluster.EnsureCA(caDir)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := ca.IssueServerCert()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CACertPEM())
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	})))
	proto.RegisterAgentServiceServer(srv, h)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// renewalRequests runs an agent whose certificate is due for renewal
// against h and returns how many CSRs h received within a second of the
// agent's stream opening.
func renewalRequests(t *testing.T, features []string) int {
	t.Helper()
	caDir, dataDir := t.TempDir(), t.TempDir()
	h := &fakeHub{features: features, channel: make(chan struct{}, 1), got: make(chan *proto.AgentMessage, 16)}
	addr := startFakeHub(t, caDir, h)
	provisionAgent(t, caDir, dataDir, "host-old", time.Now().Add(-300*24*time.Hour))

	a := New(Config{ServerAddr: addr, HostName: "old", DataDir: dataDir, Version: "test"}, newFakeHost(), noopLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case <-h.channel:
	case <-time.After(10 * time.Second):
		t.Fatal("agent never opened its stream")
	}
	n := 0
	deadline := time.After(time.Second)
	for {
		select {
		case msg := <-h.got:
			if msg.GetCertRenewal() != nil {
				n++
			}
		case <-deadline:
			return n
		}
	}
}

// A hub before v2.17.0 revokes the old certificate the moment it signs a
// new one and may drop the reply, which would lock the agent out. The
// agent only asks a hub that says it renews safely.
func TestCertRenewal_NotRequestedFromOldHub(t *testing.T) {
	if n := renewalRequests(t, nil); n != 0 {
		t.Errorf("agent sent %d renewal requests to a hub that does not advertise cert-renewal", n)
	}
}

func TestCertRenewal_RequestedFromHubThatAdvertisesIt(t *testing.T) {
	if n := renewalRequests(t, []string{cluster.FeatureCertRenewal}); n != 1 {
		t.Errorf("agent sent %d renewal requests to a hub that advertises cert-renewal, want 1", n)
	}
}
