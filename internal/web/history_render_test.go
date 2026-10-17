package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func renderPage(t *testing.T, name string, data pageData) string {
	t.Helper()
	s := &Server{}
	s.parseTemplates()
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return buf.String()
}

func TestHistoryPage_RemoteRowShowsPlainNameAndHostLink(t *testing.T) {
	now := time.Now()
	out := renderPage(t, "history.html", pageData{Page: "history", History: []UpdateRecord{
		{Timestamp: now, ContainerName: "agent1::whoami", HostID: "agent1", HostName: "Agent One", Outcome: "success"},
		{Timestamp: now, ContainerName: "nginx", Outcome: "success"},
	}})
	if strings.Contains(out, ">agent1::whoami<") {
		t.Errorf("remote row shows the scoped key as its name")
	}
	if !strings.Contains(out, `href="/container/whoami?host=agent1"`) || !strings.Contains(out, ">whoami</a>") {
		t.Errorf("remote row should link to /container/whoami?host=agent1 with the plain name")
	}
	if !strings.Contains(out, "Agent One") {
		t.Errorf("host badge missing")
	}
	if !strings.Contains(out, `href="/container/nginx"`) || !strings.Contains(out, ">nginx</a>") {
		t.Errorf("local row must still render with a plain link")
	}
}

func TestLoginPage_ErrorElementHasAnID(t *testing.T) {
	srv := &Server{}
	srv.parseTemplates()
	render := func(errText string) string {
		var buf bytes.Buffer
		if err := srv.tmpl.ExecuteTemplate(&buf, "login.html", map[string]any{"Error": errText}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	if out := render("Invalid username or password"); !strings.Contains(out, `id="login-error" class="login-error">Invalid username or password<`) {
		t.Errorf("server-rendered error should sit in #login-error and be visible")
	}
	if out := render(""); !strings.Contains(out, `id="login-error" class="login-error" style="display: none;"`) {
		t.Errorf("without an error, #login-error should exist and be hidden")
	}
}

// The CSP only allows fonts from this origin, so no page may link Google
// Fonts, and the font files the stylesheet names must be served.
func TestFontsAreSelfHosted(t *testing.T) {
	entries, err := staticFS.ReadDir("static")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		b, _ := staticFS.ReadFile("static/" + e.Name())
		if strings.Contains(string(b), "fonts.googleapis.com") || strings.Contains(string(b), "fonts.gstatic.com") {
			t.Errorf("%s still links Google Fonts, which the CSP blocks", e.Name())
		}
	}
	css, _ := staticFS.ReadFile("static/style.css")
	for _, f := range []string{"/static/fonts/roboto-latin.woff2", "/static/fonts/roboto-mono-latin.woff2"} {
		if !strings.Contains(string(css), f) {
			t.Errorf("style.css has no @font-face for %s", f)
		}
		srv := &Server{}
		w := httptest.NewRecorder()
		srv.serveStaticFile(w, httptest.NewRequest(http.MethodGet, f, nil))
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "font/woff2" || w.Body.Len() < 1000 {
			t.Errorf("%s: status %d, type %q, %d bytes", f, w.Code, w.Header().Get("Content-Type"), w.Body.Len())
		}
	}
}

// Clicking a history row opens its details; it used to navigate to
// /container/history-N, which does not exist.
func TestHistoryPage_RowClickTogglesDetails(t *testing.T) {
	out := renderPage(t, "history.html", pageData{Page: "history", History: []UpdateRecord{
		{Timestamp: time.Now(), ContainerName: "nginx", Outcome: "failed", Error: "pull failed"},
	}})
	if strings.Contains(out, "onRowClick(event, 'history-") {
		t.Errorf("history rows still use the dashboard row click")
	}
	if !strings.Contains(out, "toggleHistoryDetail(event,  0 )") || !strings.Contains(out, `id="accordion-history-0"`) {
		t.Errorf("history row should toggle its own detail panel")
	}
	if strings.Contains(out, "existingCount") {
		t.Errorf("loaded rows must not reuse server-rendered panel ids")
	}
	if !strings.Contains(out, "r.host_id") {
		t.Errorf("loaded rows should link to the host's container page")
	}
}

// A rollback asked for from the dashboard is not a failed update: it gets a
// neutral badge, while an automatic rollback keeps the red one. A row written
// by v3.0.1 has no new field, so it is told apart by its error text alone.
func TestHistoryPage_ManualRollbackBadgeIsNeutral(t *testing.T) {
	out := renderPage(t, "history.html", pageData{Page: "history", History: []UpdateRecord{
		{Timestamp: time.Now(), ContainerName: "manual", Outcome: "rollback", Error: "rolled back on request"},
		{Timestamp: time.Now().Add(-time.Minute), ContainerName: "auto", Outcome: "rollback", Error: "health check failed"},
	}})
	const manual = `<span class="badge badge-muted" title="Rolled back on request, not a failed update">Rolled Back</span>`
	const auto = `<span class="badge badge-error" title="Update failed health check, restored previous version">Rolled Back</span>`
	if strings.Count(out, manual) != 1 {
		t.Errorf("want exactly one neutral manual-rollback badge, got %d", strings.Count(out, manual))
	}
	if strings.Count(out, auto) != 1 {
		t.Errorf("want exactly one red automatic-rollback badge, got %d", strings.Count(out, auto))
	}
}

// The container and service pages tell the two rollbacks apart the same way.
func TestContainerAndServicePages_ManualRollbackBadgeIsNeutral(t *testing.T) {
	history := []UpdateRecord{
		{Timestamp: time.Now(), ContainerName: "app", Outcome: "rollback", Error: "rolled back on request"},
		{Timestamp: time.Now().Add(-time.Minute), ContainerName: "app", Outcome: "rollback", Error: "health check failed"},
	}
	const manual = `<span class="badge badge-muted" title="Rolled back on request, not a failed update">Rolled Back</span>`
	const auto = `<span class="badge badge-error" title="Update failed health check, restored previous version">Rolled Back</span>`
	s := &Server{}
	s.parseTemplates()
	for _, page := range []struct {
		name string
		data any
	}{
		{"container.html", containerDetailData{Container: containerView{Name: "app", Image: "acme/app:1.0", State: "running"}, History: history}},
		{"service.html", serviceDetailData{Service: serviceView{Name: "app", Image: "acme/app:1.0"}, History: history}},
	} {
		var buf bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&buf, page.name, page.data); err != nil {
			t.Fatalf("render %s: %v", page.name, err)
		}
		out := buf.String()
		if n := strings.Count(out, manual); n != 1 {
			t.Errorf("%s: want exactly one neutral manual-rollback badge, got %d", page.name, n)
		}
		if n := strings.Count(out, auto); n != 1 {
			t.Errorf("%s: want exactly one red automatic-rollback badge, got %d", page.name, n)
		}
	}
}

// buttonsLabelled returns the markup of every button whose text is label.
func buttonsLabelled(out, label string) []string {
	return regexp.MustCompile(`<button[^>]*>\s*`+label+`\s*</button>`).FindAllString(out, -1)
}

// Both Reject buttons (Sentinel's own update and any other) explain what
// Reject does, and the Ignore button names the version it skips.
func TestQueuePage_RejectAndIgnoreTooltips(t *testing.T) {
	out := renderPage(t, "queue.html", pageData{Page: "queue",
		Queue: []PendingUpdate{
			{ContainerName: "sentinel", CurrentImage: "acme/sentinel:3.0", NewerVersions: []string{"3.1"}},
			{ContainerName: "web", CurrentImage: "acme/web:1.0", NewerVersions: []string{"1.1"}},
		},
		QueueSelfKeys: map[string]bool{"sentinel": true},
	})
	const tip = `title="Dismiss until the next scan. To stop seeing this version, use Ignore."`
	rejects := buttonsLabelled(out, "Reject")
	if len(rejects) != 2 {
		t.Fatalf("found %d Reject buttons, want 2 (self and ordinary row)", len(rejects))
	}
	for _, b := range rejects {
		if !strings.Contains(b, tip) {
			t.Errorf("Reject button without the tooltip: %s", b)
		}
	}
	ignores := buttonsLabelled(out, "Ignore")
	if len(ignores) != 1 {
		t.Fatalf("found %d Ignore buttons, want 1 (the ordinary row)", len(ignores))
	}
	if !strings.Contains(ignores[0], `title="Skip version 1.1: re-alerts when a newer one appears"`) {
		t.Errorf("Ignore button without its tooltip: %s", ignores[0])
	}
}
