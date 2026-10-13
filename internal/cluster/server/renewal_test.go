package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// renewalCSR makes a fresh agent key and a CSR for it.
func renewalCSR(t *testing.T, hostID string) (csrDER, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	csrDER, err = x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: hostID}}, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return csrDER, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func certSerial(t *testing.T, certPEM []byte) string {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block in certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return fmt.Sprintf("%x", leaf.SerialNumber)
}

// awaitRenewalResponse reads the agent's stream until the hub's renewed
// certificate arrives.
func awaitRenewalResponse(t *testing.T, stream proto.AgentService_ChannelClient) []byte {
	t.Helper()
	got := make(chan []byte, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				close(got)
				return
			}
			if r := msg.GetCertRenewalResponse(); r != nil {
				got <- r.GetAgentCert()
				return
			}
		}
	}()
	select {
	case certPEM, ok := <-got:
		if !ok {
			t.Fatal("stream ended before the renewed certificate arrived")
		}
		return certPEM
	case <-time.After(5 * time.Second):
		t.Fatal("no renewed certificate within 5s")
	}
	return nil
}

// Renewing must not lock the agent out. The hub must keep the old
// certificate valid until the agent connects with the new one, and only
// then revoke the old serial.
func TestCertRenewal_OldCertValidUntilRenewedCertInUse(t *testing.T) {
	allowRenewalNow(t)
	srv, addr, st, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	oldSerial := srv.Registry().GetCertSerial(hostID)

	stream, asOld, closeOld := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	csrDER, newKeyPEM := renewalCSR(t, hostID)
	if err := stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_CertRenewal{
		CertRenewal: &proto.CertRenewalCSR{Csr: csrDER},
	}}); err != nil {
		t.Fatalf("send CSR: %v", err)
	}
	newCertPEM := awaitRenewalResponse(t, stream)
	newSerial := certSerial(t, newCertPEM)

	if revoked, _ := st.IsRevokedCert(oldSerial); revoked {
		t.Fatal("old certificate revoked before the agent has used the new one: losing the reply now locks the agent out")
	}
	if got := srv.Registry().GetCertSerial(hostID); got != oldSerial {
		t.Errorf("current serial = %s before the new certificate is used, want the old %s", got, oldSerial)
	}

	// The agent reconnects with its new certificate.
	closeOld()
	_, _, closeNew := connectedStream(t, srv, addr, hostID, newCertPEM, newKeyPEM, caPEM, asOld)
	defer closeNew()

	waitFor(t, 3*time.Second, func() bool { return srv.Registry().GetCertSerial(hostID) == newSerial },
		"hub should take the new serial once the agent connects with it")
	if revoked, _ := st.IsRevokedCert(oldSerial); !revoked {
		t.Error("old certificate still valid after the agent moved to the new one")
	}
	if revoked, _ := st.IsRevokedCert(newSerial); revoked {
		t.Error("the certificate in use was revoked")
	}
}

// A renewal response must not be dropped because the agent's send buffer
// is momentarily full.
func TestCertRenewal_WaitsForRoomInSendBuffer(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, _, _, _ := enrollAgent(t, addr, token)

	as := &agentStream{hostID: hostID, send: make(chan *proto.ServerMessage, 1)}
	as.send <- &proto.ServerMessage{} // full
	csrDER, _ := renewalCSR(t, hostID)

	handled := make(chan struct{})
	go func() {
		srv.handleCertRenewal(hostID, as, &proto.CertRenewalCSR{Csr: csrDER})
		close(handled)
	}()

	time.Sleep(100 * time.Millisecond)
	<-as.send // the stream drains the earlier message
	select {
	case msg := <-as.send:
		if msg.GetCertRenewalResponse() == nil {
			t.Fatalf("got %T, want the renewal response", msg.GetPayload())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("renewal response was dropped because the send buffer was full")
	}
	<-handled
}

// A renewed certificate the hub could not deliver must not stay valid: it
// is revoked and forgotten, and the agent asks again later.
func TestCertRenewal_UndeliveredCertIsRevoked(t *testing.T) {
	srv, addr, st, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, _, _, _ := enrollAgent(t, addr, token)
	oldSerial := srv.Registry().GetCertSerial(hostID)

	defer func(d time.Duration) { renewalSendTimeout = d }(renewalSendTimeout)
	renewalSendTimeout = 100 * time.Millisecond

	as := &agentStream{hostID: hostID, send: make(chan *proto.ServerMessage)} // never drained
	csrDER, _ := renewalCSR(t, hostID)
	srv.handleCertRenewal(hostID, as, &proto.CertRenewalCSR{Csr: csrDER})

	if got := srv.Registry().GetCertSerial(hostID); got != oldSerial {
		t.Errorf("current serial = %s after an undelivered renewal, want the old %s", got, oldSerial)
	}
	if revoked, _ := st.IsRevokedCert(oldSerial); revoked {
		t.Error("old certificate revoked although the agent never received a new one")
	}
	revoked, err := st.ListRevokedCerts()
	if err != nil {
		t.Fatalf("ListRevokedCerts: %v", err)
	}
	if len(revoked) != 1 {
		t.Errorf("revoked serials = %v, want just the undelivered new certificate", revoked)
	}
	if hs, _ := srv.Registry().Get(hostID); hs.Info.PendingCertSerial != "" {
		t.Errorf("pending serial %s kept for a certificate that was never delivered", hs.Info.PendingCertSerial)
	}
}

// Revoking a host mid-renewal must revoke both its certificates.
func TestRevokeHost_RevokesPendingRenewedCert(t *testing.T) {
	allowRenewalNow(t)
	srv, addr, st, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	oldSerial := srv.Registry().GetCertSerial(hostID)

	stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	defer closeConn()
	csrDER, _ := renewalCSR(t, hostID)
	if err := stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_CertRenewal{
		CertRenewal: &proto.CertRenewalCSR{Csr: csrDER},
	}}); err != nil {
		t.Fatalf("send CSR: %v", err)
	}
	newSerial := certSerial(t, awaitRenewalResponse(t, stream))

	if err := srv.RevokeHost(hostID); err != nil {
		t.Fatalf("RevokeHost: %v", err)
	}
	for _, serial := range []string{oldSerial, newSerial} {
		if revoked, _ := st.IsRevokedCert(serial); !revoked {
			t.Errorf("serial %s still valid after RevokeHost", serial)
		}
	}
}

// presentedServerSerial does a TLS handshake with the hub and returns the
// serial of the certificate it presents.
func presentedServerSerial(t *testing.T, srv *Server, addr string) string {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(srv.ca.CACertPEM())
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}
	defer conn.Close()
	return fmt.Sprintf("%x", conn.ConnectionState().PeerCertificates[0].SerialNumber)
}

// A hub that runs long enough must renew its own certificate before it
// expires, or every agent fails to reconnect.
func TestServerCert_RenewedBeforeExpiry(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	first := presentedServerSerial(t, srv, addr)

	if again := presentedServerSerial(t, srv, addr); again != first {
		t.Fatalf("server certificate changed between two handshakes with nothing near expiry (%s, %s)", first, again)
	}

	// 300 days on, the 365-day certificate has less than a third of its
	// life left.
	srv.certMu.Lock()
	srv.now = func() time.Time { return time.Now().Add(300 * 24 * time.Hour) }
	srv.certMu.Unlock()

	renewed := presentedServerSerial(t, srv, addr)
	if renewed == first {
		t.Fatal("server kept presenting its old certificate 300 days into a 365-day life")
	}
	if again := presentedServerSerial(t, srv, addr); again != renewed {
		t.Errorf("server certificate reissued again on the next handshake (%s, %s)", renewed, again)
	}
}

// allowRenewalNow lets a test renew a freshly enrolled certificate, which
// the hub otherwise refuses as not due yet.
func allowRenewalNow(t *testing.T) {
	t.Helper()
	old := renewalAcceptFraction
	renewalAcceptFraction = 1.01
	t.Cleanup(func() { renewalAcceptFraction = old })
}

// sendCSR sends a renewal request on stream and reports whether the hub
// answered within wait.
func sendCSR(t *testing.T, stream proto.AgentService_ChannelClient, csrDER []byte, wait time.Duration) bool {
	t.Helper()
	if err := stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_CertRenewal{
		CertRenewal: &proto.CertRenewalCSR{Csr: csrDER},
	}}); err != nil {
		t.Fatalf("send CSR: %v", err)
	}
	got := make(chan bool, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				got <- false
				return
			}
			if msg.GetCertRenewalResponse() != nil {
				got <- true
				return
			}
		}
	}()
	select {
	case ok := <-got:
		return ok
	case <-time.After(wait):
		return false
	}
}

// The hub signs a renewal only for a certificate that is due for one. A
// freshly enrolled agent (or a stolen key) cannot mint certificates at will.
func TestCertRenewal_RefusedWhenNotDue(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	defer closeConn()

	csrDER, _ := renewalCSR(t, hostID)
	if sendCSR(t, stream, csrDER, 500*time.Millisecond) {
		t.Error("hub renewed a certificate that was issued moments ago")
	}
	if hs, _ := srv.Registry().Get(hostID); hs.Info.PendingCertSerial != "" {
		t.Errorf("pending serial %s recorded for a refused renewal", hs.Info.PendingCertSerial)
	}
}

// A renewal must come with a new key.
func TestCertRenewal_RefusedWhenReusingTheCurrentKey(t *testing.T) {
	allowRenewalNow(t)
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	defer closeConn()

	block, _ := pem.Decode(keyPEM)
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: hostID}}, key)
	if err != nil {
		t.Fatal(err)
	}
	if sendCSR(t, stream, csrDER, 500*time.Millisecond) {
		t.Error("hub renewed a certificate for the key already in use")
	}
}

// At most one renewal per host in a short window, however often it asks.
func TestCertRenewal_AtMostOnePerHostPerInterval(t *testing.T) {
	allowRenewalNow(t)
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	defer closeConn()

	first, _ := renewalCSR(t, hostID)
	if !sendCSR(t, stream, first, 3*time.Second) {
		t.Fatal("first renewal was not answered")
	}
	second, _ := renewalCSR(t, hostID)
	if sendCSR(t, stream, second, 500*time.Millisecond) {
		t.Error("hub signed a second renewal for the same host straight away")
	}
}
