package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The browser writes a row refresh's counts into the dashboard's stat tiles
// (updateContainerRow in sse.js), so the row endpoint must count the whole
// fleet as /api/stats does, whichever host the row is on.
func TestContainerRow_StatsAreTheDashboardStats(t *testing.T) {
	docker := &mockContainerLister{containers: []ContainerSummary{
		{ID: "c1", Names: []string{"/nginx"}, Image: "nginx:1", State: "running"},
		{ID: "c2", Names: []string{"/redis"}, Image: "redis:7", State: "exited"},
	}}
	cc := NewClusterController()
	cc.SetProvider(&mockClusterProviderWithContainers{
		hosts:     []ClusterHost{{ID: "h1", Name: "remote-host"}},
		connected: []string{"h1"},
		containers: []RemoteContainer{
			{Name: "app", Image: "app:1", State: "running", HostID: "h1", HostName: "remote-host"},
			{Name: "db", Image: "postgres:16", State: "running", HostID: "h1", HostName: "remote-host"},
			{Name: "job", Image: "busybox:1", State: "exited", HostID: "h1", HostName: "remote-host"},
		},
	})
	queue := &mockQueue{items: []PendingUpdate{
		{ContainerName: "nginx", CurrentImage: "nginx:1"},
		{ContainerName: "app", CurrentImage: "app:1", HostID: "h1", HostName: "remote-host"},
		{ContainerName: "db", CurrentImage: "postgres:16", HostID: "h1", HostName: "remote-host"},
	}}
	srv := newDashboardTestServer(docker, newMockHistoryStore(), queue, nil, nil, cc)
	srv.parseTemplates()

	w := httptest.NewRecorder()
	srv.handleDashboardStats(w, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	want := decodeMap(t, w)
	// The fleet's counts, unlike this host's (2 containers, 1 running, 1 pending).
	if want["total"] != 5.0 || want["running"] != 3.0 || want["pending"] != 3.0 {
		t.Fatalf("/api/stats = %v, want total 5, running 3, pending 3", want)
	}

	for _, tc := range []struct{ name, container, url string }{
		{"a container on this host", "nginx", "/api/containers/nginx/row"},
		{"a container on an agent host", "app", "/api/containers/app/row?host=h1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.url, nil)
			r.SetPathValue("name", tc.container)
			w := httptest.NewRecorder()
			srv.handleContainerRow(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
			}
			got := decodeMap(t, w)
			if got["html"] == "" {
				t.Error("the row came back with no HTML")
			}
			for _, k := range []string{"total", "running", "pending"} {
				if got[k] != want[k] {
					t.Errorf("row %s = %v, want %v as /api/stats has it", k, got[k], want[k])
				}
			}
		})
	}
}
