package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// connectAgent enrols an agent, opens its Channel and waits until the hub
// has registered it.
func connectAgent(t *testing.T) (*Server, string, proto.AgentService_ChannelClient) {
	t.Helper()
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	t.Cleanup(closeConn)
	return srv, hostID, stream
}

// An agent whose Docker list fails answers with an error. The hub must
// report that error, and must not replace the host's known containers with
// the empty list that came with it.
func TestListContainersSync_AgentErrorReply(t *testing.T) {
	srv, hostID, stream := connectAgent(t)
	srv.Registry().UpdateContainers(hostID, protoToContainers([]*proto.ContainerInfo{{Id: "1", Name: "web", Image: "nginx:1", State: "running"}}))

	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if msg.GetListContainers() != nil {
				_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_ContainerList{
					ContainerList: &proto.ContainerList{RequestId: msg.GetRequestId(), Error: "Cannot connect to the Docker daemon"},
				}})
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	containers, err := srv.ListContainersSync(ctx, hostID)
	if err == nil || !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Fatalf("ListContainersSync = %d containers, err %v; want the agent's error", len(containers), err)
	}
	if hs, _ := srv.Registry().Get(hostID); len(hs.Containers) != 1 {
		t.Errorf("host's known containers = %d after an error reply, want the previous 1", len(hs.Containers))
	}
}

// A connected agent that never answers must not hold up the caller for
// ever. The scheduled scan passes a context with no deadline.
func TestListContainersSync_BoundedWhenAgentNeverReplies(t *testing.T) {
	srv, hostID, stream := connectAgent(t)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				return
			}
		}
	}()

	defer func(d time.Duration) { listTimeout = d }(listTimeout)
	listTimeout = 200 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, err := srv.ListContainersSync(context.Background(), hostID)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ListContainersSync returned no error although the agent never replied")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListContainersSync was still waiting 5s after its time limit")
	}
	if n := pendingCount(srv); n != 0 {
		t.Errorf("%d pending requests left behind after the time limit", n)
	}
}

// The same bound applies to updates, actions and log fetches.
func TestSyncCalls_BoundedWhenAgentNeverReplies(t *testing.T) {
	srv, hostID, stream := connectAgent(t)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				return
			}
		}
	}()

	defer func(u, a, l time.Duration) { updateTimeout, actionTimeout, logsTimeout = u, a, l }(updateTimeout, actionTimeout, logsTimeout)
	updateTimeout, actionTimeout, logsTimeout = 200*time.Millisecond, 200*time.Millisecond, 200*time.Millisecond

	calls := map[string]func() error{
		"update": func() error {
			_, err := srv.UpdateContainerSync(context.Background(), hostID, "web", "nginx:2", "")
			return err
		},
		"action": func() error { return srv.ContainerActionSync(context.Background(), hostID, "web", "restart") },
		"logs": func() error {
			_, err := srv.FetchLogsSync(context.Background(), hostID, "web", 10)
			return err
		},
	}
	for name, call := range calls {
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: no error although the agent never replied", name)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: still waiting 5s after its time limit", name)
		}
	}
}
