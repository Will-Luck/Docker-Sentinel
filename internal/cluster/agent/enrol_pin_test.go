package agent

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	clusterserver "github.com/Will-Luck/Docker-Sentinel/internal/cluster/server"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// startHub starts a real cluster server on a fresh CA and returns it with
// its address.
func startHub(t *testing.T) (*clusterserver.Server, string) {
	t.Helper()
	ca, err := cluster.EnsureCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := clusterserver.New(ca, openTestStore(t), events.New(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	lis.Close()
	if err := srv.Start(addr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	return srv, addr
}

// cluster-server-8, cluster-agent-5: enrolment trusted whatever answered at
// the server address, so anyone on the path could take the one-time token
// and become the agent's hub. The token now names its hub (the fingerprint
// of the hub's CA), and the agent checks it before it sends the token.
func TestEnroll_ChecksTheServerAgainstTheToken(t *testing.T) {
	hub, hubAddr := startHub(t)
	other, otherAddr := startHub(t)
	token, _, err := hub.GenerateEnrollToken(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, pin, ok := strings.Cut(token, "."); !ok || pin == "" {
		t.Fatalf("token %q carries no fingerprint of its hub", token)
	}

	impostor := New(Config{ServerAddr: otherAddr, EnrollToken: token, HostName: "a", DataDir: t.TempDir()}, newMockDocker(), noopLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := impostor.enroll(ctx); err == nil || !strings.Contains(err.Error(), "not the server") {
		t.Fatalf("enrolling against another server: %v, want a refusal", err)
	}
	if len(other.AllHosts()) != 0 {
		t.Error("the other server enrolled the agent")
	}

	a := New(Config{ServerAddr: hubAddr, EnrollToken: token, HostName: "a", DataDir: t.TempDir()}, newMockDocker(), noopLogger())
	if err := a.enroll(ctx); err != nil {
		t.Fatalf("enrolling against the token's hub: %v", err)
	}
	if len(hub.AllHosts()) != 1 {
		t.Errorf("hub has %d hosts, want the new agent", len(hub.AllHosts()))
	}
}

// A token without a fingerprint (made by a server before v3.0.0, or typed
// without it) still enrols, as before.
func TestEnroll_TokenWithoutFingerprintStillWorks(t *testing.T) {
	hub, addr := startHub(t)
	token, _, err := hub.GenerateEnrollToken(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secret, _, _ := strings.Cut(token, ".")
	a := New(Config{ServerAddr: addr, EnrollToken: secret, HostName: "a", DataDir: t.TempDir()}, newMockDocker(), noopLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.enroll(ctx); err != nil {
		t.Fatalf("enrolling with the bare token: %v", err)
	}
}

// cluster-agent-10: an agent whose first enrolment answer was lost (the
// connection dropped, the agent restarted) could not enrol again with the
// same token. It keeps the key of an attempt that did not finish and uses
// it again, so the hub accepts the retry and gives it the same host ID.
func TestEnroll_RetryAfterALostAnswer(t *testing.T) {
	hub, addr := startHub(t)
	token, _, err := hub.GenerateEnrollToken(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	// The first attempt reaches the hub, but its answer is lost: the agent
	// has only the key it made for it.
	key, err := loadOrCreatePendingKey(filepath.Join(dir, pendingKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "a"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	secret, _, _ := strings.Cut(token, ".")
	first, err := hub.Enroll(context.Background(), &proto.EnrollRequest{Token: secret, HostName: "a", Csr: csr})
	if err != nil {
		t.Fatal(err)
	}

	a := New(Config{ServerAddr: addr, EnrollToken: token, HostName: "a", DataDir: dir}, newMockDocker(), noopLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.enroll(ctx); err != nil {
		t.Fatalf("retried enrolment: %v", err)
	}
	if a.hostID != first.GetHostId() {
		t.Errorf("host ID %s, want the first attempt's %s", a.hostID, first.GetHostId())
	}
	if _, err := os.Stat(filepath.Join(dir, pendingKeyFile)); !os.IsNotExist(err) {
		t.Error("the pending key file is still there after enrolling")
	}
}
