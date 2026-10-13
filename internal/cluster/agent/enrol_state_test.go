package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"os"
	"path/filepath"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
)

// cluster-agent-10: an enrolment interrupted after the key was written but
// before the host ID left an agent that counted as enrolled and could not
// start ("read host id"). The host ID is also in the certificate (its
// common name): the agent reads it from there and writes the file again.
func TestLoadHostID_FallsBackToTheCertificate(t *testing.T) {
	ca, err := cluster.EnsureCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "agent"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _, err := ca.SignCSR(csr, "host-abc123")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	a := newTestAgent(dir, newMockDocker())
	a.certPath = filepath.Join(dir, "agent.pem")
	if err := os.WriteFile(a.certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}

	id, err := a.loadHostID()
	if err != nil || id != "host-abc123" {
		t.Fatalf("loadHostID = %q, %v; want host-abc123 from the certificate", id, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "host-id")); string(b) != "host-abc123" {
		t.Errorf("host-id file = %q, want it written again", b)
	}
}
