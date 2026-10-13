package web

import (
	"bytes"
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
