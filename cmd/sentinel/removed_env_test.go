package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestWarnRemovedEnv(t *testing.T) {
	t.Setenv("SENTINEL_PORTAINER_URL", "")
	t.Setenv("SENTINEL_PORTAINER_TOKEN", "")
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	warnRemovedEnv(log)
	if buf.Len() != 0 {
		t.Fatalf("unset env logged %q", buf.String())
	}

	t.Setenv("SENTINEL_PORTAINER_URL", "https://portainer.example")
	t.Setenv("SENTINEL_PORTAINER_TOKEN", "super-secret-token")
	warnRemovedEnv(log)
	out := buf.String()
	if strings.Count(out, "level=WARN") != 1 {
		t.Errorf("want exactly one warning, got %q", out)
	}
	if !strings.Contains(out, "Portainer support was removed in v2.17.0") {
		t.Errorf("warning text missing: %q", out)
	}
	if strings.Contains(out, "super-secret-token") || strings.Contains(out, "portainer.example") {
		t.Errorf("env values leaked: %q", out)
	}
}
