package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// listEvent is what a dashboard acts on in a container_state event: no
// container name reloads the page, a name refreshes that row.
type listEvent struct {
	container string
	hostID    string
}

// stateEvents takes the container_state events already published on ch.
// Publish is synchronous, so they are all there once the handler returns.
func stateEvents(ch <-chan events.SSEEvent) []listEvent {
	var got []listEvent
	for {
		select {
		case e := <-ch:
			if e.Type == events.EventContainerState {
				got = append(got, listEvent{container: e.ContainerName, hostID: e.HostID})
			}
		default:
			return got
		}
	}
}

func knownList() []*proto.ContainerInfo {
	return []*proto.ContainerInfo{
		{Id: "id-web", Name: "web", Image: "nginx:1", State: "running"},
		{Id: "id-db", Name: "db", Image: "postgres:16", State: "running"},
	}
}

// A container removed, added, renamed, started or stopped on an agent host
// by something other than Sentinel reaches an open dashboard: a list that
// changes the host's set of containers reloads it once, one that changes a
// row refreshes that row. The list the agent resends every few minutes, the
// same as the last, publishes nothing.
func TestHandleContainerList_PublishesWhatTheDashboardShows(t *testing.T) {
	reload := []listEvent{{hostID: "host-1"}}
	tests := []struct {
		name string
		edit func([]*proto.ContainerInfo) []*proto.ContainerInfo
		want []listEvent
	}{
		{"same list", func(l []*proto.ContainerInfo) []*proto.ContainerInfo { return l }, nil},
		{"same list in another order", func(l []*proto.ContainerInfo) []*proto.ContainerInfo {
			return []*proto.ContainerInfo{l[1], l[0]}
		}, nil},
		{"only the digest of a container changed", func(l []*proto.ContainerInfo) []*proto.ContainerInfo {
			l[0].ImageDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
			return l
		}, nil},
		{"container removed", func(l []*proto.ContainerInfo) []*proto.ContainerInfo { return l[:1] }, reload},
		{"container added", func(l []*proto.ContainerInfo) []*proto.ContainerInfo {
			return append(l, &proto.ContainerInfo{Id: "id-cache", Name: "cache", Image: "redis:7", State: "running"})
		}, reload},
		{"container renamed", func(l []*proto.ContainerInfo) []*proto.ContainerInfo {
			l[1].Name = "database"
			return l
		}, reload},
		{"one removed and another created in its place", func(l []*proto.ContainerInfo) []*proto.ContainerInfo {
			l[1] = &proto.ContainerInfo{Id: "id-cache", Name: "cache", Image: "redis:7", State: "running"}
			return l
		}, reload},
		{"container stopped", func(l []*proto.ContainerInfo) []*proto.ContainerInfo {
			l[1].State = "exited"
			return l
		}, []listEvent{{container: "db", hostID: "host-1"}}},
		{"container recreated under its name", func(l []*proto.ContainerInfo) []*proto.ContainerInfo {
			l[0].Id = "id-web-2"
			return l
		}, []listEvent{{container: "web", hostID: "host-1"}}},
		{"image reference changed", func(l []*proto.ContainerInfo) []*proto.ContainerInfo {
			l[0].Image = "nginx:2"
			return l
		}, []listEvent{{container: "web", hostID: "host-1"}}},
		{"both containers stopped", func(l []*proto.ContainerInfo) []*proto.ContainerInfo {
			l[0].State, l[1].State = "exited", "exited"
			return l
		}, []listEvent{{container: "web", hostID: "host-1"}, {container: "db", hostID: "host-1"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := journalTestServer(t, nil)
			registerTestHost(t, srv, "host-1", "edge")
			registerTestHost(t, srv, "host-2", "other")
			srv.registry.UpdateContainers("host-1", protoToContainers(knownList()))
			srv.registry.UpdateContainers("host-2", protoToContainers(knownList()))

			ch, cancel := srv.bus.Subscribe()
			defer cancel()
			cl := &proto.ContainerList{Containers: tc.edit(knownList())}
			srv.handleContainerList("host-1", &proto.AgentMessage{Payload: &proto.AgentMessage_ContainerList{ContainerList: cl}}, cl)

			got := stateEvents(ch)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("container_state events = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A list that changes more rows than a dashboard should fetch one by one
// reloads it once instead (the SSE buffer of each browser is 64 events).
func TestHandleContainerList_ManyChangedRowsReloadOnce(t *testing.T) {
	srv := journalTestServer(t, nil)
	registerTestHost(t, srv, "host-1", "edge")
	var before, after []*proto.ContainerInfo
	for i := range maxRowEvents + 1 {
		name := fmt.Sprintf("app%d", i)
		before = append(before, &proto.ContainerInfo{Id: "id-" + name, Name: name, Image: "nginx:1", State: "running"})
		after = append(after, &proto.ContainerInfo{Id: "id-" + name, Name: name, Image: "nginx:1", State: "exited"})
	}
	srv.registry.UpdateContainers("host-1", protoToContainers(before))

	ch, cancel := srv.bus.Subscribe()
	defer cancel()
	cl := &proto.ContainerList{Containers: after}
	srv.handleContainerList("host-1", &proto.AgentMessage{Payload: &proto.AgentMessage_ContainerList{ContainerList: cl}}, cl)

	if got := stateEvents(ch); fmt.Sprint(got) != fmt.Sprint([]listEvent{{hostID: "host-1"}}) {
		t.Errorf("container_state events = %+v, want one reload of host-1", got)
	}
}

// An agent that could not list its containers changed nothing the hub
// knows, so nothing is published.
func TestHandleContainerList_ErrorReplyPublishesNothing(t *testing.T) {
	srv := journalTestServer(t, nil)
	registerTestHost(t, srv, "host-1", "edge")
	srv.registry.UpdateContainers("host-1", protoToContainers(knownList()))

	ch, cancel := srv.bus.Subscribe()
	defer cancel()
	cl := &proto.ContainerList{Error: "Cannot connect to the Docker daemon"}
	srv.handleContainerList("host-1", &proto.AgentMessage{Payload: &proto.AgentMessage_ContainerList{ContainerList: cl}}, cl)

	if got := stateEvents(ch); len(got) != 0 {
		t.Errorf("container_state events = %+v after an error reply, want none", got)
	}
}

// The first list of a session comes in the agent's state report. A change
// made while the agent was away (its host restarted, say) reaches an open
// dashboard as one from a later list does.
func TestReportState_PublishesWhatTheDashboardShows(t *testing.T) {
	srv, addr, _, bus := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	client := proto.NewAgentServiceClient(agentTLSConn(t, addr, certPEM, keyPEM, caPEM))
	report := func(list []*proto.ContainerInfo) []listEvent {
		t.Helper()
		ch, cancel := bus.Subscribe()
		defer cancel()
		ctx, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if _, err := client.ReportState(ctx, &proto.StateReport{HostId: hostID, Containers: list}); err != nil {
			t.Fatalf("ReportState: %v", err)
		}
		return stateEvents(ch)
	}

	if got := report(knownList()); fmt.Sprint(got) != fmt.Sprint([]listEvent{{hostID: hostID}}) {
		t.Errorf("first report: container_state events = %+v, want one reload of %s", got, hostID)
	}
	if got := report(knownList()); len(got) != 0 {
		t.Errorf("same report again: container_state events = %+v, want none", got)
	}
	stopped := knownList()
	stopped[0].State = "exited"
	if got := report(stopped); fmt.Sprint(got) != fmt.Sprint([]listEvent{{container: "web", hostID: hostID}}) {
		t.Errorf("web stopped: container_state events = %+v, want a refresh of web on %s", got, hostID)
	}
}
