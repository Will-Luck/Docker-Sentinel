package agent

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// updateResult runs one UpdateContainer request through the agent's handler
// and returns the UpdateResult it sent back.
func updateResult(t *testing.T, a *Agent, name, target string) *proto.UpdateResult {
	t.Helper()
	st := &recStream{}
	req := &proto.UpdateContainerRequest{ContainerName: name, TargetImage: target}
	if err := a.handleUpdateContainer(context.Background(), st, req, "req-1"); err != nil {
		t.Fatalf("handleUpdateContainer: %v", err)
	}
	for _, m := range st.messages() {
		if ur := m.GetUpdateResult(); ur != nil {
			return ur
		}
	}
	t.Fatal("agent sent no UpdateResult")
	return nil
}

// A digest-only update (same tag, new image behind it) has no newer tag to
// send. Hubs before v2.17.0 send an empty target for it; the agent must
// re-pull the container's own image rather than fail on an empty reference.
func TestHandleUpdateContainer_EmptyTargetRepullsCurrentImage(t *testing.T) {
	h := newFakeHost()
	h.addContainer("web", "nginx:latest", true)
	h.registry["nginx:latest"] = "sha256:nginx-latest-v2"
	a := newTestAgent(t.TempDir(), h)

	ur := updateResult(t, a, "web", "")

	if ur.GetOutcome() != "success" {
		t.Fatalf("outcome = %q (%s), want success", ur.GetOutcome(), ur.GetError())
	}
	if ur.GetNewImage() != "nginx:latest" {
		t.Errorf("result NewImage = %q, want nginx:latest", ur.GetNewImage())
	}
	if len(h.pullCalls) != 1 || h.pullCalls[0] != "nginx:latest" {
		t.Errorf("pulls = %q, want [nginx:latest]", h.pullCalls)
	}
	h.requireOnly(t, "web", "nginx:latest", true)
	if c := h.byName("web"); c.imageID != "sha256:nginx-latest-v2" {
		t.Errorf("web runs image %s, want the newly pulled sha256:nginx-latest-v2", c.imageID)
	}
}

// The agent's own container takes the self-update path, which needs the
// same fallback.
func TestSelfUpdateContainer_EmptyTargetRepullsCurrentImage(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelSummary("s-1", "sentinel-agent", "sentinel:v1")}
	mock.inspectResults["s-1"] = sentinelInspect("sentinel:v1")
	a := newTestAgent(t.TempDir(), mock)

	if _, err := a.selfUpdateContainer(context.Background(), "sentinel-agent", ""); err != nil {
		t.Fatalf("selfUpdateContainer: %v", err)
	}
	if len(mock.pullCalls) != 1 || mock.pullCalls[0] != "sentinel:v1" {
		t.Errorf("pulls = %q, want [sentinel:v1]", mock.pullCalls)
	}
	if cfg := mock.createConfigs["sentinel-agent"]; cfg == nil || cfg.Image != "sentinel:v1" {
		t.Errorf("replacement created from %+v, want image sentinel:v1", cfg)
	}
}
