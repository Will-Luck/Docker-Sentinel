package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// The hub checks an update before it goes to an agent (the signature gate,
// for one). A refusal reaches the caller, and the agent is sent nothing.
func TestUpdateContainerSync_PrepRefusalSendsNothing(t *testing.T) {
	srv, hostID, stream := connectAgent(t)
	got := make(chan *proto.ServerMessage, 4)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			got <- msg
		}
	}()

	refused := errors.New("signature verification failed")
	var gotHost string
	var gotRollback bool
	srv.SetUpdatePrep(func(_ context.Context, h string, req *proto.UpdateContainerRequest, rollback bool) error {
		gotHost, gotRollback = h, rollback
		if req.GetContainerName() != "web" || req.GetTargetImage() != "nginx:2" {
			t.Errorf("prep saw %+v, want web to nginx:2", req)
		}
		return refused
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := srv.UpdateContainerSync(ctx, hostID, "web", "nginx:2", ""); !errors.Is(err, refused) {
		t.Fatalf("UpdateContainerSync error = %v, want the refusal", err)
	}
	if gotHost != hostID || gotRollback {
		t.Errorf("prep called for host %q rollback=%v, want %q and an update", gotHost, gotRollback, hostID)
	}
	select {
	case msg := <-got:
		if msg.GetUpdateContainer() != nil {
			t.Errorf("the agent was sent %v although the update was refused", msg)
		}
	case <-time.After(200 * time.Millisecond):
	}
}

// What the prep adds goes to the agent with the request; a rollback says
// so.
func TestRollbackContainerSync_PrepAddsToTheRequest(t *testing.T) {
	srv, hostID, stream := connectAgent(t)
	got := make(chan *proto.UpdateContainerRequest, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if req := msg.GetUpdateContainer(); req != nil {
				got <- req
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{
					UpdateResult: &proto.UpdateResult{RequestId: msg.GetRequestId(), ContainerName: "web", Outcome: "success"},
				}})
			}
		}
	}()
	srv.SetUpdatePrep(func(_ context.Context, _ string, req *proto.UpdateContainerRequest, rollback bool) error {
		if !rollback {
			t.Error("prep of a rollback was told it is an update")
		}
		req.PreviousImages = []*proto.PreviousImage{{Ref: "nginx:0", Digest: "sha256:old"}}
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := srv.RollbackContainerSync(ctx, hostID, "web", "nginx:1"); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-got:
		if len(req.GetPreviousImages()) != 1 || req.GetPreviousImages()[0].GetDigest() != "sha256:old" {
			t.Errorf("agent got previous images %v, want what the prep added", req.GetPreviousImages())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the agent got no update request")
	}
}
