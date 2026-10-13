package server

import (
	"context"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// An update carries the stored credentials of the target image's registry, so
// the agent's daemon can pull from a private registry.
func TestUpdateContainerSync_SendsRegistryAuth(t *testing.T) {
	srv, hostID, stream := connectAgent(t)
	srv.SetRegistryAuth(func(ref string) string { return "auth-for-" + ref })

	got := make(chan string, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if req := msg.GetUpdateContainer(); req != nil {
				got <- req.GetRegistryAuth()
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{
					UpdateResult: &proto.UpdateResult{RequestId: msg.GetRequestId(), ContainerName: "web", Outcome: "success"},
				}})
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := srv.UpdateContainerSync(ctx, hostID, "web", "ghcr.io/org/app:2", ""); err != nil {
		t.Fatal(err)
	}
	if v := <-got; v != "auth-for-ghcr.io/org/app:2" {
		t.Errorf("registry_auth = %q", v)
	}
}
