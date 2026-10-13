package engine

import (
	"context"
	"slices"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// setupDependentsUpdate builds an update of "nginx" with running containers
// that depend on it in different ways.
func setupDependentsUpdate(t *testing.T) (*mockDocker, *Updater) {
	t.Helper()
	mock, u := setupUpdateWithScanVerify(t)
	mock.containers = []container.Summary{
		summary("new-nginx", "nginx", container.StateRunning, nil),
		summary("app-id", "app", container.StateRunning, map[string]string{"sentinel.depends-on": "nginx"}),
		summary("web-id", "proj-web-1", container.StateRunning, map[string]string{
			"com.docker.compose.project":    "proj",
			"com.docker.compose.service":    "web",
			"com.docker.compose.depends_on": "proxy:service_started:true",
		}),
		summary("other-id", "other", container.StateRunning, nil),
	}
	// The updated container is the compose service "proxy" of project "proj".
	mock.containers[0].Labels = map[string]string{
		"com.docker.compose.project": "proj",
		"com.docker.compose.service": "proxy",
	}
	return mock, u
}

// Containers that declare a dependency on the updated one are restarted, so
// they reconnect to it.
func TestUpdateContainerRestartsDependents(t *testing.T) {
	mock, u := setupDependentsUpdate(t)

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	for _, id := range []string{"app-id", "web-id"} {
		if !slices.Contains(mock.restartCalls, id) {
			t.Errorf("dependent %s not restarted after its dependency updated: restartCalls=%v", id, mock.restartCalls)
		}
	}
	if slices.Contains(mock.restartCalls, "other-id") {
		t.Errorf("unrelated container restarted: restartCalls=%v", mock.restartCalls)
	}
}

func TestUpdateContainerDependentsNotRestartedWhenDisabled(t *testing.T) {
	mock, u := setupDependentsUpdate(t)
	u.cfg.SetDependencyAware(false)

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	if len(mock.restartCalls) != 0 {
		t.Errorf("restartCalls = %v with SENTINEL_DEPS off, want none", mock.restartCalls)
	}
}

// A network-namespace consumer is repaired once, by the namespace step, not
// restarted again by the dependency step.
func TestUpdateContainerRestartsNamespaceConsumerOnce(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	mock.containers = []container.Summary{
		summary("new-nginx", "nginx", container.StateRunning, nil),
		summary("vpn-user", "vpn-user", container.StateRunning, nil),
	}
	mock.containers[1].HostConfig.NetworkMode = "container:nginx"
	mock.inspectResults["vpn-user"] = container.InspectResponse{
		ID:         "vpn-user",
		HostConfig: &container.HostConfig{NetworkMode: "container:nginx"},
	}

	if err := u.UpdateContainer(context.Background(), "aaa", "nginx", ""); err != nil {
		t.Fatalf("UpdateContainer: %v", err)
	}
	if !slices.Equal(mock.restartCalls, []string{"vpn-user"}) {
		t.Errorf("restartCalls = %v, want vpn-user restarted exactly once", mock.restartCalls)
	}
}
