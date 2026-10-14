package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// The errors of a request to an agent end up in history rows,
// notifications and toasts: they name the agent's host, not its ID.
func TestRequestErrors_NameTheHost(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)

	sent := make(chan struct{})
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if msg.GetUpdateContainer() != nil {
				close(sent)
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := srv.UpdateContainerSync(context.Background(), hostID, "web", "nginx:2", "")
		done <- err
	}()
	<-sent
	closeConn() // the stream drops mid-update
	dropped := <-done

	waitFor(t, 30*time.Second, func() bool { return len(srv.ConnectedHosts()) == 0 }, "the host should be marked disconnected")
	_, offline := srv.UpdateContainerSync(context.Background(), hostID, "web", "nginx:2", "")

	for what, err := range map[string]error{"stream dropped": dropped, "not connected": offline} {
		if err == nil || !strings.Contains(err.Error(), "agent test-agent ") || strings.Contains(err.Error(), hostID) {
			t.Errorf("%s: error = %v, want it to name the host test-agent and not its ID", what, err)
		}
	}
}

// rc6: the connect and disconnect events carried the host's ID in their
// host name. They carry the ID as the host ID, which the dashboard finds
// the host by, and the name as the host name.
func TestConnectEvents_NameTheHost(t *testing.T) {
	srv, addr, _, bus := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	evts, cancel := bus.Subscribe()
	defer cancel()

	_, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	closeConn()

	for _, want := range []string{"connected", "disconnected"} {
		deadline := time.After(30 * time.Second)
		for found := false; !found; {
			select {
			case e := <-evts:
				if e.Type != events.EventClusterHost || !strings.HasSuffix(e.Message, " "+want) {
					continue
				}
				found = true
				if e.HostID != hostID || e.HostName != "test-agent" {
					t.Errorf("%s event: host_id %q, host_name %q; want %s and test-agent", want, e.HostID, e.HostName, hostID)
				}
			case <-deadline:
				t.Fatalf("no %s event", want)
			}
		}
	}
}
