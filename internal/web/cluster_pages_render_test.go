package web

import (
	"strings"
	"testing"
)

func TestClusterPage_WarnsWhenEngineIsShared(t *testing.T) {
	host := ClusterHost{ID: "a", Name: "alpha", State: "active", Connected: true, EngineSharedWith: []string{"beta"}}
	out := renderPage(t, "cluster.html", pageData{Page: "cluster", ClusterEnabled: true, ClusterHosts: []ClusterHost{host}})
	if !strings.Contains(out, "also enrolled as") || !strings.Contains(out, "beta") {
		t.Errorf("shared-engine warning missing")
	}
	host.EngineSharedWith = nil
	out = renderPage(t, "cluster.html", pageData{Page: "cluster", ClusterEnabled: true, ClusterHosts: []ClusterHost{host}})
	if strings.Contains(out, "also enrolled as") {
		t.Errorf("warning shown for a host with no shared engine")
	}
}
