package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

// cluster-agent-13: the agent pushes its container list after each update
// or action. Two pushes could overtake each other, so the hub kept the
// older list. Lists go out in the order they were taken.
func TestHandleListContainers_PushesGoOutInOrder(t *testing.T) {
	h := newFakeHost()
	web := h.addContainer("web", "nginx:1", true)
	a := newTestAgent(t.TempDir(), h)
	st := &recStream{}

	firstTaking := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	var mu sync.Mutex
	h.listHook = func() {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			close(firstTaking)
			<-release // the first push is slow to take its list
		}
	}

	done := make(chan struct{})
	go func() { _ = a.handleListContainers(context.Background(), st, ""); close(done) }()
	<-firstTaking
	// The container stops; a second push follows while the first is slow.
	h.mu.Lock()
	web.running = false
	h.mu.Unlock()
	second := make(chan struct{})
	go func() { _ = a.handleListContainers(context.Background(), st, ""); close(second) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	<-done
	<-second

	msgs := st.messages()
	if len(msgs) != 2 {
		t.Fatalf("sent %d lists, want 2", len(msgs))
	}
	last := msgs[1].GetContainerList().GetContainers()
	if len(last) != 1 || last[0].GetState() != "exited" {
		t.Errorf("last list sent = %v, want the newer one, with web exited", last)
	}
}

// cluster-agent-7: the hub heard the full container list only when an
// agent connected, after the agent's own updates and actions, and at scans
// (6 hours apart by default), so a container stopped or started by hand
// showed its old state for hours. The agent sends its list with every
// listRefreshEvery-th heartbeat.
func TestHeartbeatLoop_SendsTheContainerListRegularly(t *testing.T) {
	defer func(i time.Duration, n int) { heartbeatInterval, listRefreshEvery = i, n }(heartbeatInterval, listRefreshEvery)
	heartbeatInterval, listRefreshEvery = 5*time.Millisecond, 2

	h := newFakeHost()
	h.addContainer("web", "nginx:1", true)
	a := newTestAgent(t.TempDir(), h)
	st := &recStream{}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = a.heartbeatLoop(ctx, st)

	lists := 0
	for _, m := range st.messages() {
		if m.GetContainerList() != nil {
			lists++
		}
	}
	if lists == 0 {
		t.Error("no container list sent with the heartbeats")
	}
}
