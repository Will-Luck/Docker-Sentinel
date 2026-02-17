package cluster

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CA manages a built-in certificate authority for mTLS between server and agents.
// All issued certificates use ECDSA P-256. The CA cert itself is self-signed with
// a 10-year validity period; agent and server certs are valid for 1 year.
type CA struct {
	certPath string // path to CA certificate PEM
	keyPath  string // path to CA private key PEM
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
	mu       sync.Mutex // protects cert issuance (serialise serial number generation)
}

// EnsureCA loads or creates a CA certificate and key in the given directory.
// If ca.pem and ca-key.pem already exist and parse correctly, they are reused.
// Otherwise a fresh CA is generated. Directory is created if it doesn't exist.
//
// CA cert: 10-year validity, ECDSA P-256, IsCA=true, KeyUsageCertSign|CRLSign.
// File permissions: key 0600, cert 0644.
func EnsureCA(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create ca dir: %w", err)
	}

	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")

	// Try loading existing CA first.
	if fileExists(certPath) && fileExists(keyPath) {
		ca, err := loadCA(certPath, keyPath)
		if err == nil {
			return ca, nil
		}
		// Existing files are broken — regenerate below.
	}

	// Generate new CA key pair.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ca key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("generate ca serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Docker-Sentinel CA"},
		NotBefore:    now.Add(-1 * time.Hour), // small backdate to handle clock skew
		NotAfter:     now.Add(10 * 365 * 24 * time.Hour),

		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0, // leaf-only CA — cannot issue sub-CAs
		MaxPathLenZero:        true,

		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	// Self-sign the CA certificate.
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create ca cert: %w", err)
	}

	// Parse back so we have the *x509.Certificate for signing operations.
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse ca cert: %w", err)
	}

	// Persist to disk.
	if err := writeCertPEM(certPath, certDER, 0644); err != nil {
		return nil, err
	}
	if err := writeKeyPEM(keyPath, key); err != nil {
		return nil, err
	}

	return &CA{
		certPath: certPath,
		keyPath:  keyPath,
		cert:     cert,
		key:      key,
	}, nil
}

// IssueCert signs a certificate for a server or agent using the given public key.
//
// Server certs (isServer=true) get both ExtKeyUsageServerAuth and
// ExtKeyUsageClientAuth so the server can also authenticate to agents.
// Agent certs (isServer=false) get ExtKeyUsageClientAuth only.
//
// Validity: 1 year. Serial: random 128-bit. CN: the provided name.
func (ca *CA) IssueCert(name string, pub crypto.PublicKey, isServer bool) (certPEM []byte, err error) {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	usage := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if isServer {
		usage = append(usage, x509.ExtKeyUsageServerAuth)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    now.Add(-1 * time.Hour),
		NotAfter:     now.Add(365 * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           usage,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
	if err != nil {
		return nil, fmt.Errorf("sign cert: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), nil
}

// IssueServerCert generates a new ECDSA P-256 key pair and issues a server
// certificate signed by this CA. The cert includes SANs for localhost,
// loopback IPs, and the host's private network IPs.
//
// Returns the cert and key as PEM-encoded byte slices (suitable for
// tls.X509KeyPair or writing to disk).
func (ca *CA) IssueServerCert() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate server key: %w", err)
	}

	ca.mu.Lock()
	defer ca.mu.Unlock()

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "sentinel-server"},
		NotBefore:    now.Add(-1 * time.Hour),
		NotAfter:     now.Add(365 * 24 * time.Hour),

		KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,

		DNSNames:    []string{"localhost"},
		IPAddresses: privateIPs(),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("sign server cert: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal server key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

// SignCSR signs a PKCS#10 Certificate Signing Request from an enrolling agent.
// The CSR must be DER-encoded. The resulting agent cert is valid for 1 year
// with ExtKeyUsageClientAuth only. CN is set to the provided hostID (not the
// CSR's subject, which the server doesn't trust).
//
// Returns the signed certificate PEM and its serial number as a hex string.
func (ca *CA) SignCSR(csrDER []byte, hostID string) (certPEM []byte, serial string, err error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, "", fmt.Errorf("parse csr: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", fmt.Errorf("csr signature invalid: %w", err)
	}

	ca.mu.Lock()
	defer ca.mu.Unlock()

	serialNum, err := randomSerial()
	if err != nil {
		return nil, "", fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serialNum,
		Subject:      pkix.Name{CommonName: hostID},
		NotBefore:    now.Add(-1 * time.Hour),
		NotAfter:     now.Add(365 * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.key)
	if err != nil {
		return nil, "", fmt.Errorf("sign agent cert: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	serial = fmt.Sprintf("%x", serialNum)

	return certPEM, serial, nil
}

// CACertPEM returns the CA certificate in PEM format. This is distributed to
// agents so they can verify the server's identity during mTLS handshake.
func (ca *CA) CACertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: ca.cert.Raw,
	})
}

// IsRevoked checks if a certificate serial number appears in the revocation set.
// The revocation list itself is maintained externally (in BoltDB); this function
// is a pure lookup helper.
func IsRevoked(serial string, revokedSerials map[string]bool) bool {
	return revokedSerials[serial]
}

// --- internal helpers ---

// loadCA reads an existing CA cert and key from PEM files.
func loadCA(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read ca cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read ca key: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in ca cert")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse ca cert: %w", err)
	}

	block, _ = pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in ca key")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse ca key: %w", err)
	}

	return &CA{
		certPath: certPath,
		keyPath:  keyPath,
		cert:     cert,
		key:      key,
	}, nil
}

// randomSerial generates a cryptographically random 128-bit serial number,
// as recommended by CABForum for certificate serial numbers.
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

// privateIPs returns IP SANs for server certificates: localhost IPs plus
// private unicast IPs from the host's network interfaces. Duplicates are
// filtered. This mirrors the logic in internal/web/tls.go selfSignedIPs().
func privateIPs() []net.IP {
	ips := []net.IP{
		net.ParseIP("127.0.0.1"),
		net.ParseIP("::1"),
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips // best-effort — loopback is always available
	}

	seen := make(map[string]bool)
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		if ipNet.IP.IsLoopback() || !ipNet.IP.IsPrivate() {
			continue
		}
		s := ipNet.IP.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		ips = append(ips, ipNet.IP)
	}
	return ips
}

// writeCertPEM encodes a DER certificate as PEM and writes it to path.
func writeCertPEM(path string, certDER []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("write cert %s: %w", path, err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return fmt.Errorf("encode cert pem: %w", err)
	}
	return nil
}

// writeKeyPEM encodes an ECDSA private key as PEM and writes it with 0600 perms.
func writeKeyPEM(path string, key *ecdsa.PrivateKey) error {
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("write key %s: %w", path, err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return fmt.Errorf("encode key pem: %w", err)
	}
	return nil
}

// fileExists returns true if a file exists at the given path.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
