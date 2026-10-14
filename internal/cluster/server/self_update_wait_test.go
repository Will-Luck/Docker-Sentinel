package server

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// selfNotes keeps the self-update notifications the hub sends.
type selfNotes struct {
	mu   sync.Mutex
	recs []store.UpdateRecord
}

func (n *selfNotes) add(_, _ string, rec store.UpdateRecord) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.recs = append(n.recs, rec)
}

func (n *selfNotes) all() []store.UpdateRecord {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.recs)
}

// selfUpdatingAgent connects an agent that answers an update of its own
// container with "pending", as a v3.0.0 agent handing the swap to its
// helper does. It returns the server, the database, the host ID, the
// stream, the request IDs it answered, the notifications and the agent's
// credentials.
type selfUpdateFixture struct {
	srv                    *Server
	st                     *store.Store
	addr, hostID           string
	stream                 proto.AgentService_ChannelClient
	reqIDs                 chan string
	notes                  *selfNotes
	certPEM, keyPEM, caPEM []byte
}

func newSelfUpdateFixture(t *testing.T) *selfUpdateFixture {
	t.Helper()
	srv, addr, st, _ := testServer(t)
	srv.SetHistoryRecorder(st)
	notes := &selfNotes{}
	srv.SetSelfUpdateNotify(notes.add)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	t.Cleanup(closeConn)
	f := &selfUpdateFixture{srv: srv, st: st, addr: addr, hostID: hostID, stream: stream, reqIDs: make(chan string, 4), notes: notes,
		certPEM: certPEM, keyPEM: keyPEM, caPEM: caPEM}
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if req := msg.GetUpdateContainer(); req != nil {
				f.reqIDs <- msg.GetRequestId()
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{UpdateResult: &proto.UpdateResult{
					RequestId: msg.GetRequestId(), ContainerName: "sentinel-agent", Outcome: cluster.OutcomePending,
					OldImage: "sentinel:3.0.0", NewImage: req.GetTargetImage(),
				}}})
			}
		}
	}()
	return f
}

func (f *selfUpdateFixture) update(t *testing.T, ctx context.Context) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ur, err := f.srv.UpdateContainerSync(ctx, f.hostID, "sentinel-agent", "sentinel:3.0.1", "")
	if err != nil || ur.GetOutcome() != cluster.OutcomePending {
		t.Fatalf("UpdateContainerSync = %+v, %v; want the pending result", ur, err)
	}
	return <-f.reqIDs
}

func (f *selfUpdateFixture) history() []store.UpdateRecord {
	recs, _ := f.st.ListHistoryByContainer(store.ScopedKey(f.hostID, "sentinel-agent"), 10)
	return recs
}

// smoke F2: an agent's update of itself was reported "success" when its
// helper took over, so the hub recorded and notified a success that was
// then rolled back. The agent now says it is under way; the hub records
// nothing until the agent that comes back reports the outcome, then one
// row and, for a caller that notifies, one notification.
func TestSelfUpdatePending_RecordedOnceWithTheReportedOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, outcome, errText string
		caller                 bool // a caller that records and notifies (the dashboard)
	}{
		{"succeeded", "success", "", true},
		{"rolled back", "failed", "self-update rolled back: the new container stopped or is restarting", true},
		{"auto-update", "success", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelfUpdateFixture(t)
			ctx := context.Background()
			if tc.caller {
				ctx = WithCallerRecords(ctx)
			}
			id := f.update(t, ctx)

			time.Sleep(100 * time.Millisecond)
			if recs := f.history(); len(recs) != 0 {
				t.Fatalf("history before the outcome = %+v, want none", recs)
			}
			if n := f.notes.all(); len(n) != 0 {
				t.Fatalf("notifications before the outcome = %+v, want none", n)
			}

			// The agent that comes back reports the outcome, under the
			// request's ID.
			if err := f.stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_OfflineJournal{OfflineJournal: &proto.OfflineJournal{
				Entries: []*proto.JournalEntry{{Id: id, Action: "update", Container: "sentinel-agent",
					OldImage: "sentinel:3.0.0", NewImage: "sentinel:3.0.1", Outcome: tc.outcome, Error: tc.errText}},
			}}}); err != nil {
				t.Fatal(err)
			}
			// The hub records the outcome, then notifies it.
			waitFor(t, 5*time.Second, func() bool {
				return len(f.history()) > 0 && (!tc.caller || len(f.notes.all()) > 0)
			}, "the outcome should be recorded and notified")
			time.Sleep(100 * time.Millisecond) // time for a second row or notification to show
			recs := f.history()
			if len(recs) != 1 || recs[0].Outcome != tc.outcome || recs[0].Error != tc.errText ||
				recs[0].OldImage != "sentinel:3.0.0" || recs[0].NewImage != "sentinel:3.0.1" || recs[0].HostName != "test-agent" {
				t.Errorf("history = %+v, want one %s row", recs, tc.outcome)
			}
			notes := f.notes.all()
			if !tc.caller {
				if len(notes) != 0 {
					t.Errorf("notifications = %+v, want none for a caller that sends none", notes)
				}
				return
			}
			if len(notes) != 1 || notes[0].Outcome != tc.outcome {
				t.Errorf("notifications = %+v, want one %s", notes, tc.outcome)
			}
		})
	}
}

// The hub waits for the outcome for a bounded time, then records the
// update as failed.
func TestSelfUpdatePending_FailedWhenTheAgentDoesNotComeBack(t *testing.T) {
	defer func(d time.Duration) { selfUpdateWaitBound = d }(selfUpdateWaitBound)
	selfUpdateWaitBound = 200 * time.Millisecond
	f := newSelfUpdateFixture(t)
	f.update(t, WithCallerRecords(context.Background()))

	waitFor(t, 5*time.Second, func() bool { return len(f.history()) > 0 && len(f.notes.all()) > 0 },
		"the update should be recorded and notified once the wait ends")
	time.Sleep(100 * time.Millisecond)
	recs := f.history()
	if len(recs) != 1 || recs[0].Outcome != "failed" || !strings.Contains(recs[0].Error, "did not come back on the new version") {
		t.Errorf("history = %+v, want one failed row saying the agent did not come back", recs)
	}
	if n := f.notes.all(); len(n) != 1 || n[0].Outcome != "failed" {
		t.Errorf("notifications = %+v, want one failure", n)
	}
}

// An agent that does not report the outcome (a replacement older than
// v3.0.0) is taken as updated when it connects running the target image.
// The hub tells agents it waits for the outcome.
func TestSelfUpdatePending_OlderReplacementOnTheTargetSucceeds(t *testing.T) {
	f := newSelfUpdateFixture(t)
	f.update(t, WithCallerRecords(context.Background()))

	conn := agentTLSConn(t, f.addr, f.certPEM, f.keyPEM, f.caPEM)
	ack, err := proto.NewAgentServiceClient(conn).ReportState(context.Background(), &proto.StateReport{
		HostId: f.hostID, AgentVersion: "2.16.1",
		Containers: []*proto.ContainerInfo{{Id: "new", Name: "sentinel-agent", Image: "sentinel:3.0.1", State: "running"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ack.GetServerFeatures(), cluster.FeatureSelfUpdatePending) {
		t.Errorf("server features = %v, want %s", ack.GetServerFeatures(), cluster.FeatureSelfUpdatePending)
	}
	waitFor(t, 5*time.Second, func() bool { return len(f.history()) > 0 && len(f.notes.all()) > 0 },
		"the update should be recorded and notified")
	time.Sleep(100 * time.Millisecond)
	if recs := f.history(); len(recs) != 1 || recs[0].Outcome != "success" {
		t.Errorf("history = %+v, want one success row", recs)
	}
	if n := f.notes.all(); len(n) != 1 || n[0].Outcome != "success" {
		t.Errorf("notifications = %+v, want one success", n)
	}
}

// An outcome the agent reports straight after its "pending" answer (here
// in the next message) is not missed: the hub waits for it before the
// caller is answered, so it is recorded and notified once.
func TestSelfUpdatePending_OutcomeRightAfterTheAnswer(t *testing.T) {
	srv, addr, st, _ := testServer(t)
	srv.SetHistoryRecorder(st)
	notes := &selfNotes{}
	srv.SetSelfUpdateNotify(notes.add)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	t.Cleanup(closeConn)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if req := msg.GetUpdateContainer(); req != nil {
				id := msg.GetRequestId()
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{UpdateResult: &proto.UpdateResult{
					RequestId: id, ContainerName: "sentinel-agent", Outcome: cluster.OutcomePending, OldImage: "sentinel:3.0.0", NewImage: req.GetTargetImage(),
				}}})
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_OfflineJournal{OfflineJournal: &proto.OfflineJournal{
					Entries: []*proto.JournalEntry{{Id: id, Action: "update", Container: "sentinel-agent", OldImage: "sentinel:3.0.0",
						NewImage: req.GetTargetImage(), Outcome: "success"}},
				}}})
			}
		}
	}()
	ctx, cancel := context.WithTimeout(WithCallerRecords(context.Background()), 5*time.Second)
	defer cancel()
	if _, err := srv.UpdateContainerSync(ctx, hostID, "sentinel-agent", "sentinel:3.0.1", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(notes.all()) > 0 }, "the outcome should be notified")
	time.Sleep(100 * time.Millisecond)
	recs, _ := st.ListHistoryByContainer(store.ScopedKey(hostID, "sentinel-agent"), 10)
	if len(recs) != 1 || recs[0].Outcome != "success" {
		t.Errorf("history = %+v, want one success row", recs)
	}
	if n := notes.all(); len(n) != 1 {
		t.Errorf("notifications = %+v, want one", n)
	}
}

// rc7: after an agent updated itself, the dashboard and the queue kept its
// parked old container and its helper (both gone from the host) until the
// next scan. When the outcome is known the hub asks the agent for its
// containers at once, and never keeps a self-update helper.
func TestSelfUpdateOutcome_RefreshesTheHostsContainers(t *testing.T) {
	srv, addr, st, _ := testServer(t)
	srv.SetHistoryRecorder(st)
	refreshed := make(chan []cluster.ContainerInfo, 4)
	srv.SetHostRefreshed(func(_ string, cs []cluster.ContainerInfo) { refreshed <- cs })
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	t.Cleanup(closeConn)

	helper := &proto.ContainerInfo{Id: "h", Name: "sentinel-agent-self-update-helper", Image: "sentinel:3.0.0", State: "running",
		Labels: map[string]string{"sentinel.self-update-helper": "true", "sentinel.self": "false"}}
	agent := &proto.ContainerInfo{Id: "new", Name: "sentinel-agent", Image: "sentinel:3.0.1", State: "running"}
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			switch {
			case msg.GetUpdateContainer() != nil:
				id := msg.GetRequestId()
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{UpdateResult: &proto.UpdateResult{
					RequestId: id, ContainerName: "sentinel-agent", Outcome: cluster.OutcomePending, OldImage: "sentinel:3.0.0", NewImage: "sentinel:3.0.1",
				}}})
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_OfflineJournal{OfflineJournal: &proto.OfflineJournal{
					Entries: []*proto.JournalEntry{{Id: id, Action: "update", Container: "sentinel-agent", OldImage: "sentinel:3.0.0",
						NewImage: "sentinel:3.0.1", Outcome: "success"}},
				}}})
			case msg.GetListContainers() != nil:
				// The old container is gone; the helper is finishing.
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_ContainerList{ContainerList: &proto.ContainerList{
					RequestId: msg.GetRequestId(), Containers: []*proto.ContainerInfo{agent, helper},
				}}})
			}
		}
	}()
	// What the hub heard while the swap was under way.
	srv.Registry().UpdateContainers(hostID, protoToContainers([]*proto.ContainerInfo{
		agent, helper, {Id: "old", Name: "sentinel-agent-old-1791990001", Image: "sentinel:3.0.0", State: "exited"},
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := srv.UpdateContainerSync(ctx, hostID, "sentinel-agent", "sentinel:3.0.1", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case cs := <-refreshed:
		if len(cs) != 1 || cs[0].Name != "sentinel-agent" {
			t.Errorf("refreshed containers = %+v, want only sentinel-agent", cs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hub did not refresh the host's containers when the outcome came in")
	}
	hs, _ := srv.GetHost(hostID)
	var names []string
	for _, c := range hs.Containers {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{"sentinel-agent"}) {
		t.Errorf("containers the hub shows = %v, want only sentinel-agent", names)
	}
}
