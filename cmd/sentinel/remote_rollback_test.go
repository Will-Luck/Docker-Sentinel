package main

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
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	clusterserver "github.com/Will-Luck/Docker-Sentinel/internal/cluster/server"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// sentEvents is a notification channel that keeps what it is sent.
type sentEvents struct {
	mu     sync.Mutex
	events []notify.Event
}

func (s *sentEvents) Send(_ context.Context, e notify.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

func (s *sentEvents) Name() string { return "test" }

func (s *sentEvents) all() []notify.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]notify.Event(nil), s.events...)
}

// hubWithAgent starts a hub and connects one agent, named edge, to it. The
// agent answers each update request with answer(request), then sends what
// each of then returns for the request's ID. It returns a cluster adapter
// over the hub, the agent's host ID, the database and the notifications
// sent.
func hubWithAgent(t *testing.T, answer func(*proto.UpdateContainerRequest) *proto.UpdateResult, then ...func(requestID string) *proto.AgentMessage) (*clusterAdapter, string, *store.Store, *sentEvents) {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/sentinel.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ca, err := cluster.EnsureCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := clusterserver.New(ca, db, events.New(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	srv.SetHistoryRecorder(db) // as clusterManager.Start does
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

	token, _, err := srv.GenerateEnrollToken(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "edge"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	enrolConn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // enrolment bootstraps trust
		MinVersion:         tls.VersionTLS13,
	})))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	enrolled, err := proto.NewEnrollmentServiceClient(enrolConn).Enroll(ctx, &proto.EnrollRequest{Token: token, HostName: "edge", Csr: csr})
	cancel()
	enrolConn.Close()
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(enrolled.AgentCert, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(enrolled.CaCert)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert}, RootCAs: roots, MinVersion: tls.VersionTLS13,
	})))
	if err != nil {
		t.Fatal(err)
	}
	streamCtx, stop := context.WithCancel(context.Background())
	stream, err := proto.NewAgentServiceClient(conn).Channel(streamCtx)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if req := msg.GetUpdateContainer(); req != nil {
				ur := answer(req)
				ur.RequestId = msg.RequestId
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{UpdateResult: ur}})
				for _, next := range then {
					_ = stream.Send(next(msg.RequestId))
				}
			}
		}
	}()
	t.Cleanup(func() {
		stop()
		conn.Close()
		<-agentDone
	})
	waitUntil(t, 5*time.Second, func() bool { return len(srv.ConnectedHosts()) == 1 }, "the agent should connect")

	sent := &sentEvents{}
	log := logging.New(false)
	u := engine.NewUpdater(nil, nil, db, nil, config.NewTestConfig(), log, clock.Real{}, notify.NewMulti(log, sent), nil)
	wireSelfUpdateNotify(srv, u) // as clusterManager.Start does
	return &clusterAdapter{srv: srv, store: db, updater: u}, enrolled.HostId, db, sent
}

// A rollback of a remote container asked for from the dashboard or the API
// is recorded once, as a rollback from the image the container ran to the
// one it went back to, and notified, as a local one is.
func TestRollbackRemoteContainer_RecordsAndNotifies(t *testing.T) {
	for _, tc := range []struct {
		outcome    string
		want       notify.EventType
		rowOutcome string // as a local rollback row
	}{
		{"success", notify.EventRollbackOK, "rollback"},
		{"failed", notify.EventRollbackFailed, "failed"},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			a, hostID, db, sent := hubWithAgent(t, func(req *proto.UpdateContainerRequest) *proto.UpdateResult {
				ur := &proto.UpdateResult{ContainerName: req.ContainerName, OldImage: "nginx:1.26", OldDigest: "sha256:new", NewImage: req.TargetImage, Outcome: tc.outcome}
				if tc.outcome == "success" {
					ur.NewDigest = "sha256:old"
				} else {
					ur.Error = "pull failed"
				}
				return ur
			})
			key := store.ScopedKey(hostID, "nginx")
			if err := db.RecordUpdate(store.UpdateRecord{
				Timestamp: time.Now().Add(-time.Hour), ContainerName: key, OldImage: "nginx:1.25",
				NewImage: "nginx:1.26", Outcome: "success", HostID: hostID, HostName: "edge",
			}); err != nil {
				t.Fatal(err)
			}

			err := a.RollbackRemoteContainer(context.Background(), hostID, "nginx")
			if (err == nil) != (tc.outcome == "success") {
				t.Fatalf("rollback error = %v, want an error only when the agent failed", err)
			}

			recs, _ := db.ListHistoryByContainer(key, 10) // newest first
			if len(recs) != 2 {
				t.Fatalf("history = %+v, want the update and one rollback row", recs)
			}
			r := recs[0]
			if r.Outcome != tc.rowOutcome || r.Type != "" || r.OldImage != "nginx:1.26" || r.NewImage != "nginx:1.25" ||
				r.HostID != hostID || r.HostName != "edge" {
				t.Errorf("rollback row = %+v, want outcome %s from nginx:1.26 to nginx:1.25 on edge", r, tc.rowOutcome)
			}
			if tc.outcome == "failed" && !strings.Contains(r.Error, "rollback failed: pull failed") {
				t.Errorf("failed rollback row error = %q, want it to say the rollback failed and why", r.Error)
			}

			var got []notify.Event
			for _, e := range sent.all() {
				if e.Type == notify.EventRollbackOK || e.Type == notify.EventRollbackFailed {
					got = append(got, e)
				}
			}
			if len(got) != 1 || got[0].Type != tc.want || got[0].ContainerName != "nginx on edge" ||
				got[0].OldImage != "nginx:1.26" || got[0].NewImage != "nginx:1.25" {
				t.Errorf("rollback notifications = %+v, want one %s for nginx on edge", got, tc.want)
			}
		})
	}
}

// A rollback the agent did not answer is recorded as failed at the time
// the hub records its late result at, and only if that result is not
// already in: a late result that landed first stays.
func TestRecordRemoteRollback_LateResultFirstStays(t *testing.T) {
	a, db := historyAdapter(t)
	key := store.ScopedKey("host-1", "nginx")
	at := time.Now().UTC().Add(-time.Second)
	if err := db.RecordUpdate(store.UpdateRecord{
		Timestamp: at, ContainerName: key, OldImage: "nginx:1.26", NewImage: "nginx:1.25",
		Outcome: "rollback", HostID: "host-1",
	}); err != nil {
		t.Fatal(err)
	}

	unanswered := &cluster.UnansweredError{HostID: "host-1", RequestID: "r1", RecordAt: at, Err: errors.New("stream closed")}
	a.recordRemoteRollback(context.Background(), "nginx", "edge",
		store.UpdateRecord{ContainerName: key, HostID: "host-1", OldImage: "nginx:1.26", NewImage: "nginx:1.25"},
		nil, unanswered, time.Now())

	recs, _ := db.ListHistoryByContainer(key, 10)
	if len(recs) != 1 || recs[0].Outcome != "rollback" {
		t.Errorf("history = %+v, want only the late result", recs)
	}
}

// smoke F2: "Update now" on an agent's own container sent update_succeeded
// and recorded a success as soon as the agent's helper took over, before a
// swap that was then rolled back. The dashboard now records and notifies
// nothing for it; the hub does, once, with the outcome the agent reports.
func TestUpdateRemoteContainer_SelfUpdateNotifiedOnceWithItsOutcome(t *testing.T) {
	a, hostID, db, sent := hubWithAgent(t, func(req *proto.UpdateContainerRequest) *proto.UpdateResult {
		return &proto.UpdateResult{ContainerName: "sentinel-agent", OldImage: "sentinel:3.0.0", NewImage: req.TargetImage, Outcome: cluster.OutcomePending}
	}, func(id string) *proto.AgentMessage {
		return &proto.AgentMessage{Payload: &proto.AgentMessage_OfflineJournal{OfflineJournal: &proto.OfflineJournal{
			Entries: []*proto.JournalEntry{{Id: id, Action: "update", Container: "sentinel-agent", OldImage: "sentinel:3.0.0",
				NewImage: "sentinel:3.0.1", Outcome: "failed", Error: "self-update rolled back: the new container stopped or is restarting"}},
		}}}
	})

	res, err := a.UpdateRemoteContainer(context.Background(), hostID, "sentinel-agent", "sentinel:3.0.1", "")
	if err != nil || !res.Pending {
		t.Fatalf("UpdateRemoteContainer = %+v, %v; want it reported as under way", res, err)
	}
	key := store.ScopedKey(hostID, "sentinel-agent")
	waitUntil(t, 3*time.Second, func() bool { recs, _ := db.ListHistoryByContainer(key, 10); return len(recs) > 0 }, "the outcome should be recorded")
	time.Sleep(100 * time.Millisecond)
	if recs, _ := db.ListHistoryByContainer(key, 10); len(recs) != 1 || recs[0].Outcome != "failed" {
		t.Errorf("history = %+v, want one failed row", recs)
	}
	var started, ended []notify.Event
	for _, e := range sent.all() {
		switch e.Type {
		case notify.EventUpdateStarted:
			started = append(started, e)
		case notify.EventUpdateSucceeded, notify.EventUpdateFailed:
			ended = append(ended, e)
		}
	}
	if len(started) != 1 || len(ended) != 1 || ended[0].Type != notify.EventUpdateFailed || ended[0].ContainerName != "sentinel-agent on edge" {
		t.Errorf("started %+v, ended %+v; want one start and one failure for sentinel-agent on edge", started, ended)
	}
}
