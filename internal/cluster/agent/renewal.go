package agent

// Certificate renewal.
//
// An agent certificate is valid for a year. Once less than a third of its
// life is left, the agent asks the hub for a new one over its live Channel:
// it makes a new key, sends a CSR, and keeps the key in memory. When the
// hub's reply arrives the agent checks that the certificate matches that
// key and chains to its CA, writes the pair to disk, and reconnects with it
// once no command is in flight. The pair is staged in one file first, so a
// crash between writing the key and the certificate is finished on the next
// connect. The hub keeps the old certificate valid until the agent connects
// with the new one, so a lost reply only means asking again later.

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
	"os"
	"path/filepath"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// Variables so tests can shorten them.
var (
	// renewalCheckInterval is how often a long-lived session re-checks the
	// certificate. Every new session checks once at the start.
	renewalCheckInterval = 12 * time.Hour
	// renewalRetryAfter is how long to wait for the hub's reply before
	// asking again with a new key.
	renewalRetryAfter = 10 * time.Minute
	// renewalIdleWait bounds how long a renewed certificate waits for
	// in-flight commands to finish before the agent reconnects with it.
	// At least the hub's 15-minute limit on an update, so a reconnect does
	// not cut off an update the hub is still waiting for.
	renewalIdleWait = 16 * time.Minute
)

// errCertRenewed ends a session so the next one uses the renewed
// certificate.
var errCertRenewed = errors.New("reconnecting with the renewed certificate")

// pendingRenewal is a CSR sent to the hub that has not been answered yet.
type pendingRenewal struct {
	keyPEM []byte
	sentAt time.Time
}

func (a *Agent) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *Agent) renewalPath() string {
	return filepath.Join(a.cfg.DataDir, "agent-renewal.pem")
}

// renewalDue reports whether leaf has less than a third of its life left
// at now.
func renewalDue(leaf *x509.Certificate, now time.Time) bool {
	lifetime := leaf.NotAfter.Sub(leaf.NotBefore)
	return leaf.NotAfter.Sub(now) < lifetime/3
}

// renewalLoop checks the certificate at the start of a session and every
// renewalCheckInterval after. Once a renewed certificate is on disk it
// waits for in-flight commands, then ends the session with errCertRenewed.
func (a *Agent) renewalLoop(ctx context.Context, stream proto.AgentService_ChannelClient) error {
	ticker := time.NewTicker(renewalCheckInterval)
	defer ticker.Stop()
	for {
		if err := a.requestRenewalIfDue(stream); err != nil {
			a.log.Warn("certificate renewal request failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.certRenewed:
			a.waitIdle(ctx)
			return errCertRenewed
		case <-ticker.C:
		}
	}
}

// requestRenewalIfDue sends the hub a CSR for a new key when the current
// certificate is due for renewal and no recent request is outstanding.
func (a *Agent) requestRenewalIfDue(stream proto.AgentService_ChannelClient) error {
	leaf, err := a.loadLeafCert()
	if err != nil {
		return err
	}
	now := a.clock()
	if !renewalDue(leaf, now) {
		return nil
	}
	if _, err := os.Stat(a.renewalPath()); err == nil {
		// A renewed pair is staged and is installed on the next connect.
		return nil
	}
	if !a.hubRenews.Load() {
		// A hub before v3.0.0 revokes the old certificate as soon as it
		// signs a new one and can drop the reply, which would lock this
		// agent out. Keep the working certificate and say why.
		a.log.Warn("agent certificate expires soon, but the server does not renew certificates; upgrade the server to v3.0.0 or later before it expires",
			"expires", leaf.NotAfter.Format(time.RFC3339))
		return nil
	}

	a.renewMu.Lock()
	outstanding := a.renewal != nil && now.Sub(a.renewal.sentAt) < renewalRetryAfter
	a.renewMu.Unlock()
	if outstanding {
		return nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: a.hostID},
	}, key)
	if err != nil {
		return fmt.Errorf("create csr: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}

	a.renewMu.Lock()
	a.renewal = &pendingRenewal{
		keyPEM: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		sentAt: now,
	}
	a.renewMu.Unlock()

	a.log.Info("agent certificate expires soon, asking the hub for a new one", "expires", leaf.NotAfter.Format(time.RFC3339))
	return a.sendMsg(stream, &proto.AgentMessage{
		Payload: &proto.AgentMessage_CertRenewal{CertRenewal: &proto.CertRenewalCSR{Csr: csrDER}},
	})
}

// loadLeafCert parses the agent certificate on disk.
func (a *Agent) loadLeafCert() (*x509.Certificate, error) {
	a.credMu.Lock()
	data, err := os.ReadFile(a.certPath)
	a.credMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("read agent cert: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("agent cert %s holds no certificate", a.certPath)
	}
	return x509.ParseCertificate(block.Bytes)
}

// handleCertRenewal installs the certificate the hub signed for the agent's
// outstanding renewal request, then tells the session to reconnect with it.
// A certificate the agent did not ask for, or one that does not match the
// key it is waiting on, is ignored: the current pair stays in place.
func (a *Agent) handleCertRenewal(resp *proto.CertRenewalResponse) error {
	certPEM := resp.GetAgentCert()
	if len(certPEM) == 0 {
		return fmt.Errorf("empty cert in renewal response")
	}

	a.renewMu.Lock()
	p := a.renewal
	a.renewMu.Unlock()
	if p == nil {
		return fmt.Errorf("ignoring a renewed certificate the agent did not ask for")
	}
	if err := a.checkRenewedCert(certPEM, p.keyPEM); err != nil {
		return fmt.Errorf("renewed certificate rejected: %w", err)
	}
	if err := a.commitRenewedCert(certPEM, p.keyPEM); err != nil {
		return err
	}

	a.renewMu.Lock()
	if a.renewal == p {
		a.renewal = nil
	}
	a.renewMu.Unlock()

	a.log.Info("renewed certificate saved, reconnecting with it once idle")
	select {
	case a.certRenewed <- struct{}{}:
	default:
	}
	return nil
}

// checkRenewedCert verifies that certPEM is for keyPEM, was issued by the
// agent's CA for client authentication, and names this host.
func (a *Agent) checkRenewedCert(certPEM, keyPEM []byte) error {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("does not match the requested key: %w", err)
	}
	leaf := pair.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return err
		}
	}
	caPEM, err := os.ReadFile(a.caPath)
	if err != nil {
		return fmt.Errorf("read ca cert: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("failed to parse ca cert")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return err
	}
	if leaf.Subject.CommonName != a.hostID {
		return fmt.Errorf("issued to %q, not this host %q", leaf.Subject.CommonName, a.hostID)
	}
	return nil
}

// commitRenewedCert writes a renewed pair to disk. The pair is staged in a
// single file first, so an interruption between replacing the key and the
// certificate is finished by recoverStagedRenewal.
func (a *Agent) commitRenewedCert(certPEM, keyPEM []byte) error {
	a.credMu.Lock()
	defer a.credMu.Unlock()

	staged := append(append([]byte{}, certPEM...), keyPEM...)
	if err := writeFileAtomic(a.renewalPath(), staged, 0600); err != nil {
		return fmt.Errorf("stage renewed certificate: %w", err)
	}
	return a.installPairLocked(certPEM, keyPEM)
}

// installPairLocked replaces the live key and certificate, then drops the
// staged copy. Caller holds credMu.
func (a *Agent) installPairLocked(certPEM, keyPEM []byte) error {
	if err := writeFileAtomic(a.keyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("write renewed key: %w", err)
	}
	if err := writeFileAtomic(a.certPath, certPEM, 0600); err != nil {
		return fmt.Errorf("write renewed cert: %w", err)
	}
	if err := os.Remove(a.renewalPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.log.Warn("could not remove the staged certificate renewal", "path", a.renewalPath(), "error", err)
	}
	return nil
}

// recoverStagedRenewal finishes installing a renewed pair that was staged
// before the agent stopped. A staged file that does not hold a matching
// pair is discarded and the live pair is kept.
func (a *Agent) recoverStagedRenewal() {
	a.credMu.Lock()
	defer a.credMu.Unlock()

	// Temp files of a credential write that was interrupted (writeFileAtomic
	// names them ".<file>.tmp-*"). credMu is held, so none is in use.
	for _, path := range []string{a.certPath, a.keyPath, a.renewalPath()} {
		pattern := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if err := os.Remove(m); err != nil {
				a.log.Warn("could not remove an interrupted credential temp file", "path", m, "error", err)
			}
		}
	}

	data, err := os.ReadFile(a.renewalPath())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		a.log.Warn("could not read the staged certificate renewal", "error", err)
		return
	}
	certPEM, keyPEM := splitPEM(data)
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		a.log.Warn("discarding an incomplete staged certificate renewal", "error", err)
		if err := os.Remove(a.renewalPath()); err != nil {
			a.log.Warn("could not remove the staged certificate renewal", "error", err)
		}
		return
	}
	if err := a.installPairLocked(certPEM, keyPEM); err != nil {
		a.log.Error("could not finish installing the staged certificate renewal", "error", err)
		return
	}
	a.log.Info("finished installing a renewed certificate staged before a restart")
}

// splitPEM separates certificate blocks from the private key block.
func splitPEM(data []byte) (certPEM, keyPEM []byte) {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return certPEM, keyPEM
		}
		switch block.Type {
		case "CERTIFICATE":
			certPEM = append(certPEM, pem.EncodeToMemory(block)...)
		case "EC PRIVATE KEY", "PRIVATE KEY":
			keyPEM = append(keyPEM, pem.EncodeToMemory(block)...)
		}
	}
}

// waitIdle returns once no command handler is running, after
// renewalIdleWait, or when ctx ends.
func (a *Agent) waitIdle(ctx context.Context) {
	deadline := time.Now().Add(renewalIdleWait)
	for a.inflight.Load() > 0 && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// writeFileAtomic replaces path with data, so a reader sees either the old
// contents or the new, never a partial write.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	committed = true

	// Persist the rename itself.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
