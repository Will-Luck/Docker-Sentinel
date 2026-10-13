package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
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

// The hub knows which remote updates it is waiting for, so a scan can
// leave those containers alone.
func TestUpdateInFlight(t *testing.T) {
	srv, hostID, stream := connectAgent(t)
	got := make(chan string, 1)
	release := make(chan struct{})
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if req := msg.GetUpdateContainer(); req != nil {
				got <- msg.GetRequestId()
				<-release
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{
					UpdateResult: &proto.UpdateResult{RequestId: msg.GetRequestId(), ContainerName: "web", Outcome: "success"},
				}})
			}
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := srv.UpdateContainerSync(context.Background(), hostID, "web", "nginx:2", "")
		done <- err
	}()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent got no update request")
	}
	if !srv.UpdateInFlight(hostID, "web") {
		t.Error("the update the hub is waiting for is not reported in flight")
	}
	if srv.UpdateInFlight(hostID, "db") || srv.UpdateInFlight("other", "web") {
		t.Error("another container reported in flight")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if srv.UpdateInFlight(hostID, "web") {
		t.Error("the finished update is still reported in flight")
	}
}

// A pull-only update asks the agent to pull the image; nothing else.
func TestPullImage_SendsAPullRequest(t *testing.T) {
	srv, hostID, stream := connectAgent(t)
	got := make(chan string, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if p := msg.GetPullImage(); p != nil {
				got <- p.GetImageRef()
			}
		}
	}()
	if err := srv.PullImage(hostID, "nginx:2"); err != nil {
		t.Fatal(err)
	}
	select {
	case ref := <-got:
		if ref != "nginx:2" {
			t.Errorf("agent asked to pull %q, want nginx:2", ref)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the agent got no pull request")
	}
}

// A paused host gets no new updates, whatever starts them; a rollback
// still goes through.
func TestUpdateContainerSync_RefusedForAPausedHost(t *testing.T) {
	srv, hostID, stream := connectAgent(t)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if msg.GetUpdateContainer() != nil {
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{
					UpdateResult: &proto.UpdateResult{RequestId: msg.GetRequestId(), ContainerName: "web", Outcome: "success"},
				}})
			}
		}
	}()
	if err := srv.PauseHost(hostID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := srv.UpdateContainerSync(ctx, hostID, "web", "nginx:2", ""); err == nil {
		t.Error("an update went to a paused host")
	}
	if _, err := srv.RollbackContainerSync(ctx, hostID, "web", "nginx:1"); err != nil {
		t.Errorf("rollback on a paused host refused: %v", err)
	}
	if err := srv.ResumeHost(hostID); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.UpdateContainerSync(ctx, hostID, "web", "nginx:2", ""); err != nil {
		t.Errorf("update after resuming: %v", err)
	}
}

// The hub forwards the discard of a swap record to the agent, and refuses
// it with a clear error for an agent too old to do it (it does not
// advertise the feature), without sending anything.
func TestDiscardSwapSync(t *testing.T) {
	srv, hostID, stream := connectAgent(t)
	got := make(chan *proto.ContainerActionRequest, 2)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if a := msg.GetContainerAction(); a != nil {
				got <- a
				outcome := "success"
				if a.GetContainerName() == "missing" {
					outcome = "not_found"
				}
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_ContainerActionResult{
					ContainerActionResult: &proto.ContainerActionResult{RequestId: msg.GetRequestId(), ContainerName: a.GetContainerName(), Action: a.GetAction(), Outcome: outcome},
				}})
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.DiscardSwapSync(ctx, hostID, "web"); !errors.Is(err, cluster.ErrAgentTooOld) {
		t.Fatalf("discard on an agent without the feature: %v, want ErrAgentTooOld", err)
	}
	select {
	case a := <-got:
		t.Fatalf("an old agent was sent %v", a)
	default:
	}

	if err := stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_Heartbeat{Heartbeat: &proto.Heartbeat{
		SupportedFeatures: []string{cluster.FeatureDiscardSwap},
	}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return srv.hostHas(hostID, cluster.FeatureDiscardSwap) }, "the hub should see the feature")
	if err := srv.DiscardSwapSync(ctx, hostID, "web"); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if a := <-got; a.GetAction() != "discard-swap" || a.GetContainerName() != "web" {
		t.Errorf("agent got %v, want a discard-swap of web", a)
	}
	if err := srv.DiscardSwapSync(ctx, hostID, "missing"); !errors.Is(err, cluster.ErrNoSwapRecord) {
		t.Errorf("discard with no record: %v, want ErrNoSwapRecord", err)
	}
}
