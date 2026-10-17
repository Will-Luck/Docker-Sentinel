package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
)

// failingRollbackCluster fails every remote update and rollback with err.
type failingRollbackCluster struct {
	failingUpdateCluster
}

func (f *failingRollbackCluster) RollbackRemoteContainer(context.Context, string, string) error {
	return f.err
}

// A remote update or rollback the agent answered with a failure has its end
// told by the hub, with the agent's result. The dashboard that started it
// tells only what the hub could not: a refusal before anything was sent, or
// a request the agent never answered.
func TestRemoteFailure_ToldByTheDashboardOnlyWhenTheAgentDidNotAnswer(t *testing.T) {
	for _, kind := range []struct {
		name string
		err  error
		told bool
	}{
		{"answered", &cluster.AnsweredError{Err: errors.New("pull failed")}, false},
		{"refused", errors.New("host Agent One is paused: no new updates are sent to it"), true},
		{"unanswered", &cluster.UnansweredError{HostID: "h1", RequestID: "r1", Err: errors.New("agent disconnected")}, true},
	} {
		for _, path := range []struct {
			name, url, body string
			handler         func(*Server) http.HandlerFunc
		}{
			{"update now", "/api/update/web?host=h1", "", func(s *Server) http.HandlerFunc { return s.apiUpdate }},
			{"update to version", "/api/containers/web/update-to-version?host=h1", `{"tag":"2"}`, func(s *Server) http.HandlerFunc { return s.apiUpdateToVersion }},
			{"ghcr switch", "/api/containers/web/switch-ghcr?host=h1", "", func(s *Server) http.HandlerFunc { return s.apiSwitchToGHCR }},
			{"rollback", "/api/containers/web/rollback?host=h1", "", func(s *Server) http.HandlerFunc { return s.apiRollback }},
		} {
			t.Run(kind.name+", "+path.name, func(t *testing.T) {
				cp := &failingRollbackCluster{failingUpdateCluster{
					mockClusterProviderWithContainers: mockClusterProviderWithContainers{
						hosts: []ClusterHost{{ID: "h1", Name: "Agent One"}}, connected: []string{"h1"},
						containers: []RemoteContainer{{Name: "web", Image: "nginx:1", HostID: "h1", HostName: "Agent One"}},
					},
					err: kind.err,
				}}
				srv := remoteUpdateServer(cp, rollbackHistory{recs: []UpdateRecord{{Outcome: "success", OldImage: "nginx:0", NewImage: "nginx:1"}}})
				srv.remoteHold = 10 * time.Millisecond
				srv.deps.GHCRCache = fixedGHCR{}
				srv.deps.Rollback = okRollback{called: make(chan string, 1)}
				ch, cancel := srv.deps.EventBus.Subscribe()
				defer cancel()

				r := httptest.NewRequest(http.MethodPost, path.url, strings.NewReader(path.body))
				r.SetPathValue("name", "web")
				w := httptest.NewRecorder()
				path.handler(srv)(w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("status = %d: %s", w.Code, w.Body.String())
				}

				// An update's part ends with the row's state event, which
				// comes after its failure event. A rollback has none: its
				// failure event comes at once, or not at all.
				var told []events.SSEEvent
				deadline := time.After(3 * time.Second)
				quiet := time.After(200 * time.Millisecond)
				rollback := path.name == "rollback"
			collect:
				for {
					select {
					case e := <-ch:
						if e.Type == events.EventContainerUpdate && e.ContainerName == "web" && e.HostID == "h1" {
							told = append(told, e)
							if rollback {
								break collect
							}
						}
						if !rollback && e.Type == events.EventContainerState && e.ContainerName == "web" {
							break collect
						}
					case <-quiet:
						if rollback && !kind.told {
							break collect
						}
					case <-deadline:
						t.Fatalf("the dashboard's part did not end; events: %+v", told)
					}
				}
				if want := map[bool]int{true: 1, false: 0}[kind.told]; len(told) != want {
					t.Errorf("container_update events from the dashboard = %+v, want %d", told, want)
				}
				if kind.told && len(told) == 1 && !strings.Contains(told[0].Message, kind.err.Error()) {
					t.Errorf("failure event %q does not say why: %v", told[0].Message, kind.err)
				}
			})
		}
	}
}
