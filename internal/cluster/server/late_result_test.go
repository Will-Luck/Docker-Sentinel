package server

import (
	"context"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// An update result that arrives after the hub stopped waiting (the request
// timed out) is what the agent really did. It must reach the history, not
// only the live event feed.
func TestHandleUpdateResult_LateResultRecordedInHistory(t *testing.T) {
	hist := &mockHistoryRecorder{}
	srv := journalTestServer(t, hist)
	registerTestHost(t, srv, "host-a", "agent-a")

	ur := &proto.UpdateResult{
		RequestId: "late-1", ContainerName: "web",
		OldImage: "nginx:1", NewImage: "nginx:2", NewDigest: "sha256:new", Outcome: "success",
	}
	srv.handleUpdateResult("host-a", &proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{UpdateResult: ur}}, ur)

	recs := hist.all()
	if len(recs) != 1 {
		t.Fatalf("history records = %d, want 1 for the late result", len(recs))
	}
	r := recs[0]
	if r.ContainerName != store.ScopedKey("host-a", "web") || r.HostID != "host-a" || r.HostName != "agent-a" {
		t.Errorf("record keyed %q host %q/%q, want %q on host-a/agent-a", r.ContainerName, r.HostID, r.HostName, store.ScopedKey("host-a", "web"))
	}
	if r.Outcome != "success" || r.NewImage != "nginx:2" || r.NewDigest != "sha256:new" {
		t.Errorf("record = %+v, want the agent's result", r)
	}
}

// While a caller is still waiting, it records the outcome itself.
func TestHandleUpdateResult_AnsweredResultNotRecordedTwice(t *testing.T) {
	hist := &mockHistoryRecorder{}
	srv := journalTestServer(t, hist)
	registerTestHost(t, srv, "host-a", "agent-a")
	ch, err := srv.registerPending("host-a", "req-1")
	if err != nil {
		t.Fatal(err)
	}

	ur := &proto.UpdateResult{RequestId: "req-1", ContainerName: "web", Outcome: "success"}
	srv.handleUpdateResult("host-a", &proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{UpdateResult: ur}}, ur)

	if got := <-ch; got.GetUpdateResult() != ur {
		t.Fatal("waiting caller did not get the result")
	}
	if n := hist.count(); n != 0 {
		t.Errorf("history records = %d, want 0 (the waiting caller records it)", n)
	}
}

// A rollback whose result arrives after the hub stopped waiting must be
// recorded as a rollback. Recorded as a forward update, the next Rollback
// would pick it and put the rolled-back image straight back.
func TestLateRollbackResult_RecordedAsRollback(t *testing.T) {
	srv, hostID, stream := connectAgent(t)
	hist := &mockHistoryRecorder{}
	srv.SetHistoryRecorder(hist)

	defer func(d time.Duration) { updateTimeout = d }(updateTimeout)
	updateTimeout = 200 * time.Millisecond

	reqs := make(chan *proto.ServerMessage, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if msg.GetUpdateContainer() != nil {
				reqs <- msg
			}
		}
	}()

	if _, err := srv.RollbackContainerSync(context.Background(), hostID, "web", "nginx:1"); err == nil {
		t.Fatal("rollback returned no error although the agent never answered in time")
	}
	req := <-reqs
	if err := stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{UpdateResult: &proto.UpdateResult{
		RequestId: req.GetRequestId(), ContainerName: "web", OldImage: "nginx:2", NewImage: "nginx:1", Outcome: "success",
	}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return hist.count() == 1 }, "late rollback result not recorded")
	if r := hist.all()[0]; r.Type != "rollback" {
		t.Errorf("late rollback recorded with type %q, want rollback", r.Type)
	}
}
