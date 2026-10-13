package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

// The activity log names a host by its name, not its 32-character ID, and a
// user by name, not ID.
func TestActivityLog_NamesHostsAndUsers(t *testing.T) {
	hostID := strings.Repeat("ab", 16)
	logger := &mockEventLogger{}
	cp := &mockClusterProviderWithContainers{
		hosts:      []ClusterHost{{ID: hostID, Name: "Agent One"}},
		connected:  []string{hostID},
		containers: []RemoteContainer{{Name: "web", Image: "nginx:1", HostID: hostID, HostName: "Agent One"}},
	}
	srv := remoteUpdateServer(cp, &recordingHistory{})
	srv.deps.EventLog = logger
	srv.deps.Restarter, srv.deps.Stopper, srv.deps.Starter = nopCtl{}, nopCtl{}, nopCtl{}

	for name, call := range map[string]func(*Server, http.ResponseWriter, *http.Request){
		"stop": (*Server).apiStop, "restart": (*Server).apiRestart, "start": (*Server).apiStart,
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/containers/web/"+name+"?host="+hostID, nil)
		r.SetPathValue("name", "web")
		call(srv, httptest.NewRecorder(), r)
	}
	if len(logger.entries) < 3 {
		t.Fatalf("expected three log entries, got %v", logger.entries)
	}
	for _, e := range logger.entries {
		if strings.Contains(e.Message, hostID) || !strings.Contains(e.Message, "Agent One") {
			t.Errorf("log line %q should name the host, not its ID", e.Message)
		}
	}

	msrv := newMFATestServer()
	msrv.deps.EventLog = logger
	admin := createTestUser(msrv.deps.Auth, "root", "Str0ngP@ssword!")
	bob := createTestUser(msrv.deps.Auth, "bob", "Str0ngP@ssword!")
	logger.entries = nil
	r := reqWithAuthContext(httptest.NewRequest(http.MethodPost, "/x", nil), &admin)
	r.SetPathValue("id", bob.ID)
	msrv.apiOIDCUnlink(httptest.NewRecorder(), r)
	if len(logger.entries) != 1 || !strings.Contains(logger.entries[0].Message, "bob") || strings.Contains(logger.entries[0].Message, bob.ID) {
		t.Errorf("unlink log = %v, want the user named, not their ID", logger.entries)
	}
	_ = auth.User{}
}

type nopCtl struct{}

func (nopCtl) RestartContainer(context.Context, string) error { return nil }
func (nopCtl) StopContainer(context.Context, string) error    { return nil }
func (nopCtl) StartContainer(context.Context, string) error   { return nil }
