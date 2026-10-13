package server

import (
	"context"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// connectedStream opens an agent Channel with the given credentials and
// waits until the server has registered a stream other than prev for the
// host. It returns the client stream and the server-side agentStream.
func connectedStream(t *testing.T, srv *Server, addr, hostID string, certPEM, keyPEM, caPEM []byte, prev *agentStream) (proto.AgentService_ChannelClient, *agentStream, func()) {
	t.Helper()
	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := proto.NewAgentServiceClient(conn).Channel(ctx)
	if err != nil {
		cancel()
		t.Fatalf("Channel: %v", err)
	}
	var as *agentStream
	waitFor(t, 3*time.Second, func() bool {
		srv.mu.RLock()
		defer srv.mu.RUnlock()
		cur, ok := srv.streams[hostID]
		if ok && cur != prev {
			as = cur
			return true
		}
		return false
	}, "server should register the new stream")
	return stream, as, func() { cancel(); conn.Close() }
}

// When an agent reconnects before the hub notices that the old connection
// is dead, the old stream's cleanup must not mark the host offline or fail
// requests that went out on the new stream. Requests that went out on the
// old stream can never be answered and must fail.
func TestChannel_StaleStreamCleanupLeavesLiveStreamAlone(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	_, asA, closeA := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	defer closeA()

	// A request goes out on A, which never answers (half-open socket).
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	errA := make(chan error, 1)
	go func() {
		_, err := srv.ListContainersSync(ctx, hostID)
		errA <- err
	}()
	waitFor(t, 3*time.Second, func() bool { return pendingCount(srv) == 1 }, "request on A should be pending")

	// The agent reconnects on B before the hub has noticed A is dead.
	streamB, asB, closeB := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, asA)
	defer closeB()

	answer := make(chan struct{})
	go func() {
		for {
			msg, err := streamB.Recv()
			if err != nil {
				return
			}
			if msg.GetListContainers() == nil {
				continue
			}
			<-answer
			_ = streamB.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_ContainerList{
				ContainerList: &proto.ContainerList{
					RequestId:  msg.GetRequestId(),
					Containers: []*proto.ContainerInfo{{Id: "1", Name: "web", Image: "nginx:1", State: "running"}},
				},
			}})
		}
	}()

	resB := make(chan error, 1)
	go func() {
		_, err := srv.ListContainersSync(ctx, hostID)
		resB <- err
	}()
	waitFor(t, 3*time.Second, func() bool { return pendingOn(srv, asB) == 1 }, "request on B should be pending")

	// The stale connection A finally dies (if replacing it has not already
	// ended its handler).
	closeA()

	select {
	case err := <-errA:
		if err == nil {
			t.Error("request sent on the dead stream A returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request sent on the dead stream A was not failed when A closed")
	}

	// Give A's cleanup time to run in full before checking what it touched.
	time.Sleep(200 * time.Millisecond)

	if hs, _ := srv.Registry().Get(hostID); !hs.Connected {
		t.Errorf("registry says Connected=false after stale stream A closed, but stream B is live (ConnectedHosts=%v)", srv.ConnectedHosts())
	}
	select {
	case err := <-resB:
		t.Fatalf("request in flight on live stream B ended early when A closed: %v", err)
	default:
	}

	close(answer)
	select {
	case err := <-resB:
		if err != nil {
			t.Fatalf("request on live stream B failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request on live stream B was never answered")
	}
}

// Turning cluster mode off, or stopping the hub, must not wait for agents to
// hang up: their streams stay open for as long as the agents run. Stop has
// to end the agent streams itself and return promptly.
func TestStop_ReturnsWithConnectedAgent(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	defer closeConn()

	recvErr := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				recvErr <- err
				return
			}
		}
	}()

	done := make(chan struct{})
	go func() { srv.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("srv.Stop() did not return within 5s while an agent stream was open")
	}

	select {
	case <-recvErr:
	case <-time.After(2 * time.Second):
		t.Fatal("the agent's stream was still open after Stop returned")
	}
	if hs, _ := srv.Registry().Get(hostID); hs.Connected {
		t.Error("host still marked connected after Stop")
	}
}

func pendingCount(srv *Server) int {
	srv.pendingMu.Lock()
	defer srv.pendingMu.Unlock()
	return len(srv.pending)
}

// pendingOn counts the pending requests that went out on as.
func pendingOn(srv *Server, as *agentStream) int {
	srv.pendingMu.Lock()
	defer srv.pendingMu.Unlock()
	n := 0
	for _, pr := range srv.pending {
		if pr.stream == as {
			n++
		}
	}
	return n
}

// An agent that reconnects while the server is stopping must be turned
// away, not registered: Stop has already ended the streams it knew about
// and would otherwise wait for this one.
func TestChannel_RefusedWhileStopping(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	srv.mu.Lock()
	srv.stopping = true
	srv.mu.Unlock()

	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := proto.NewAgentServiceClient(conn).Channel(ctx)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.Unavailable {
		t.Errorf("Channel during Stop: %v, want Unavailable", err)
	}
	if hs, _ := srv.Registry().Get(hostID); hs.Connected {
		t.Error("host marked connected during Stop")
	}
}

// Agents that keep reconnecting while the server stops do not hold Stop up.
func TestStop_ReturnsWhileAgentsKeepReconnecting(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	client := proto.NewAgentServiceClient(conn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			stream, err := client.Channel(ctx)
			if err == nil {
				_, _ = stream.Recv()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	waitFor(t, 3*time.Second, func() bool {
		hs, _ := srv.Registry().Get(hostID)
		return hs.Connected
	}, "agent should connect")

	done := make(chan struct{})
	go func() { srv.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s while an agent kept reconnecting")
	}
}
