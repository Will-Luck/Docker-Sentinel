package agent

import (
	"context"
	"errors"
	"strings"
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

// --- Never leave the container missing ---

// recreateFixture is a running "web" on nginx:1 with nginx:2 in the registry.
func recreateFixture(t *testing.T) (*fakeHost, *fakeContainer, *Agent) {
	t.Helper()
	h := newFakeHost()
	old := h.addContainer("web", "nginx:1", true)
	h.registry["nginx:2"] = "sha256:nginx-2"
	return h, old, newTestAgent(t.TempDir(), h)
}

func TestRecreateContainer_SuccessReplacesContainer(t *testing.T) {
	h, _, a := recreateFixture(t)

	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err != nil {
		t.Fatalf("recreateContainer: %v", err)
	}
	h.requireOnly(t, "web", "nginx:2", true)
	if env := h.byName("web").env; len(env) != 1 || env[0] != "APP_MODE=prod" {
		t.Errorf("new container env = %v, want the old container's [APP_MODE=prod]", env)
	}
}

// The daemon rejects the new container (bad config, missing mount): the old
// container must still be there, running, under its own name.
func TestRecreateContainer_CreateFailureKeepsOldContainer(t *testing.T) {
	h, _, a := recreateFixture(t)
	h.createErr["nginx:2"] = errors.New("invalid mount config")

	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err == nil {
		t.Fatal("recreateContainer returned no error although create failed")
	}
	h.requireOnly(t, "web", "nginx:1", true)
}

// The new container is created but will not start (port clash, bad
// entrypoint): it must be removed and the old container brought back.
func TestRecreateContainer_StartFailureRestoresOldContainer(t *testing.T) {
	h, old, a := recreateFixture(t)
	h.startErr["nginx:2"] = errors.New("port is already allocated")

	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err == nil {
		t.Fatal("recreateContainer returned no error although start failed")
	}
	h.requireOnly(t, "web", "nginx:1", true)
	if c := h.byName("web"); c.id != old.id {
		t.Errorf("web is container %s, want the original %s back", c.id, old.id)
	}
}

func TestRecreateContainer_StopFailureLeavesOldContainerRunning(t *testing.T) {
	h, old, a := recreateFixture(t)
	h.stopErr[old.id] = errors.New("container did not stop in time")

	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err == nil {
		t.Fatal("recreateContainer returned no error although stop failed")
	}
	h.requireOnly(t, "web", "nginx:1", true)
}

// A --rm container is deleted by the daemon as soon as it stops, so there
// is no stopped container to rename back. The agent must recreate it from
// what it inspected, on the old image.
func TestRecreateContainer_AutoRemoveContainerRestoredOnStartFailure(t *testing.T) {
	h, old, a := recreateFixture(t)
	old.autoRemove = true
	h.startErr["nginx:2"] = errors.New("port is already allocated")

	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err == nil {
		t.Fatal("recreateContainer returned no error although start failed")
	}
	if got := h.names(); len(got) != 1 || got[0] != "web" {
		t.Fatalf("containers on host = %v, want only web", got)
	}
	c := h.byName("web")
	if !c.running || c.imageID != old.imageID {
		t.Errorf("web running=%v on image %s, want running on the old image %s", c.running, c.imageID, old.imageID)
	}
}

// A --rm container must not be stopped before its replacement exists:
// when create fails it is still running, untouched.
func TestRecreateContainer_AutoRemoveContainerKeptOnCreateFailure(t *testing.T) {
	h, old, a := recreateFixture(t)
	old.autoRemove = true
	h.createErr["nginx:2"] = errors.New("invalid mount config")

	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err == nil {
		t.Fatal("recreateContainer returned no error although create failed")
	}
	h.requireOnly(t, "web", "nginx:1", true)
	if c := h.byName("web"); c.id != old.id {
		t.Errorf("web is container %s, want the original %s", c.id, old.id)
	}
}

// A container left under the parked name by an interrupted update blocks
// the protocol. The update must refuse with a clear error and touch nothing.
func TestRecreateContainer_LeftoverParkedContainerFailsSafely(t *testing.T) {
	h, old, a := recreateFixture(t)
	leftover := h.addContainer("web-sentinel-old", "nginx:0", false)

	_, err := a.recreateContainer(context.Background(), "web", "nginx:2")
	if err == nil || !strings.Contains(err.Error(), "web-sentinel-old") {
		t.Fatalf("recreateContainer error = %v, want one naming web-sentinel-old", err)
	}
	if got := h.names(); len(got) != 2 {
		t.Fatalf("containers on host = %v, want web and the leftover untouched", got)
	}
	if c := h.byName("web"); c.id != old.id || !c.running || c.image != "nginx:1" {
		t.Errorf("web changed: id=%s running=%v image=%s", c.id, c.running, c.image)
	}
	if c := h.byName("web-sentinel-old"); c == nil || c.id != leftover.id {
		t.Error("the leftover parked container was changed")
	}
}

// On a same-tag update the tag already names the new image once it has been
// pulled, so bringing back a --rm container by tag would run the image that
// just failed. It must come back on the old image ID.
func TestRecreateContainer_AutoRemoveSameTagRestoredOnOldImage(t *testing.T) {
	h := newFakeHost()
	old := h.addContainer("web", "nginx:latest", true)
	old.autoRemove = true
	h.registry["nginx:latest"] = "sha256:nginx-latest-v2"
	h.startErr["nginx:latest"] = errors.New("exec format error")
	a := newTestAgent(t.TempDir(), h)

	if _, err := a.recreateContainer(context.Background(), "web", ""); err == nil {
		t.Fatal("recreateContainer returned no error although start failed")
	}
	if got := h.names(); len(got) != 1 || got[0] != "web" {
		t.Fatalf("containers on host = %v, want only web", got)
	}
	if c := h.byName("web"); c.imageID != old.imageID || !c.running {
		t.Errorf("web running=%v on image %s, want running on the old image %s", c.running, c.imageID, old.imageID)
	}
}

// --- Keep the container's running state ---

// A container the user stopped (or a one-shot job that has exited) must
// come back stopped: updating its image is not a reason to start it.
func TestRecreateContainer_StoppedContainerStaysStopped(t *testing.T) {
	h := newFakeHost()
	h.addContainer("backup-job", "restic:1", false)
	h.registry["restic:2"] = "sha256:restic-2"
	a := newTestAgent(t.TempDir(), h)

	if _, err := a.recreateContainer(context.Background(), "backup-job", "restic:2"); err != nil {
		t.Fatalf("recreateContainer: %v", err)
	}
	h.requireOnly(t, "backup-job", "restic:2", false)
	if len(h.startCalls) != 0 {
		t.Errorf("start called %d times for a container that was stopped", len(h.startCalls))
	}
}

func TestRecreateContainer_StoppedContainerStaysStoppedOnRollback(t *testing.T) {
	h := newFakeHost()
	h.addContainer("backup-job", "restic:1", false)
	h.registry["restic:2"] = "sha256:restic-2"
	h.createErr["restic:2"] = errors.New("invalid mount config")
	a := newTestAgent(t.TempDir(), h)

	if _, err := a.recreateContainer(context.Background(), "backup-job", "restic:2"); err == nil {
		t.Fatal("recreateContainer returned no error although create failed")
	}
	h.requireOnly(t, "backup-job", "restic:1", false)
}

// The same through the update request the hub sends.
func TestHandleUpdateContainer_StoppedContainerStaysStopped(t *testing.T) {
	h := newFakeHost()
	h.addContainer("maintenance", "nginx:1", false)
	h.registry["nginx:2"] = "sha256:nginx-2"
	a := newTestAgent(t.TempDir(), h)

	if ur := updateResult(t, a, "maintenance", "nginx:2"); ur.GetOutcome() != "success" {
		t.Fatalf("outcome = %q (%s), want success", ur.GetOutcome(), ur.GetError())
	}
	h.requireOnly(t, "maintenance", "nginx:2", false)
}
