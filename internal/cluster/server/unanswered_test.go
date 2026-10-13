package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// When the stream drops while an agent updates a container, the caller
// records the update as failed ("agent disconnected"). The agent's true
// result, replayed from its offline journal when it reconnects, replaces
// that row: the history shows one row for the update, with its outcome.
func TestUnansweredUpdate_ReplayedResultReplacesTheFailure(t *testing.T) {
	srv, addr, st, _ := testServer(t)
	srv.SetHistoryRecorder(st)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	stream, as, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)

	reqID := make(chan string, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if msg.GetUpdateContainer() != nil {
				reqID <- msg.GetRequestId()
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := srv.UpdateContainerSync(context.Background(), hostID, "web", "nginx:2", "")
		done <- err
	}()
	id := <-reqID
	closeConn() // the stream drops mid-update
	err := <-done
	var un *cluster.UnansweredError
	if !errors.As(err, &un) || un.RequestID != id {
		t.Fatalf("UpdateContainerSync error = %v, want an UnansweredError for request %s", err, id)
	}
	// The caller records the failure, as the dashboard and the scan do.
	key := store.ScopedKey(hostID, "web")
	if err := st.RecordUpdate(store.UpdateRecord{
		Timestamp: cluster.HistoryTime(err, time.Now()), ContainerName: key, Outcome: "failed", Error: err.Error(), HostID: hostID,
	}); err != nil {
		t.Fatal(err)
	}

	// The agent reconnects and replays what it did.
	stream2, _, closeConn2 := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, as)
	t.Cleanup(closeConn2)
	if err := stream2.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_OfflineJournal{OfflineJournal: &proto.OfflineJournal{
		Entries: []*proto.JournalEntry{{Id: id, Timestamp: timestamppb.Now(), Action: "update", Container: "web",
			OldImage: "nginx:1", NewImage: "nginx:2", Outcome: "success"}},
	}}}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 3*time.Second, func() bool {
		recs, _ := st.ListHistoryByContainer(key, 10)
		return len(recs) == 1 && recs[0].Outcome == "success"
	}, "the replayed result should replace the failure row")
}

// review (minor): a rollback that the agent finished while the stream was
// down was replayed as a plain update, so the next Rollback would undo it.
// The replay takes the request's purpose, and replaces the failure row.
func TestUnansweredRollback_ReplayedAsARollback(t *testing.T) {
	srv, addr, st, _ := testServer(t)
	srv.SetHistoryRecorder(st)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	stream, as, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)

	reqID := make(chan string, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if msg.GetUpdateContainer() != nil {
				reqID <- msg.GetRequestId()
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := srv.RollbackContainerSync(context.Background(), hostID, "web", "nginx:1")
		done <- err
	}()
	id := <-reqID
	closeConn()
	err := <-done
	key := store.ScopedKey(hostID, "web")
	if err := st.RecordUpdate(store.UpdateRecord{
		Timestamp: cluster.HistoryTime(err, time.Now()), ContainerName: key, Outcome: "failed", HostID: hostID,
	}); err != nil {
		t.Fatal(err)
	}

	stream2, _, closeConn2 := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, as)
	t.Cleanup(closeConn2)
	if err := stream2.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_OfflineJournal{OfflineJournal: &proto.OfflineJournal{
		Entries: []*proto.JournalEntry{{Id: id, Timestamp: timestamppb.Now(), Action: "update", Container: "web",
			OldImage: "nginx:2", NewImage: "nginx:1", Outcome: "success"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		recs, _ := st.ListHistoryByContainer(key, 10)
		return len(recs) == 1 && recs[0].Outcome == "success" && recs[0].Type == "rollback"
	}, "the replayed rollback should replace the failure row, as a rollback")
}
