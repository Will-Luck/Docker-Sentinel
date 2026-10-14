package web

import (
	"bytes"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"strings"
	"testing"
	"time"
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

// A remote container's page offers Rollback when the hub knows what to go back
// to, naming the host and the image in the confirm; with no target it offers
// nothing.
func TestContainerPage_RemoteRollbackButton(t *testing.T) {
	view := containerView{Name: "whoami", HostID: "h1", HostName: "Agent One"}
	out := renderAny(t, "container.html", containerDetailData{Container: view, RemoteRollbackImage: "traefik/whoami:v1"})
	if !strings.Contains(out, "triggerRemoteRollback(") || !strings.Contains(out, "Agent One") || !strings.Contains(out, "whoami:v1") {
		t.Errorf("remote page with a target should offer Rollback naming the host and image")
	}
	out = renderAny(t, "container.html", containerDetailData{Container: view})
	if strings.Contains(out, "triggerRemoteRollback(") || strings.Contains(out, ">Rollback</button>") {
		t.Errorf("remote page with no target must not offer Rollback")
	}
	// The local button is unchanged.
	out = renderAny(t, "container.html", containerDetailData{Container: containerView{Name: "web"}, HasSnapshot: true})
	if !strings.Contains(out, "triggerRollback('web', event)") {
		t.Errorf("local Rollback button missing")
	}
}

type rollbackHistory struct {
	nopHistory
	recs []UpdateRecord
}

func (h rollbackHistory) ListHistoryByContainer(string, int) ([]UpdateRecord, error) {
	return h.recs, nil
}

// The target is the old image of the newest update that has not been rolled
// back already: each manual rollback row cancels the update before it.
func TestRemoteRollbackTarget(t *testing.T) {
	up := func(old, nw string) UpdateRecord {
		return UpdateRecord{Outcome: "success", OldImage: old, NewImage: nw}
	}
	rb := func(from, to string) UpdateRecord {
		return UpdateRecord{Outcome: "rollback", Error: cluster.RolledBackOnRequest, OldImage: from, NewImage: to}
	}
	for name, tc := range map[string]struct {
		recs []UpdateRecord // newest first
		want string
	}{
		"one update":                     {[]UpdateRecord{up("x:1", "x:2")}, "x:1"},
		"no history":                     {nil, ""},
		"rolled back, nothing older":     {[]UpdateRecord{rb("x:2", "x:1"), up("x:1", "x:2")}, ""},
		"rolled back, an older update":   {[]UpdateRecord{rb("x:2", "x:1"), up("x:1", "x:2"), up("x:0", "x:1")}, "x:0"},
		"rolled back twice":              {[]UpdateRecord{rb("x:1", "x:0"), rb("x:2", "x:1"), up("x:1", "x:2"), up("x:0", "x:1")}, ""},
		"updated again after a rollback": {[]UpdateRecord{up("x:1", "x:3"), rb("x:2", "x:1"), up("x:1", "x:2")}, "x:1"},
		"pre-3.0 rollback row":           {[]UpdateRecord{{Outcome: "success", OldImage: "x:2", NewImage: "x:1", Type: "rollback"}, up("x:1", "x:2")}, ""},
		"pre-3.0 rollback, older update": {[]UpdateRecord{{Outcome: "success", OldImage: "x:2", NewImage: "x:1", Type: "rollback"}, up("x:1", "x:2"), up("x:0", "x:1")}, "x:0"},
		"lh-rg-61 history": {[]UpdateRecord{rb("x:1", "x:0"), up("x:0", "x:1"), rb("x:1", "x:0"), up("x:0", "x:1"),
			{Outcome: "success", OldImage: "x:1", NewImage: "x:0", Type: "rollback"}, up("x:0", "x:1")}, ""},
		"failed and imageless rows": {[]UpdateRecord{{Outcome: "failed", OldImage: "x:9"}, {Outcome: "success"}, up("x:1", "x:2")}, "x:1"},
	} {
		srv := &Server{deps: Dependencies{Store: rollbackHistory{recs: tc.recs}}}
		if got := srv.remoteRollbackTarget("h1", "web"); got != tc.want {
			t.Errorf("%s: target = %q, want %q", name, got, tc.want)
		}
	}
}

// The local Rollback button is hidden while the newest image change is a
// rollback: the container already runs what the snapshot holds.
func TestLocalRollbackOffered(t *testing.T) {
	rb := UpdateRecord{Outcome: "rollback", Error: cluster.RolledBackOnRequest}
	up := UpdateRecord{Outcome: "success", OldImage: "x:1", NewImage: "x:2"}
	for name, tc := range map[string]struct {
		recs []UpdateRecord
		want bool
	}{
		"after an update":              {[]UpdateRecord{up}, true},
		"just rolled back":             {[]UpdateRecord{rb, up}, false},
		"updated again after rollback": {[]UpdateRecord{up, rb, up}, true},
		"no history":                   {nil, true},
	} {
		srv := &Server{deps: Dependencies{Store: rollbackHistory{recs: tc.recs}}}
		if got := srv.localRollbackOffered("web"); got != tc.want {
			t.Errorf("%s: offered = %v, want %v", name, got, tc.want)
		}
	}
}

// On a phone the queue table drops its Image and Detected columns, so the
// version is also shown under the name, and the history table drops Version
// and Duration; both lay out automatically so they fit with no sideways scroll.
func TestPhoneLayout_QueueAndHistoryFit(t *testing.T) {
	q := []PendingUpdate{{ContainerName: "web", CurrentImage: "app:latest", RemoteDigest: "sha256:y", ResolvedTargetVersion: "1.26", DetectedAt: time.Now()}}
	out := renderPage(t, "queue.html", pageData{Page: "queue", Queue: q, QueueSelfKeys: map[string]bool{}, QueueCount: 1})
	if strings.Count(out, `class="version-new"`) != 2 || !strings.Contains(out, `class="queue-version-inline mono"`) {
		t.Errorf("the queue row should show its version in the Image cell and under the name")
	}
	if !strings.Contains(out, `class="container-link" title="web"`) {
		t.Errorf("the queue name has no tooltip")
	}
	h := []UpdateRecord{{Timestamp: time.Now(), ContainerName: "web", Outcome: "success"}}
	if out := renderPage(t, "history.html", pageData{Page: "history", History: h}); !strings.Contains(out, `class="container-link" title="web"`) {
		t.Errorf("the history name has no tooltip")
	}
	b, _ := staticFS.ReadFile("static/style.css")
	css := string(b)
	i := strings.Index(css, "@media (max-width: 768px)")
	if i < 0 {
		t.Fatal("no tablet media block")
	}
	for _, want := range []string{".queue-version-inline", ".queue-table td:last-child .btn-group", ".table-history .hist-duration", ".table-history .badge"} {
		if !strings.Contains(css[i:], want) {
			t.Errorf("the phone layout lacks %q", want)
		}
	}
}

// The name and its self badge sit in a flex cell so a long name ends in an
// ellipsis without swallowing the badge, and a wide status badge is capped.
func TestDashboardRow_NameCellAndBadgeCaps(t *testing.T) {
	out := renderAny(t, "container-row", containerView{Name: "sentinel", Image: "s:1", Tag: "1", State: "running", IsSelf: true, HasUpdate: true})
	if !strings.Contains(out, `<div class="name-cell">`) || !strings.Contains(out, `badge badge-muted name-cell-badge">self`) {
		t.Errorf("the name and self badge should sit in a name-cell")
	}
	if !strings.Contains(out, `title="Update Sentinel">Update Sentinel`) {
		t.Errorf("the Update Sentinel badge has no tooltip")
	}
	b, _ := staticFS.ReadFile("static/style.css")
	if !strings.Contains(string(b), ".name-cell-badge") {
		t.Errorf("stylesheet lacks the name-cell badge rule")
	}
	// A status badge keeps the badge's own display: overriding it put the dot
	// on a line of its own above the text.
	if strings.Contains(string(b), "col-status > .badge") {
		t.Errorf("a rule overrides how status badges lay out their dot and text")
	}
}

// On a phone the history hides Version and Duration by class, so a scan
// summary row (which has a different cell layout) keeps its Summary badge, and
// it carries a narrow copy of its text that lines up with the other rows.
func TestHistoryPage_PhoneColumnsHideByClass(t *testing.T) {
	h := []UpdateRecord{
		{Timestamp: time.Now(), Outcome: "scan_summary", Type: "scan", Error: "3 checked"},
		{Timestamp: time.Now(), ContainerName: "web", OldImage: "a:1", NewImage: "a:2", Outcome: "success"},
	}
	out := renderPage(t, "history.html", pageData{Page: "history", History: h})
	for _, want := range []string{`<th class="hist-version">`, `<th class="hist-duration">`, `class="mono hist-duration"`, `class="cell-image mono hist-version"`,
		`scan-summary-text scan-summary-wide`, `scan-summary-text scan-summary-narrow`} {
		if !strings.Contains(out, want) {
			t.Errorf("history page lacks %s", want)
		}
	}
	b, _ := staticFS.ReadFile("static/style.css")
	css := string(b)
	i := strings.Index(css, "@media (max-width: 768px)")
	if i < 0 || !strings.Contains(css[i:], ".table-history .hist-version,") || !strings.Contains(css[i:], ".table-history td.scan-summary-narrow") {
		t.Errorf("the phone layout does not hide history columns by class")
	}
}
