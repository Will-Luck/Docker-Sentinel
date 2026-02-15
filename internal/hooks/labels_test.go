package hooks

import (
	"testing"
)

func TestReadLabelsPreAndPost(t *testing.T) {
	labels := map[string]string{
		"sentinel.hook.pre-update":  "pg_dump -f /backup/db.sql",
		"sentinel.hook.post-update": "pg_restore /backup/db.sql",
		"sentinel.hook.timeout":     "60",
	}

	hooks := ReadLabels("postgres", labels)
	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(hooks))
	}

	var pre, post *Hook
	for i := range hooks {
		switch hooks[i].Phase {
		case "pre-update":
			pre = &hooks[i]
		case "post-update":
			post = &hooks[i]
		}
	}

	if pre == nil {
		t.Fatal("missing pre-update hook")
	}
	if pre.Timeout != 60 {
		t.Errorf("expected timeout 60, got %d", pre.Timeout)
	}
	if pre.Command[2] != "pg_dump -f /backup/db.sql" {
		t.Errorf("unexpected pre command: %v", pre.Command)
	}

	if post == nil {
		t.Fatal("missing post-update hook")
	}
	if post.Command[2] != "pg_restore /backup/db.sql" {
		t.Errorf("unexpected post command: %v", post.Command)
	}
}

func TestReadLabelsEmpty(t *testing.T) {
	hooks := ReadLabels("nginx", map[string]string{})
	if len(hooks) != 0 {
		t.Errorf("expected 0 hooks, got %d", len(hooks))
	}
}

func TestHookLabelsRoundTrip(t *testing.T) {
	original := []Hook{
		{ContainerName: "postgres", Phase: "pre-update", Command: []string{"/bin/sh", "-c", "pg_dump"}, Timeout: 60},
		{ContainerName: "postgres", Phase: "post-update", Command: []string{"/bin/sh", "-c", "pg_restore"}, Timeout: 30},
	}

	labels := HookLabels(original)
	roundTripped := ReadLabels("postgres", labels)

	if len(roundTripped) != 2 {
		t.Fatalf("expected 2 hooks after round-trip, got %d", len(roundTripped))
	}

	for _, h := range roundTripped {
		if h.Phase == "pre-update" && h.Command[2] != "pg_dump" {
			t.Errorf("pre-update command mismatch: %v", h.Command)
		}
		if h.Phase == "post-update" && h.Command[2] != "pg_restore" {
			t.Errorf("post-update command mismatch: %v", h.Command)
		}
	}
}

func TestDefaultTimeout(t *testing.T) {
	labels := map[string]string{
		"sentinel.hook.pre-update": "echo hello",
	}
	hooks := ReadLabels("test", labels)
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks))
	}
	if hooks[0].Timeout != 30 {
		t.Errorf("expected default timeout 30, got %d", hooks[0].Timeout)
	}
}
