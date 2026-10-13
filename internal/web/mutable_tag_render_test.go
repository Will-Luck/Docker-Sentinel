package web

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func renderQueue(t *testing.T, item PendingUpdate) string {
	t.Helper()
	s := &Server{}
	s.parseTemplates()
	data := pageData{Page: "queue", Queue: []PendingUpdate{item}, QueueSelfKeys: map[string]bool{}, QueueCount: 1}
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "queue.html", data); err != nil {
		t.Fatalf("render queue.html: %v", err)
	}
	return buf.String()
}

func TestQueuePage_MutableTagShowsTargetAndIgnore(t *testing.T) {
	base := PendingUpdate{ContainerName: "web", CurrentImage: "nginx:latest", RemoteDigest: "sha256:abc", DetectedAt: time.Now()}

	resolved := base
	resolved.ResolvedTargetVersion = "1.29.2"
	out := renderQueue(t, resolved)
	if !strings.Contains(out, "1.29.2") || !strings.Contains(out, "ignoreUpdate(") {
		t.Errorf("resolved row should show 1.29.2 and an Ignore button")
	}

	out = renderQueue(t, base)
	if !strings.Contains(out, "new digest") || !strings.Contains(out, "ignoreUpdate(") {
		t.Errorf("digest row should show a new digest label and an Ignore button")
	}
}

func TestDashboardRow_MutableTagShowsTarget(t *testing.T) {
	s := &Server{}
	s.parseTemplates()
	render := func(v containerView) string {
		var buf bytes.Buffer
		if err := s.tmpl.ExecuteTemplate(&buf, "container-row", v); err != nil {
			t.Fatalf("render: %v", err)
		}
		return buf.String()
	}
	base := containerView{Name: "web", Image: "nginx:latest", Tag: "latest", HasUpdate: true, DigestOnly: true}
	if out := render(base); !strings.Contains(out, "new digest") {
		t.Errorf("digest-only row should say new digest")
	}
	base.ResolvedTarget = "1.29.2"
	if out := render(base); !strings.Contains(out, "1.29.2") {
		t.Errorf("row should show the resolved target 1.29.2")
	}
}
