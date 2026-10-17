package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// eventsAbout returns the events about name on hostID that ch carries up to
// and including the state event that follows an update, then any that come
// within a short wait after it (a second start or result would be a bug).
// inFlight, when set, is called as the state event is taken off ch.
func eventsAbout(t *testing.T, ch <-chan events.SSEEvent, hostID, name string, inFlight func() bool) (got []events.SSEEvent, inFlightAtState bool) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.HostID != hostID || e.ContainerName != name {
				continue
			}
			got = append(got, e)
			if e.Type != events.EventContainerState {
				continue
			}
			if inFlight != nil {
				inFlightAtState = inFlight()
			}
			quiet := time.After(200 * time.Millisecond)
			for {
				select {
				case e := <-ch:
					if e.HostID == hostID && e.ContainerName == name {
						got = append(got, e)
					}
				case <-quiet:
					return got, inFlightAtState
				}
			}
		case <-deadline:
			t.Fatalf("no state event for %s after its update; events so far: %+v", name, got)
		}
	}
}

// The hub announces every remote update and rollback it sends, whoever asked
// for it (a scan, the dashboard, the API, the agent auto-update): one start
// event as it sends the request, the container in flight until the agent
// answers, the event of the agent's result, and a state event once it is no
// longer in flight, so a browser that fetched the row for the result while
// the update was still in flight fetches it again. A rollback is told as a
// rollback, from start to end.
func TestUpdateContainerSync_AnnouncedOnceAndInFlightUntilAnswered(t *testing.T) {
	for _, tc := range []struct {
		name             string
		rollback         bool
		outcome, errText string
		wantStart        string
		wantEnd          string
	}{
		{"update", false, "success", "", "update started", "update web: success"},
		{"failed update", false, "failed", "pull failed", "update started", "update web failed: pull failed"},
		{"rollback", true, "success", "", "rollback started", "rollback web: success"},
		{"failed rollback", true, "failed", "pull failed", "rollback started", "rollback web failed: pull failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, addr, _, bus := testServer(t)
			token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
			hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
			stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
			t.Cleanup(closeConn)
			ch, cancel := bus.Subscribe()
			defer cancel()

			asked := make(chan struct{})
			release := make(chan struct{})
			go func() {
				for {
					msg, err := stream.Recv()
					if err != nil {
						return
					}
					if req := msg.GetUpdateContainer(); req != nil {
						close(asked)
						<-release
						_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{UpdateResult: &proto.UpdateResult{
							RequestId: msg.GetRequestId(), ContainerName: "web", OldImage: "nginx:1",
							NewImage: req.GetTargetImage(), Outcome: tc.outcome, Error: tc.errText,
						}}})
						return
					}
				}
			}()

			// The scan, the dashboard and the API all call with
			// WithCallerRecords.
			ctx, stop := context.WithTimeout(WithCallerRecords(context.Background()), 30*time.Second)
			defer stop()
			done := make(chan error, 1)
			go func() {
				var err error
				if tc.rollback {
					_, err = srv.RollbackContainerSync(ctx, hostID, "web", "nginx:0")
				} else {
					_, err = srv.UpdateContainerSync(ctx, hostID, "web", "nginx:2", "")
				}
				done <- err
			}()

			select {
			case <-asked:
			case <-time.After(30 * time.Second):
				t.Fatal("the agent was never asked")
			}
			if !srv.UpdateInFlight(hostID, "web") {
				t.Error("the container is not in flight while the agent works on it")
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatalf("sync call: %v", err)
			}

			got, inFlightAtState := eventsAbout(t, ch, hostID, "web", func() bool { return srv.UpdateInFlight(hostID, "web") })
			if len(got) != 3 ||
				got[0].Type != events.EventContainerUpdate || got[0].Message != tc.wantStart ||
				got[1].Type != events.EventContainerUpdate || got[1].Message != tc.wantEnd ||
				got[2].Type != events.EventContainerState || got[2].Message != "" {
				t.Errorf("events = %+v\nwant container_update %q, container_update %q, then a container_state with no message", got, tc.wantStart, tc.wantEnd)
			}
			if inFlightAtState {
				t.Error("the state event went out while the container was still in flight")
			}
		})
	}
}

// An update the hub refuses before it sends anything (a paused host, a
// failed signature check) never started: no start event, and it is never in
// flight.
func TestUpdateContainerSync_RefusedUpdateIsNotAnnounced(t *testing.T) {
	srv, addr, _, bus := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	_, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	t.Cleanup(closeConn)
	refused := errors.New("signature check failed")
	srv.SetUpdatePrep(func(context.Context, string, *proto.UpdateContainerRequest, bool) error { return refused })
	ch, cancel := bus.Subscribe()
	defer cancel()

	if _, err := srv.UpdateContainerSync(WithCallerRecords(context.Background()), hostID, "web", "nginx:2", ""); !errors.Is(err, refused) {
		t.Fatalf("UpdateContainerSync error = %v, want the refusal", err)
	}
	quiet := time.After(200 * time.Millisecond)
	for {
		select {
		case e := <-ch:
			if e.ContainerName == "web" {
				t.Errorf("event %+v for an update that was refused before it was sent", e)
			}
		case <-quiet:
			if srv.UpdateInFlight(hostID, "web") {
				t.Error("a refused update is in flight")
			}
			return
		}
	}
}

// A rollback result that arrives after the hub stopped waiting for it is
// still told as a rollback, not as an update.
func TestHandleUpdateResult_LateRollbackToldAsARollback(t *testing.T) {
	srv := journalTestServer(t, &mockHistoryRecorder{})
	registerTestHost(t, srv, "host-a", "agent-a")
	srv.rememberLate("host-a", "late-rb", "rollback", time.Now().UTC())
	ch, cancel := srv.bus.Subscribe()
	defer cancel()

	ur := &proto.UpdateResult{RequestId: "late-rb", ContainerName: "web", OldImage: "nginx:2", NewImage: "nginx:1", Outcome: "success"}
	srv.handleUpdateResult("host-a", &proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{UpdateResult: ur}}, ur)

	select {
	case e := <-ch:
		if e.Type != events.EventContainerUpdate || e.HostID != "host-a" || e.Message != "rollback web: success" {
			t.Errorf("event = %+v, want container_update \"rollback web: success\" for host-a", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event for the late result")
	}
}
