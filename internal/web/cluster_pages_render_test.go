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

// A long server version string must wrap inside its card, not overflow it.
func TestClusterPage_ServerCardValueIsTextSized(t *testing.T) {
	out := renderPage(t, "cluster.html", pageData{Page: "cluster", ClusterEnabled: true, ServerVersion: "v3.0.0-rc1-123-gabcdef0123456789-dirty"})
	if !strings.Contains(out, `class="stat-value stat-value-text" id="stat-version"`) {
		t.Errorf("server version value should use the text-sized style")
	}
	css, _ := staticFS.ReadFile("static/style.css")
	if !strings.Contains(string(css), "overflow-wrap: anywhere") || !strings.Contains(string(css), ".stat-value-text") {
		t.Errorf("stylesheet lacks the wrap and text-size rules")
	}
}

// A remote container's notification preference is read and written under
// its host, not under the hub's container of the same name.
func TestContainerPage_NotifyPrefIsHostScoped(t *testing.T) {
	out := renderAny(t, "container.html", containerDetailData{Container: containerView{Name: "whoami", HostID: "h1"}})
	if strings.Contains(out, `"/notify-pref", {`) {
		t.Errorf("a notify-pref request is built without the host")
	}
	if !strings.Contains(out, `"?host=" + encodeURIComponent(_containerHostId)`) || strings.Count(out, "notifyPrefURL()") < 2 {
		t.Errorf("both notify-pref requests should use the host-scoped URL")
	}
}

func renderAny(t *testing.T, name string, data any) string {
	t.Helper()
	s := &Server{}
	s.parseTemplates()
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return buf.String()
}

// The row checkbox is announced with the host's name, not its 32-character ID.
func TestDashboardRow_CheckboxNamesTheHost(t *testing.T) {
	s := &Server{}
	s.parseTemplates()
	render := func(v containerView) string {
		var buf bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&buf, "container-row", v); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	id := strings.Repeat("ab", 16)
	out := render(containerView{Name: "web", Image: "nginx:1", Tag: "1", State: "running", HostID: id, HostName: "Agent One"})
	if !strings.Contains(out, `aria-label="Select web on Agent One"`) {
		t.Errorf("checkbox label should use the host name")
	}
	out = render(containerView{Name: "web", Image: "nginx:1", Tag: "1", State: "running", HostID: id})
	if !strings.Contains(out, `aria-label="Select web on `+id+`"`) {
		t.Errorf("without a name the label falls back to the host ID")
	}
}

// On a phone the dashboard hides the secondary columns by class, <col> included
// (the table is fixed-layout, so a hidden cell with a live <col> squeezes the
// Name column away), and never by cell position.
func TestResponsiveCSS_DashboardColumnsCollapseByClass(t *testing.T) {
	b, _ := staticFS.ReadFile("static/style.css")
	css := string(b)
	if strings.Contains(css, "#container-table th:nth-child(3)") || strings.Contains(css, "#container-table td:nth-child(4)") {
		t.Error("dashboard columns are still hidden by cell position")
	}
	for _, want := range []string{"#container-table .col-image", "#container-table .col-policy", "#container-table col.col-status"} {
		if !strings.Contains(css, want) {
			t.Errorf("stylesheet lacks %q", want)
		}
	}
}

// An agent version that already starts with v is not shown as "vv3.0.0", and a
// long commit part stays inside the card.
func TestClusterPage_AgentVersionShownOnce(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"v3.0.0-rc4 (5c0ffee1234567890abcdef)", ">v3.0.0-rc4<"},
		{"3.0.0", ">v3.0.0<"},
	} {
		host := ClusterHost{ID: "a", Name: "alpha", State: "active", Connected: true, AgentVersion: tc.in}
		out := renderPage(t, "cluster.html", pageData{Page: "cluster", ClusterEnabled: true, ClusterHosts: []ClusterHost{host}})
		if strings.Contains(out, "vv3") || !strings.Contains(out, tc.want) {
			t.Errorf("%q: want %s and no double v", tc.in, tc.want)
		}
		if !strings.Contains(out, `title="`+tc.in+`"`) {
			t.Errorf("%q: the full version should be in a tooltip", tc.in)
		}
	}
}

// A remote container's page shows its container ID and names its host; with no
// ID known it leaves the row out instead of showing it blank.
func TestContainerPage_RemoteShowsIDAndHost(t *testing.T) {
	id := strings.Repeat("ab", 32)
	out := renderAny(t, "container.html", containerDetailData{Container: containerView{Name: "whoami", ID: id, HostID: "h1", HostName: "Agent One"}})
	if !strings.Contains(out, `title="`+id+`"`) {
		t.Errorf("container ID missing")
	}
	if !strings.Contains(out, "Agent One") {
		t.Errorf("the page does not name the host")
	}
	out = renderAny(t, "container.html", containerDetailData{Container: containerView{Name: "whoami", HostID: "h1", HostName: "Agent One"}})
	if strings.Contains(out, `id="container-id"`) {
		t.Errorf("a blank container ID row is shown")
	}
}

// A Swarm task's tag is taken from its image reference by the same rule as
// everywhere else, so a registry with a port ("192.168.1.60:5443/lh/app:1.0.1")
// shows 1.0.1 and not "5443/lh/app:1.0.1".
func TestTaskTag_RegistryWithPort(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.60:5443/lh/app:1.0.1": "1.0.1",
		"nginx:1.25":                     "1.25",
	} {
		if got := taskTag(in, "5443/lh/app:1.0.1"); got != want {
			t.Errorf("taskTag(%q) = %q, want %q", in, got, want)
		}
	}
	if got := taskTag("", "fallback"); got != "fallback" {
		t.Errorf("no image: %q, want the reported tag", got)
	}
}

// The service page uses the same option styles as the container page, so its
// Policy and Notification sections are not unstyled.
func TestServicePage_UsesTheContainerPageOptionStyles(t *testing.T) {
	b, _ := staticFS.ReadFile("static/service.html")
	if strings.Contains(string(b), "policy-card") || strings.Contains(string(b), "notify-pref-cards") {
		t.Error("service.html still uses classes that have no styles")
	}
	css, _ := staticFS.ReadFile("static/style.css")
	if !strings.Contains(string(b), `class="notify-option"`) || !strings.Contains(string(css), ".notify-option") {
		t.Error("service.html should use the styled notify-option markup")
	}
}

// The container and service pages show the pending-updates count in the nav,
// like the other pages.
func TestDetailPages_ShowTheQueueBadge(t *testing.T) {
	c := renderAny(t, "container.html", containerDetailData{Container: containerView{Name: "web"}, QueueCount: 7})
	s := renderAny(t, "service.html", serviceDetailData{Service: serviceView{Name: "svc"}, QueueCount: 7})
	for name, out := range map[string]string{"container": c, "service": s} {
		if !strings.Contains(out, `<span class="nav-badge">7</span>`) {
			t.Errorf("%s page has no queue badge", name)
		}
	}
	if out := renderAny(t, "container.html", containerDetailData{Container: containerView{Name: "web"}}); strings.Contains(out, "nav-badge") {
		t.Errorf("an empty queue should show no badge")
	}
}

// With the secondary columns collapsed at phone width the Name column takes
// the rest of the table instead of its fixed 20%.
func TestResponsiveCSS_NameColumnGetsTheRestOnPhones(t *testing.T) {
	css, _ := staticFS.ReadFile("static/style.css")
	if !strings.Contains(string(css), "#container-table col.col-name") {
		t.Error("stylesheet lacks the Name column rule for small screens")
	}
	// A fixed-layout table keeps a share for hidden columns, so the phone
	// layouts switch to automatic layout.
	i := strings.Index(string(css), "@media (max-width: 768px)")
	if i < 0 || !strings.Contains(string(css)[i:], "table-layout: auto") || !strings.Contains(string(css)[i:], ".queue-table col:nth-child(4)") {
		t.Error("the tablet and phone layout does not switch the dashboard and queue tables to automatic layout")
	}
	if out := renderAny(t, "container-row", containerView{Name: "web", Image: "nginx:1", Tag: "1", State: "running"}); !strings.Contains(out, `class="container-link" title="web"`) {
		t.Error("a truncated name has no tooltip")
	}
	out := renderPage(t, "index.html", pageData{Page: "dashboard", ColumnVisible: map[string]bool{"image": true, "policy": true, "status": true, "ports": true}})
	if !strings.Contains(out, `<col class="col-name"`) {
		t.Error("the Name <col> has no col-name class")
	}
}
