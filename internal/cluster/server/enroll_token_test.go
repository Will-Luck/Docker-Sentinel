package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

func testCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "agent"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return csr
}

// cluster-server-9: the one-time token was checked and consumed in two
// separate steps, so agents enrolling at the same moment could all use
// it. Exactly one may.
func TestEnroll_TokenWorksOnceUnderConcurrency(t *testing.T) {
	srv, _, _, _ := testServer(t)
	token, _, err := srv.GenerateEnrollToken(5 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for range n {
		csr := testCSR(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := srv.Enroll(context.Background(), &proto.EnrollRequest{Token: token, HostName: "agent", Csr: csr}); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Errorf("%d enrolments succeeded with one token, want 1", ok)
	}
}

// live-24: a second use of a token was refused without a word in the
// server's log. It now says so, naming the token and the host.
func TestEnroll_TokenReuseIsLogged(t *testing.T) {
	srv, _, _, _ := testServer(t)
	var buf bytes.Buffer
	var bmu sync.Mutex
	srv.log = slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &bmu}, nil))
	token, id, err := srv.GenerateEnrollToken(5 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Enroll(context.Background(), &proto.EnrollRequest{Token: token, HostName: "first", Csr: testCSR(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Enroll(context.Background(), &proto.EnrollRequest{Token: token, HostName: "second", Csr: testCSR(t)}); err == nil {
		t.Fatal("the used token enrolled a second agent")
	}
	bmu.Lock()
	out := buf.String()
	bmu.Unlock()
	if !strings.Contains(out, "already used") || !strings.Contains(out, id) || !strings.Contains(out, "second") {
		t.Errorf("log = %q, want a warning that token %s was used again by second", out, id)
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// cluster-server-11 and the re-smoke: the agent's version reached the hub
// only with its first heartbeat, so an auto-update check run just after a
// hub restart saw no versions, and the host's version was never stored.
// The state report carries it and it is kept with the host.
func TestReportState_StoresTheAgentVersion(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := proto.NewAgentServiceClient(conn).ReportState(ctx, &proto.StateReport{HostId: hostID, AgentVersion: "2.16.0"}); err != nil {
		t.Fatal(err)
	}
	if hs, _ := srv.Registry().Get(hostID); hs.Info.AgentVersion != "2.16.0" {
		t.Errorf("host's agent version = %q after its state report, want 2.16.0", hs.Info.AgentVersion)
	}
	_, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	t.Cleanup(closeConn)
	if v := srv.AgentVersions()[hostID]; v != "2.16.0" {
		t.Errorf("AgentVersions[%s] = %q before its first heartbeat, want 2.16.0", hostID, v)
	}
}

// live-10: an agent restarted with a new SENTINEL_HOST_NAME kept its old
// name on the hub. The name in its state report is taken.
func TestReportState_TakesTheAgentsNewName(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := proto.NewAgentServiceClient(conn)
	if _, err := client.ReportState(ctx, &proto.StateReport{HostId: hostID, HostName: "renamed"}); err != nil {
		t.Fatal(err)
	}
	if hs, _ := srv.Registry().Get(hostID); hs.Info.Name != "renamed" {
		t.Errorf("host name = %q, want the agent's new name", hs.Info.Name)
	}
	// An agent before v3.0.0 sends no name: the name stays.
	if _, err := client.ReportState(ctx, &proto.StateReport{HostId: hostID}); err != nil {
		t.Fatal(err)
	}
	if hs, _ := srv.Registry().Get(hostID); hs.Info.Name != "renamed" {
		t.Errorf("host name = %q after a report without one, want it kept", hs.Info.Name)
	}
}
