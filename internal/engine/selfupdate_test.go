package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/moby/moby/api/types/container"
)

func newTestSelfUpdater(mock *mockDocker) *SelfUpdater {
	return NewSelfUpdater(mock, logging.New(false))
}

func sentinelContainer(id, name, image string) container.Summary {
	return container.Summary{
		ID:     id,
		Names:  []string{"/" + name},
		Image:  image,
		Labels: map[string]string{"sentinel.self": "true"},
	}
}

func sentinelInspect(image string) container.InspectResponse {
	return container.InspectResponse{
		Config: &container.Config{
			Image:  image,
			Env:    []string{"SENTINEL_WEB_PORT=8080"},
			Labels: map[string]string{"sentinel.self": "true"},
		},
		HostConfig: &container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		},
	}
}

func TestSelfUpdateUsesOriginalImage(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", "ghcr.io/will-luck/docker-sentinel:2.2.0")}
	mock.inspectResults["abc123"] = sentinelInspect("ghcr.io/will-luck/docker-sentinel:2.2.0")

	su := newTestSelfUpdater(mock)
	if err := su.Update(context.Background(), ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mock.createCalls))
	}

	// Helper name should start with sentinel-updater-
	if !strings.HasPrefix(mock.createCalls[0], "sentinel-updater-") {
		t.Errorf("expected helper name prefix 'sentinel-updater-', got %q", mock.createCalls[0])
	}

	// Script in Cmd should reference the original image, not a different one.
	cfg := mock.createConfigs[mock.createCalls[0]]
	if cfg == nil {
		t.Fatal("no config captured for helper container")
	}
	script := strings.Join(cfg.Cmd, " ")
	if !strings.Contains(script, "ghcr.io/will-luck/docker-sentinel:2.2.0") {
		t.Errorf("script should contain original image ref, got: %s", script)
	}
}

func TestSelfUpdateUsesTargetImage(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", "ghcr.io/will-luck/docker-sentinel:2.2.0")}
	mock.inspectResults["abc123"] = sentinelInspect("ghcr.io/will-luck/docker-sentinel:2.2.0")

	su := newTestSelfUpdater(mock)
	if err := su.Update(context.Background(), "ghcr.io/will-luck/docker-sentinel:2.3.1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg := mock.createConfigs[mock.createCalls[0]]
	if cfg == nil {
		t.Fatal("no config captured for helper container")
	}
	script := strings.Join(cfg.Cmd, " ")

	// Script should reference the target image, not the original.
	if !strings.Contains(script, "ghcr.io/will-luck/docker-sentinel:2.3.1") {
		t.Errorf("script should contain target image ref, got: %s", script)
	}
	if strings.Contains(script, "docker-sentinel:2.2.0") {
		t.Errorf("script should NOT contain original image ref, got: %s", script)
	}
}

func TestSelfUpdateNoSentinelContainer(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "xyz", Names: []string{"/some-other"}, Labels: map[string]string{}},
	}

	su := newTestSelfUpdater(mock)
	err := su.Update(context.Background(), "")
	if err == nil {
		t.Fatal("expected error when no sentinel container found")
	}
	if !strings.Contains(err.Error(), "could not find sentinel container") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSelfUpdateInspectFails(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", "img:1.0")}
	mock.inspectErr["abc123"] = ErrUpdateInProgress

	su := newTestSelfUpdater(mock)
	err := su.Update(context.Background(), "")
	if err == nil {
		t.Fatal("expected error when inspect fails")
	}
	if !strings.Contains(err.Error(), "inspect self") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSelfUpdateHelperLifecycle(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", "img:1.0")}
	mock.inspectResults["abc123"] = sentinelInspect("img:1.0")

	su := newTestSelfUpdater(mock)
	err := su.Update(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.startCalls) != 1 {
		t.Fatalf("expected 1 start call, got %d", len(mock.startCalls))
	}
}
