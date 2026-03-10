package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
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
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"sentinel_default": {},
			},
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

	// Should create a container named "sentinel" (the original name).
	if len(mock.createCalls) != 1 || mock.createCalls[0] != "sentinel" {
		t.Fatalf("expected create call for 'sentinel', got %v", mock.createCalls)
	}

	// New container config should use the original image.
	cfg := mock.createConfigs["sentinel"]
	if cfg == nil {
		t.Fatal("no config captured for replacement container")
	}
	if cfg.Image != "ghcr.io/will-luck/docker-sentinel:2.2.0" {
		t.Errorf("expected image ghcr.io/will-luck/docker-sentinel:2.2.0, got %s", cfg.Image)
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

	cfg := mock.createConfigs["sentinel"]
	if cfg == nil {
		t.Fatal("no config captured for replacement container")
	}
	if cfg.Image != "ghcr.io/will-luck/docker-sentinel:2.3.1" {
		t.Errorf("expected target image, got %s", cfg.Image)
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

func TestSelfUpdateRenameAndReplace(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", "img:1.0")}
	mock.inspectResults["abc123"] = sentinelInspect("img:1.0")

	su := newTestSelfUpdater(mock)
	if err := su.Update(context.Background(), "img:2.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have pulled the target image.
	if len(mock.pullCalls) != 1 || mock.pullCalls[0] != "img:2.0" {
		t.Errorf("expected pull of img:2.0, got %v", mock.pullCalls)
	}

	// Should have renamed the old container.
	if len(mock.renameCalls) != 1 {
		t.Fatalf("expected 1 rename call, got %d", len(mock.renameCalls))
	}
	if mock.renameCalls[0].id != "abc123" {
		t.Errorf("expected rename of abc123, got %s", mock.renameCalls[0].id)
	}
	if !strings.HasPrefix(mock.renameCalls[0].newName, "sentinel-old-") {
		t.Errorf("expected rename to sentinel-old-*, got %s", mock.renameCalls[0].newName)
	}

	// Should have created a container with the original name.
	if len(mock.createCalls) != 1 || mock.createCalls[0] != "sentinel" {
		t.Errorf("expected create of 'sentinel', got %v", mock.createCalls)
	}

	// Should have started the new container.
	if len(mock.startCalls) != 1 {
		t.Fatalf("expected 1 start call, got %d", len(mock.startCalls))
	}

	// No helper container should exist (no docker:cli pull).
	for _, ref := range mock.pullCalls {
		if ref == "docker:cli" {
			t.Error("should not pull docker:cli helper image")
		}
	}
}

func TestSelfUpdateRollbackOnCreateFailure(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", "img:1.0")}
	mock.inspectResults["abc123"] = sentinelInspect("img:1.0")
	mock.createErr["sentinel"] = fmt.Errorf("name conflict")

	su := newTestSelfUpdater(mock)
	err := su.Update(context.Background(), "img:2.0")
	if err == nil {
		t.Fatal("expected error when create fails")
	}

	// Should have attempted rename, then rolled back.
	if len(mock.renameCalls) != 2 {
		t.Fatalf("expected 2 rename calls (rename + rollback), got %d", len(mock.renameCalls))
	}
	// Second rename should restore original name.
	if mock.renameCalls[1].newName != "sentinel" {
		t.Errorf("rollback should rename back to 'sentinel', got %s", mock.renameCalls[1].newName)
	}
}

func TestSelfUpdateMultiNetwork(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", "img:1.0")}
	inspect := sentinelInspect("img:1.0")
	inspect.NetworkSettings = &container.NetworkSettings{
		Networks: map[string]*network.EndpointSettings{
			"app_net":     {},
			"monitor_net": {},
		},
	}
	mock.inspectResults["abc123"] = inspect

	su := newTestSelfUpdater(mock)
	if err := su.Update(context.Background(), ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// One network in create's NetworkingConfig, one via NetworkConnect.
	if len(mock.networkConnectCalls) != 1 {
		t.Fatalf("expected 1 NetworkConnect call for second network, got %d", len(mock.networkConnectCalls))
	}
}
