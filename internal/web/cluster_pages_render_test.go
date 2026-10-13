package web

import (
	"bytes"
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

// The Sentinel container's own row used to say "Running" whatever its state.
func TestDashboardRow_SelfRowShowsRealState(t *testing.T) {
	s := &Server{}
	s.parseTemplates()
	render := func(state string) string {
		var buf bytes.Buffer
		v := containerView{Name: "sentinel", Image: "sentinel:1", Tag: "1", State: state, IsSelf: true}
		if err := s.tmpl.ExecuteTemplate(&buf, "container-row", v); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	if out := render("running"); !strings.Contains(out, ">Running<") {
		t.Errorf("running self row should say Running")
	}
	out := render("exited")
	if strings.Contains(out, ">Running<") || !strings.Contains(out, ">exited<") {
		t.Errorf("exited self row should say exited, not Running")
	}
}
