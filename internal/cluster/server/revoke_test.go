package server

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// hookStore runs onRevoke inside AddRevokedCert, so a test can make other
// work happen in the middle of a revocation.
type hookStore struct {
	*store.Store
	once     sync.Once
	onRevoke func(serial string)
}

func (h *hookStore) AddRevokedCert(serial string) error {
	h.once.Do(func() { h.onRevoke(serial) })
	return h.Store.AddRevokedCert(serial)
}

// A renewal that lands while the host is being revoked must not leave the
// host with a valid certificate: every serial issued to it ends up revoked,
// and the host is gone from the store.
func TestRevokeHost_RenewalDuringRevokeIsRevoked(t *testing.T) {
	ca, err := cluster.EnsureCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir() + "/hub.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := &hookStore{Store: db}
	srv, err := New(ca, st, events.New(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	const hostID = "host-r"
	if err := srv.registry.Register(cluster.HostInfo{ID: hostID, Name: "r", State: cluster.HostActive, CertSerial: "aaaa"}); err != nil {
		t.Fatal(err)
	}

	csrDER, _ := renewalCSR(t, hostID)
	as := &agentStream{hostID: hostID, send: make(chan *proto.ServerMessage, 4)}
	renewed := make(chan struct{})
	st.onRevoke = func(string) {
		// RevokeHost is half way: the renewal arrives now.
		go func() {
			srv.handleCertRenewal(hostID, as, &proto.CertRenewalCSR{Csr: csrDER})
			close(renewed)
		}()
		select {
		case <-renewed:
		case <-time.After(200 * time.Millisecond):
		}
	}

	if err := srv.RevokeHost(hostID); err != nil {
		t.Fatalf("RevokeHost: %v", err)
	}
	<-renewed

	revoked, err := db.ListRevokedCerts()
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 2 {
		t.Errorf("revoked serials = %v, want the old one and the renewal issued during the revoke", revoked)
	}
	if data, _ := db.GetClusterHost(hostID); data != nil {
		t.Error("revoked host is still in the store")
	}
}

// A certificate the CA signed for this host ID but the hub never recorded
// (a superseded renewal, a copied key pair) must not get in.
func TestChannel_RejectsCertificateNotIssuedToHost(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, _, _, caPEM := enrollAgent(t, addr, token)

	csrDER, strayKeyPEM := renewalCSR(t, hostID)
	strayCertPEM, _, err := srv.ca.SignCSR(csrDER, hostID)
	if err != nil {
		t.Fatal(err)
	}
	conn := agentTLSConn(t, addr, strayCertPEM, strayKeyPEM, caPEM)
	client := proto.NewAgentServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := client.ReportState(ctx, &proto.StateReport{HostId: hostID}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("ReportState with an unrecorded certificate: %v, want PermissionDenied", err)
	}
	stream, err := client.Channel(ctx)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.PermissionDenied {
		t.Errorf("Channel with an unrecorded certificate: %v, want PermissionDenied", err)
	}
}

// slowSaveStore pauses the first host save it sees until release is
// closed, and says when it has started.
type slowSaveStore struct {
	*store.Store
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (s *slowSaveStore) pause() {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
}

func (s *slowSaveStore) SaveClusterHost(id string, data []byte) error {
	s.pause()
	return s.Store.SaveClusterHost(id, data)
}

func (s *slowSaveStore) SaveClusterHostWith(id string, fn func() ([]byte, error)) error {
	s.pause()
	return s.Store.SaveClusterHostWith(id, fn)
}

// A save that was already under way when the host was removed (a late
// last-seen write from its disconnect) must not bring the host back.
func TestRegistry_LateSaveDoesNotResurrectRemovedHost(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/hub.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := NewRegistry(db, slog.Default())
	if err := reg.Register(cluster.HostInfo{ID: "h1", Name: "one"}); err != nil {
		t.Fatal(err)
	}

	st := &slowSaveStore{Store: db, started: make(chan struct{}), release: make(chan struct{})}
	reg.store = st

	saved := make(chan struct{})
	go func() {
		_ = reg.PersistLastSeen("h1")
		close(saved)
	}()
	<-st.started

	removed := make(chan struct{})
	go func() {
		_ = reg.Remove("h1")
		close(removed)
	}()
	// Give Remove the chance to run while the save is paused.
	select {
	case <-removed:
	case <-time.After(200 * time.Millisecond):
	}
	close(st.release)
	<-saved
	<-removed

	if data, _ := db.GetClusterHost("h1"); data != nil {
		t.Error("removed host was written back to the store by a late save")
	}
}

// One host's slow database write must not hold up every other host: their
// heartbeats only touch memory.
func TestRegistry_SlowSaveDoesNotBlockOtherHosts(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/hub.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := NewRegistry(db, slog.Default())
	for _, id := range []string{"h1", "h2"} {
		if err := reg.Register(cluster.HostInfo{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	st := &slowSaveStore{Store: db, started: make(chan struct{}), release: make(chan struct{})}
	reg.store = st
	defer close(st.release)

	go func() { _ = reg.PersistLastSeen("h1") }()
	<-st.started

	done := make(chan struct{})
	go func() {
		reg.SetConnected("h2", true)
		_, _ = reg.Get("h2")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("another host's state was blocked behind a slow save of h1")
	}
}

// live-11: revoking a host cut its agent off only some 20 seconds later.
// The agent's stream ends at once.
func TestRevokeHost_DisconnectsTheAgentAtOnce(t *testing.T) {
	srv, hostID, stream := connectAgent(t)
	ended := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				ended <- err
				return
			}
		}
	}()
	start := time.Now()
	if err := srv.RevokeHost(hostID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("the revoked agent's stream ended after %v", d)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the revoked agent's stream was still open 30s later")
	}
}
