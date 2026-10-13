package agent

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
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/server"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func eventually(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

func readPEMBlock(t *testing.T, path, typ string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != typ {
		t.Fatalf("%s: no %s PEM block", path, typ)
	}
	return block.Bytes
}

// provisionAgent writes the on-disk credentials of an agent that enrolled
// with the CA in caDir at issuedAt, as enrolment would have, and returns
// the certificate's serial.
func provisionAgent(t *testing.T, caDir, dataDir, hostID string, issuedAt time.Time) string {
	t.Helper()
	caCert, err := x509.ParseCertificate(readPEMBlock(t, filepath.Join(caDir, "ca.pem"), "CERTIFICATE"))
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	caKey, err := x509.ParseECPrivateKey(readPEMBlock(t, filepath.Join(caDir, "ca-key.pem"), "EC PRIVATE KEY"))
	if err != nil {
		t.Fatalf("parse CA key: %v", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate agent key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hostID},
		NotBefore:             issuedAt.Add(-time.Hour),
		NotAfter:              issuedAt.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("sign agent cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal agent key: %v", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(caDir, "ca.pem"))
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	files := map[string][]byte{
		"agent.pem":     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"agent-key.pem": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		"ca.pem":        caPEM,
		"host-id":       []byte(hostID),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dataDir, name), data, 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return fmt.Sprintf("%x", serial)
}

func diskCertSerial(t *testing.T, dataDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "agent.pem"))
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return ""
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", leaf.SerialNumber)
}

// renewalHub starts an in-process hub with a fresh CA and returns it with
// its address, store and CA directory.
func renewalHub(t *testing.T) (*server.Server, string, *store.Store, string) {
	t.Helper()
	caDir := t.TempDir()
	ca, err := cluster.EnsureCA(caDir)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := server.New(ca, st, events.New(), noopLogger())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	lis.Close()
	if err := srv.Start(addr); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv, addr, st, caDir
}

// An agent certificate lasts 365 days. Before it runs out, the agent must
// get a new one from the hub over its live connection, switch to it, and
// the hub must then retire the old one. Here the agent enrolled 300 days
// ago, so its certificate has 65 days left.
func TestCertRenewal_AgentRenewsBeforeExpiry(t *testing.T) {
	srv, addr, st, caDir := renewalHub(t)

	const hostID = "0123456789abcdef0123456789abcdef"
	dataDir := t.TempDir()
	enrolledAt := time.Now().Add(-300 * 24 * time.Hour)
	oldSerial := provisionAgent(t, caDir, dataDir, hostID, enrolledAt)
	oldCertPEM, _ := os.ReadFile(filepath.Join(dataDir, "agent.pem"))
	oldKeyPEM, _ := os.ReadFile(filepath.Join(dataDir, "agent-key.pem"))
	if err := srv.Registry().Register(cluster.HostInfo{
		ID: hostID, Name: "renewing-host", State: cluster.HostActive,
		CertSerial: oldSerial, EnrolledAt: enrolledAt, LastSeen: enrolledAt,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	a := New(Config{ServerAddr: addr, HostName: "renewing-host", DataDir: dataDir, Version: "test"}, newFakeHost(), noopLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	eventually(t, 30*time.Second, func() bool {
		onDisk := diskCertSerial(t, dataDir)
		hs, ok := srv.GetHost(hostID)
		return onDisk != "" && onDisk != oldSerial && ok &&
			hs.Info.CertSerial == onDisk && hs.Info.PendingCertSerial == "" && hs.Connected
	}, "agent did not renew its certificate and reconnect with it")

	newSerial := diskCertSerial(t, dataDir)
	cert, err := tls.LoadX509KeyPair(filepath.Join(dataDir, "agent.pem"), filepath.Join(dataDir, "agent-key.pem"))
	if err != nil {
		t.Fatalf("renewed certificate and key on disk do not form a pair: %v", err)
	}
	if left := time.Until(cert.Leaf.NotAfter); left < 300*24*time.Hour {
		t.Errorf("renewed certificate has %v left, want a fresh year", left.Round(time.Hour))
	}
	if revoked, _ := st.IsRevokedCert(oldSerial); !revoked {
		t.Error("old certificate still valid after the agent moved to the new one")
	}
	if revoked, _ := st.IsRevokedCert(newSerial); revoked {
		t.Error("the certificate in use was revoked")
	}
	if all, _ := st.ListRevokedCerts(); len(all) != 1 {
		t.Errorf("revoked serials = %v, want only the old one (one renewal, no repeats)", all)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "agent-renewal.pem")); !os.IsNotExist(err) {
		t.Errorf("staged renewal left behind: %v", err)
	}

	// The retired certificate no longer gets in.
	caPEM, _ := os.ReadFile(filepath.Join(dataDir, "ca.pem"))
	oldPair, err := tls.X509KeyPair(oldCertPEM, oldKeyPEM)
	if err != nil {
		t.Fatalf("old pair: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{oldPair}, RootCAs: pool, MinVersion: tls.VersionTLS13,
	})))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer rcancel()
	if _, err := proto.NewAgentServiceClient(conn).ReportState(rctx, &proto.StateReport{HostId: hostID}); err == nil {
		t.Error("the retired certificate was still accepted")
	}
}

// An expired certificate needs re-enrolment, and the agent log must say so
// rather than blame a changed CA.
func TestIsCertError_RecognisesExpiry(t *testing.T) {
	for _, msg := range []string{
		// The hub rejected this agent's expired certificate.
		`rpc error: code = Unavailable desc = connection error: desc = "error reading server preface: remote error: tls: expired certificate"`,
		// The hub presented an expired certificate.
		`rpc error: code = Unavailable desc = connection error: desc = "transport: authentication handshake failed: tls: failed to verify certificate: x509: certificate has expired or is not yet valid: current time 2027-10-14T10:11:11Z is after 2027-10-13T10:11:11Z"`,
	} {
		if !isCertError(errors.New(msg)) {
			t.Errorf("isCertError(%q) = false", msg)
		}
	}
}

func TestCertErrorGuidance(t *testing.T) {
	agentExpired, _ := certErrorGuidance(errors.New("remote error: tls: expired certificate"))
	hubExpired, _ := certErrorGuidance(errors.New("tls: failed to verify certificate: x509: certificate has expired or is not yet valid: current time"))
	mismatch, _ := certErrorGuidance(errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"))
	if agentExpired == hubExpired || agentExpired == mismatch || hubExpired == mismatch {
		t.Fatalf("expected three different explanations, got %q, %q, %q", agentExpired, hubExpired, mismatch)
	}
	if !strings.Contains(agentExpired, "agent's certificate has expired") {
		t.Errorf("agent expiry explained as %q", agentExpired)
	}
	if !strings.Contains(hubExpired, "server's TLS certificate has expired") {
		t.Errorf("hub expiry explained as %q", hubExpired)
	}
}

func TestRenewalDue(t *testing.T) {
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	leaf := &x509.Certificate{NotBefore: issued, NotAfter: issued.Add(365 * 24 * time.Hour)}
	for _, tt := range []struct {
		days int
		want bool
	}{{0, false}, {200, false}, {250, true}, {364, true}} {
		if got := renewalDue(leaf, issued.Add(time.Duration(tt.days)*24*time.Hour)); got != tt.want {
			t.Errorf("renewalDue at day %d = %v, want %v", tt.days, got, tt.want)
		}
	}
}

// renewalAgent is an agent whose fresh certificate (from caDir's CA) is on
// disk, with no hub attached.
func renewalAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	caDir := t.TempDir()
	if _, err := cluster.EnsureCA(caDir); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	dataDir := t.TempDir()
	provisionAgent(t, caDir, dataDir, "host-a", time.Now())
	a := New(Config{DataDir: dataDir}, newFakeHost(), noopLogger())
	a.hostID = "host-a"
	a.hubRenews.Store(true)
	return a, caDir
}

func renewalCSRs(st *recStream) int {
	n := 0
	for _, m := range st.messages() {
		if m.GetCertRenewal() != nil {
			n++
		}
	}
	return n
}

// With the clock moved 300 days on, a fresh 365-day certificate is due.
// The agent asks once, waits for the reply, and asks again with a new key
// only if no reply comes.
func TestRequestRenewalIfDue_ShiftedClock(t *testing.T) {
	a, _ := renewalAgent(t)
	st := &recStream{}

	if err := a.requestRenewalIfDue(st); err != nil || renewalCSRs(st) != 0 {
		t.Fatalf("fresh certificate: %d CSRs sent (err %v), want none", renewalCSRs(st), err)
	}

	shift := 300 * 24 * time.Hour
	a.now = func() time.Time { return time.Now().Add(shift) }
	if err := a.requestRenewalIfDue(st); err != nil || renewalCSRs(st) != 1 {
		t.Fatalf("300 days on: %d CSRs sent (err %v), want 1", renewalCSRs(st), err)
	}
	first := a.renewal.keyPEM

	if err := a.requestRenewalIfDue(st); err != nil || renewalCSRs(st) != 1 {
		t.Fatalf("while waiting for the reply: %d CSRs sent (err %v), want still 1", renewalCSRs(st), err)
	}

	shift += renewalRetryAfter + time.Minute
	if err := a.requestRenewalIfDue(st); err != nil || renewalCSRs(st) != 2 {
		t.Fatalf("after the retry window: %d CSRs sent (err %v), want 2", renewalCSRs(st), err)
	}
	if string(a.renewal.keyPEM) == string(first) {
		t.Error("retry reused the first request's key")
	}
}

// signForAgent has the CA in caDir sign csr as the hub would.
func signForAgent(t *testing.T, caDir string, csrDER []byte, hostID string) []byte {
	t.Helper()
	ca, err := cluster.EnsureCA(caDir)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	certPEM, _, err := ca.SignCSR(csrDER, hostID)
	if err != nil {
		t.Fatalf("SignCSR: %v", err)
	}
	return certPEM
}

func lastCSR(t *testing.T, st *recStream) []byte {
	t.Helper()
	msgs := st.messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if r := msgs[i].GetCertRenewal(); r != nil {
			return r.GetCsr()
		}
	}
	t.Fatal("no CSR sent")
	return nil
}

func TestHandleCertRenewal_InstallsRequestedCert(t *testing.T) {
	a, caDir := renewalAgent(t)
	a.now = func() time.Time { return time.Now().Add(300 * 24 * time.Hour) }
	st := &recStream{}
	if err := a.requestRenewalIfDue(st); err != nil {
		t.Fatalf("requestRenewalIfDue: %v", err)
	}
	certPEM := signForAgent(t, caDir, lastCSR(t, st), "host-a")

	if err := a.handleCertRenewal(&proto.CertRenewalResponse{AgentCert: certPEM}); err != nil {
		t.Fatalf("handleCertRenewal: %v", err)
	}
	onDisk, _ := os.ReadFile(a.certPath)
	if string(onDisk) != string(certPEM) {
		t.Error("renewed certificate not written")
	}
	if _, err := tls.LoadX509KeyPair(a.certPath, a.keyPath); err != nil {
		t.Errorf("certificate and key on disk do not match: %v", err)
	}
	select {
	case <-a.certRenewed:
	default:
		t.Error("session not told to reconnect with the renewed certificate")
	}
}

// Agents before v2.17.0 wrote any renewal reply over their certificate,
// leaving a certificate that did not match their key.
func TestHandleCertRenewal_IgnoresUnrequestedOrMismatchedCert(t *testing.T) {
	a, caDir := renewalAgent(t)
	before, _ := os.ReadFile(a.certPath)

	otherCSR, _ := newCSRForTest(t)
	stray := signForAgent(t, caDir, otherCSR, "host-a")
	if err := a.handleCertRenewal(&proto.CertRenewalResponse{AgentCert: stray}); err == nil {
		t.Error("accepted a certificate the agent never asked for")
	}

	// Now with a request outstanding, a certificate for another key.
	a.now = func() time.Time { return time.Now().Add(300 * 24 * time.Hour) }
	if err := a.requestRenewalIfDue(&recStream{}); err != nil {
		t.Fatalf("requestRenewalIfDue: %v", err)
	}
	if err := a.handleCertRenewal(&proto.CertRenewalResponse{AgentCert: stray}); err == nil {
		t.Error("accepted a certificate for a key the agent did not make")
	}

	if after, _ := os.ReadFile(a.certPath); string(after) != string(before) {
		t.Error("certificate on disk changed")
	}
	if _, err := tls.LoadX509KeyPair(a.certPath, a.keyPath); err != nil {
		t.Errorf("certificate and key on disk no longer match: %v", err)
	}
}

func newCSRForTest(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "x"}}, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}
	return csrDER, key
}

// The agent stopped after staging a renewed pair but before both live files
// were replaced. The next connect must finish the job.
func TestRecoverStagedRenewal_FinishesInterruptedCommit(t *testing.T) {
	a, caDir := renewalAgent(t)
	csrDER, key := newCSRForTest(t)
	certPEM := signForAgent(t, caDir, csrDER, "host-a")
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// Staged, and the key already swapped, the certificate not yet.
	if err := os.WriteFile(a.renewalPath(), append(append([]byte{}, certPEM...), keyPEM...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := tls.LoadX509KeyPair(a.certPath, a.keyPath); err == nil {
		t.Fatal("test setup: live pair should be mismatched")
	}

	a.recoverStagedRenewal()

	if _, err := tls.LoadX509KeyPair(a.certPath, a.keyPath); err != nil {
		t.Fatalf("live pair still unusable after recovery: %v", err)
	}
	if onDisk, _ := os.ReadFile(a.certPath); string(onDisk) != string(certPEM) {
		t.Error("recovery did not install the staged certificate")
	}
	if _, err := os.Stat(a.renewalPath()); !os.IsNotExist(err) {
		t.Error("staged file left behind after recovery")
	}
}

func TestRecoverStagedRenewal_DiscardsBrokenStaging(t *testing.T) {
	a, _ := renewalAgent(t)
	before, _ := os.ReadFile(a.certPath)
	if err := os.WriteFile(a.renewalPath(), []byte("-----BEGIN CERTIFICATE-----\ntrunc"), 0600); err != nil {
		t.Fatal(err)
	}

	a.recoverStagedRenewal()

	if after, _ := os.ReadFile(a.certPath); string(after) != string(before) {
		t.Error("a broken staged file replaced the live certificate")
	}
	if _, err := os.Stat(a.renewalPath()); !os.IsNotExist(err) {
		t.Error("broken staged file not discarded")
	}
}
