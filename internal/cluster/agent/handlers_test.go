package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// recStream records what the agent sends on its Channel.
type recStream struct {
	grpc.BidiStreamingClient[proto.AgentMessage, proto.ServerMessage]
	mu   sync.Mutex
	sent []*proto.AgentMessage
}

func (r *recStream) Send(m *proto.AgentMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, m)
	return nil
}

func (r *recStream) messages() []*proto.AgentMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*proto.AgentMessage(nil), r.sent...)
}

// The hub waits for a reply carrying its request ID. When the agent's
// Docker list fails, the agent must still answer, with the error, or the
// hub's scan waits on this host.
func TestHandleListContainers_RepliesWhenDockerFails(t *testing.T) {
	md := newMockDocker()
	md.allContainersErr = errors.New("Cannot connect to the Docker daemon")
	a := newTestAgent(t.TempDir(), md)
	st := &recStream{}

	a.safeHandle("list-containers", "req-1", func() error {
		return a.handleListContainers(context.Background(), st, "req-1")
	})

	for _, m := range st.messages() {
		if cl := m.GetContainerList(); cl != nil && cl.GetRequestId() == "req-1" {
			if cl.GetError() == "" {
				t.Fatal("reply to req-1 carries no error, so the hub would take it for an empty host")
			}
			if len(cl.GetContainers()) != 0 {
				t.Errorf("error reply carries %d containers, want none", len(cl.GetContainers()))
			}
			return
		}
	}
	t.Fatalf("agent sent %d messages and none answered request req-1", len(st.messages()))
}

// The refresh the agent pushes after an update or action is unsolicited.
// If the list fails there is nobody to answer, and an empty list would
// blank the host's containers on an older hub, so nothing is sent.
func TestHandleListContainers_UnsolicitedRefreshSendsNothingOnFailure(t *testing.T) {
	md := newMockDocker()
	md.allContainersErr = errors.New("Cannot connect to the Docker daemon")
	a := newTestAgent(t.TempDir(), md)
	st := &recStream{}

	if err := a.handleListContainers(context.Background(), st, ""); err == nil {
		t.Error("handleListContainers returned no error for a failed list")
	}
	if n := len(st.messages()); n != 0 {
		t.Errorf("agent sent %d messages for a failed unsolicited refresh, want 0", n)
	}
}
