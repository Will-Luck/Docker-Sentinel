package cluster

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCA_CreatesNewCA(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA failed: %v", err)
	}

	// CA cert file should exist.
	certPath := filepath.Join(dir, "ca.pem")
	if _, err := os.Stat(certPath); err != nil {
		t.Fatalf("ca.pem not found: %v", err)
	}

	// CA key file should exist with restricted permissions.
	keyPath := filepath.Join(dir, "ca-key.pem")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("ca-key.pem not found: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("ca-key.pem permissions: got %o, want 0600", perm)
	}

	// Validate CA certificate properties.
	if !ca.cert.IsCA {
		t.Error("CA cert should have IsCA=true")
	}
	if ca.cert.Subject.CommonName != "Docker-Sentinel CA" {
		t.Errorf("CA CN: got %q, want %q", ca.cert.Subject.CommonName, "Docker-Sentinel CA")
	}
	if ca.cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("CA cert should have KeyUsageCertSign")
	}
	if ca.cert.KeyUsage&x509.KeyUsageCRLSign == 0 {
		t.Error("CA cert should have KeyUsageCRLSign")
	}
	if ca.cert.MaxPathLen != 0 || !ca.cert.MaxPathLenZero {
		t.Error("CA cert should be leaf-only (MaxPathLen=0, MaxPathLenZero=true)")
	}

	// Should be ECDSA P-256.
	pub, ok := ca.cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatal("CA public key is not ECDSA")
	}
	if pub.Curve != elliptic.P256() {
		t.Error("CA key should use P-256 curve")
	}
}

func TestEnsureCA_LoadsExisting(t *testing.T) {
	dir := t.TempDir()

	// Create CA.
	ca1, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("first EnsureCA failed: %v", err)
	}

	// Load same CA from disk.
	ca2, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("second EnsureCA failed: %v", err)
	}

	// Serial numbers should match — it's the same cert loaded twice.
	if ca1.cert.SerialNumber.Cmp(ca2.cert.SerialNumber) != 0 {
		t.Error("reloaded CA should have the same serial number")
	}
	if ca1.cert.Subject.CommonName != ca2.cert.Subject.CommonName {
		t.Error("reloaded CA should have the same CN")
	}
}

func TestEnsureCA_RegeneratesCorruptFiles(t *testing.T) {
	dir := t.TempDir()

	// Create CA, then corrupt the key file.
	_, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca-key.pem"), []byte("garbage"), 0600); err != nil {
		t.Fatalf("corrupt key: %v", err)
	}

	// Should regenerate without error.
	ca, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA after corruption failed: %v", err)
	}
	if !ca.cert.IsCA {
		t.Error("regenerated CA cert should have IsCA=true")
	}
}

func TestIssueServerCert(t *testing.T) {
	ca := mustCA(t)

	certPEM, keyPEM, err := ca.IssueServerCert()
	if err != nil {
		t.Fatalf("IssueServerCert failed: %v", err)
	}

	// Parse the cert back.
	cert := mustParseCertPEM(t, certPEM)

	// CN should be "sentinel-server".
	if cert.Subject.CommonName != "sentinel-server" {
		t.Errorf("server cert CN: got %q, want %q", cert.Subject.CommonName, "sentinel-server")
	}

	// Should have both server and client auth.
	hasServer, hasClient := false, false
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			hasServer = true
		}
		if u == x509.ExtKeyUsageClientAuth {
			hasClient = true
		}
	}
	if !hasServer {
		t.Error("server cert should have ExtKeyUsageServerAuth")
	}
	if !hasClient {
		t.Error("server cert should have ExtKeyUsageClientAuth")
	}

	// Should include localhost in DNS SANs.
	foundLocalhost := false
	for _, name := range cert.DNSNames {
		if name == "localhost" {
			foundLocalhost = true
			break
		}
	}
	if !foundLocalhost {
		t.Error("server cert should include 'localhost' in DNS SANs")
	}

	// Should include loopback IP.
	foundLoopback := false
	for _, ip := range cert.IPAddresses {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			foundLoopback = true
			break
		}
	}
	if !foundLoopback {
		t.Error("server cert should include 127.0.0.1 in IP SANs")
	}

	// Key PEM should parse as valid ECDSA.
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		t.Fatal("server key PEM: no PEM block")
	}
	if _, err := x509.ParseECPrivateKey(block.Bytes); err != nil {
		t.Fatalf("server key PEM: parse failed: %v", err)
	}

	// Cert should chain to CA.
	verifyCertChain(t, ca, cert)
}

func TestIssueCert_AgentIsClientOnly(t *testing.T) {
	ca := mustCA(t)

	// Generate a key as if the agent did.
	agentKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate agent key: %v", err)
	}

	certPEM, err := ca.IssueCert("agent-host-1", &agentKey.PublicKey, false)
	if err != nil {
		t.Fatalf("IssueCert (agent) failed: %v", err)
	}

	cert := mustParseCertPEM(t, certPEM)

	if cert.Subject.CommonName != "agent-host-1" {
		t.Errorf("agent cert CN: got %q, want %q", cert.Subject.CommonName, "agent-host-1")
	}

	// Agent cert: client auth only, no server auth.
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			t.Error("agent cert should NOT have ExtKeyUsageServerAuth")
		}
	}
	hasClient := false
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			hasClient = true
		}
	}
	if !hasClient {
		t.Error("agent cert should have ExtKeyUsageClientAuth")
	}

	verifyCertChain(t, ca, cert)
}

func TestIssueCert_ServerHasBothUsages(t *testing.T) {
	ca := mustCA(t)

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	certPEM, err := ca.IssueCert("sentinel-server", &key.PublicKey, true)
	if err != nil {
		t.Fatalf("IssueCert (server) failed: %v", err)
	}

	cert := mustParseCertPEM(t, certPEM)

	hasServer, hasClient := false, false
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			hasServer = true
		}
		if u == x509.ExtKeyUsageClientAuth {
			hasClient = true
		}
	}
	if !hasServer || !hasClient {
		t.Errorf("server cert usages: ServerAuth=%v ClientAuth=%v, want both true", hasServer, hasClient)
	}
}

func TestSignCSR(t *testing.T) {
	ca := mustCA(t)

	// Simulate agent: generate key + CSR.
	agentKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate agent key: %v", err)
	}
	csrTmpl := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "agent-self-reported-name"},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTmpl, agentKey)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}

	certPEM, serial, err := ca.SignCSR(csrDER, "host-abc-123")
	if err != nil {
		t.Fatalf("SignCSR failed: %v", err)
	}

	cert := mustParseCertPEM(t, certPEM)

	// CN should be the hostID the server assigned, not what the agent requested.
	if cert.Subject.CommonName != "host-abc-123" {
		t.Errorf("signed cert CN: got %q, want %q", cert.Subject.CommonName, "host-abc-123")
	}

	// Should have client auth only (it's an agent cert).
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			t.Error("CSR-signed cert should NOT have ExtKeyUsageServerAuth")
		}
	}

	// Serial string should be non-empty hex.
	if serial == "" {
		t.Error("serial should be non-empty")
	}

	verifyCertChain(t, ca, cert)
}

func TestSignCSR_InvalidCSR(t *testing.T) {
	ca := mustCA(t)

	// Garbage DER should fail.
	_, _, err := ca.SignCSR([]byte("not a real CSR"), "host-xyz")
	if err == nil {
		t.Error("SignCSR should fail on invalid CSR DER")
	}
}

func TestSignCSR_CertChainValid(t *testing.T) {
	ca := mustCA(t)

	// Generate agent CSR.
	agentKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "test-agent"},
	}, agentKey)

	certPEM, _, err := ca.SignCSR(csrDER, "agent-chain-test")
	if err != nil {
		t.Fatalf("SignCSR failed: %v", err)
	}

	cert := mustParseCertPEM(t, certPEM)

	// Build a full verification with the CA pool — this is the authoritative
	// chain validation that proves agents can verify connections.
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)

	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		t.Fatalf("agent cert does not chain to CA: %v", err)
	}
}

func TestCACertPEM(t *testing.T) {
	ca := mustCA(t)

	pemBytes := ca.CACertPEM()

	// Should be valid PEM.
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("CACertPEM returned invalid PEM")
	}
	if block.Type != "CERTIFICATE" {
		t.Errorf("PEM type: got %q, want %q", block.Type, "CERTIFICATE")
	}

	// Should parse as a valid certificate.
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse CACertPEM: %v", err)
	}
	if !cert.IsCA {
		t.Error("CACertPEM should return a CA certificate")
	}

	// Should match the CA's own cert.
	if cert.SerialNumber.Cmp(ca.cert.SerialNumber) != 0 {
		t.Error("CACertPEM serial should match the CA's serial")
	}
}

func TestIsRevoked(t *testing.T) {
	revoked := map[string]bool{
		"abc123": true,
		"def456": true,
	}

	if !IsRevoked("abc123", revoked) {
		t.Error("abc123 should be revoked")
	}
	if !IsRevoked("def456", revoked) {
		t.Error("def456 should be revoked")
	}
	if IsRevoked("not-revoked", revoked) {
		t.Error("not-revoked should not be revoked")
	}
	if IsRevoked("abc123", nil) {
		t.Error("nil map should never report revoked")
	}
	if IsRevoked("abc123", map[string]bool{}) {
		t.Error("empty map should never report revoked")
	}
}

func TestIssueCert_UniqueSerials(t *testing.T) {
	ca := mustCA(t)

	// Issue several certs and verify serial numbers are unique.
	serials := make(map[string]bool)
	for i := 0; i < 10; i++ {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		certPEM, err := ca.IssueCert(fmt.Sprintf("host-%d", i), &key.PublicKey, false)
		if err != nil {
			t.Fatalf("IssueCert #%d failed: %v", i, err)
		}
		cert := mustParseCertPEM(t, certPEM)
		s := cert.SerialNumber.String()
		if serials[s] {
			t.Errorf("duplicate serial number: %s", s)
		}
		serials[s] = true
	}
}

// --- test helpers ---

// mustCA creates a fresh CA in a temp directory. Fails the test on error.
func mustCA(t *testing.T) *CA {
	t.Helper()
	ca, err := EnsureCA(t.TempDir())
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	return ca
}

// mustParseCertPEM decodes a PEM certificate and parses it. Fails on error.
func mustParseCertPEM(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("no PEM block in cert")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

// verifyCertChain validates that the given cert was signed by the CA.
func verifyCertChain(t *testing.T, ca *CA, cert *x509.Certificate) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)

	// Try all extended key usages that the cert declares.
	usages := cert.ExtKeyUsage
	if len(usages) == 0 {
		usages = []x509.ExtKeyUsage{x509.ExtKeyUsageAny}
	}

	_, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: usages,
	})
	if err != nil {
		t.Errorf("cert chain verification failed: %v", err)
	}
}
