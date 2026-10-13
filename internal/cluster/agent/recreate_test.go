package agent

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// updateResult runs one UpdateContainer request through the agent's handler
// and returns the UpdateResult it sent back.
func updateResult(t *testing.T, a *Agent, name, target string) *proto.UpdateResult {
	t.Helper()
	ur, err := runUpdate(a, name, target)
	if err != nil {
		t.Fatal(err)
	}
	return ur
}

// runUpdate is updateResult for use off the test goroutine.
func runUpdate(a *Agent, name, target string) (*proto.UpdateResult, error) {
	st := &recStream{}
	req := &proto.UpdateContainerRequest{ContainerName: name, TargetImage: target}
	if err := a.handleUpdateContainer(context.Background(), st, req, "req-1"); err != nil {
		return nil, fmt.Errorf("handleUpdateContainer: %w", err)
	}
	for _, m := range st.messages() {
		if ur := m.GetUpdateResult(); ur != nil {
			return ur, nil
		}
	}
	return nil, errors.New("agent sent no UpdateResult")
}

// A digest-only update (same tag, new image behind it) has no newer tag to
// send. Hubs before v2.17.0 send an empty target for it; the agent must
// re-pull the container's own image rather than fail on an empty reference.
func TestHandleUpdateContainer_EmptyTargetRepullsCurrentImage(t *testing.T) {
	h := newFakeHost()
	h.addContainer("web", "nginx:latest", true)
	h.registry["nginx:latest"] = "sha256:nginx-latest-v2"
	a := newSwapAgent(t, h)

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
	return h, old, newSwapAgent(t, h)
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

// A running container under the parked name is not a leftover the agent
// may clear. The update must refuse with a clear error and touch nothing.
func TestRecreateContainer_RunningParkedContainerFailsSafely(t *testing.T) {
	h, old, a := recreateFixture(t)
	leftover := h.addContainer("web-sentinel-old", "nginx:0", true)

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
	h.startErrID["sha256:nginx-latest-v2"] = errors.New("exec format error")
	a := newSwapAgent(t, h)

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
	a := newSwapAgent(t, h)

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
	a := newSwapAgent(t, h)

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
	a := newSwapAgent(t, h)

	if ur := updateResult(t, a, "maintenance", "nginx:2"); ur.GetOutcome() != "success" {
		t.Fatalf("outcome = %q (%s), want success", ur.GetOutcome(), ur.GetError())
	}
	h.requireOnly(t, "maintenance", "nginx:2", false)
}

// The container was stopped when the update began but was started (by a
// user or a restart policy) before the agent parked it. The agent must
// treat it as running: stop it itself, start the replacement, and never
// force-remove a container it did not stop.
func TestRecreateContainer_StartedDuringUpdateIsStoppedNotForceRemoved(t *testing.T) {
	h := newFakeHost()
	old := h.addContainer("web", "nginx:1", false)
	h.registry["nginx:2"] = "sha256:nginx-2"
	first := true
	h.afterInspect = func(id string) {
		if first && id == old.id {
			first = false
			h.startByID(id)
		}
	}
	a := newSwapAgent(t, h)

	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err != nil {
		t.Fatalf("recreateContainer: %v", err)
	}
	if len(h.removedRunning) != 0 {
		t.Errorf("force-removed running containers %v", h.removedRunning)
	}
	h.requireOnly(t, "web", "nginx:2", true)
}

// When the hub gives up on a slow update and retries, the agent must not
// run a second recreate of the same container alongside the first: the two
// would fight over the parked name and can leave the live container parked.
func TestHandleUpdateContainer_SecondUpdateOfSameContainerFailsFast(t *testing.T) {
	h := newFakeHost()
	h.addContainer("web", "nginx:1", true)
	h.registry["nginx:2"] = "sha256:nginx-2"
	h.pullGate = make(chan struct{})
	h.pullWaiting = make(chan string, 4)
	a := newSwapAgent(t, h)

	type res struct {
		ur  *proto.UpdateResult
		err error
	}
	run := func(out chan<- res) {
		ur, err := runUpdate(a, "web", "nginx:2")
		out <- res{ur, err}
	}
	first := make(chan res, 1)
	go run(first)
	<-h.pullWaiting // the first update is now pulling

	second := make(chan res, 1)
	go run(second)
	select {
	case r := <-second:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if ur := r.ur; ur.GetOutcome() == "success" || !strings.Contains(ur.GetError(), "already running") {
			t.Errorf("second update: outcome %q, error %q; want a failure saying an update is already running", ur.GetOutcome(), ur.GetError())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second update of the same container did not answer while the first was still running")
	}

	close(h.pullGate)
	r := <-first
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.ur.GetOutcome() != "success" {
		t.Fatalf("first update: %q (%s)", r.ur.GetOutcome(), r.ur.GetError())
	}
	h.requireOnly(t, "web", "nginx:2", true)
}

// --- Networks ---

// A container on several networks must keep, on every one of them, the
// static IP, aliases and MAC address the user gave it. Docker adds the
// container's own short ID as an alias; that must not follow it.
func TestRecreateContainer_KeepsEveryNetworksSettings(t *testing.T) {
	const oldID = "0123456789abcdef0123456789abcdef"
	mock := newMockDocker()
	mock.containers = []container.Summary{{ID: oldID, Names: []string{"/web"}, Image: "nginx:1", State: container.StateRunning}}
	mock.inspectResults[oldID] = container.InspectResponse{
		ID:         oldID,
		State:      &container.State{Running: true},
		Config:     &container.Config{Image: "nginx:1"},
		HostConfig: &container.HostConfig{NetworkMode: "app_net"},
		NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
			"app_net": {
				IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: netip.MustParseAddr("10.10.0.5")},
				Aliases:    []string{"web", "0123456789ab"},
				NetworkID:  "net-app",
			},
			"monitor_net": {
				IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: netip.MustParseAddr("10.20.0.5")},
				Aliases:    []string{"web-metrics", "0123456789ab"},
				NetworkID:  "net-mon",
			},
		}},
	}
	a := newSwapAgent(t, mock)

	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err != nil {
		t.Fatalf("recreateContainer: %v", err)
	}

	if len(mock.networkConnectCalls) != 0 {
		t.Errorf("networks attached after create (settings dropped): %v", mock.networkConnectCalls)
	}
	netCfg := mock.createNetConfigs["web"]
	if netCfg == nil {
		t.Fatal("replacement created with no network settings")
	}
	want := map[string]struct {
		ip      string
		aliases []string
	}{
		"app_net":     {"10.10.0.5", []string{"web"}},
		"monitor_net": {"10.20.0.5", []string{"web-metrics"}},
	}
	for name, w := range want {
		ep := netCfg.EndpointsConfig[name]
		if ep == nil {
			t.Errorf("network %s missing at create", name)
			continue
		}
		if ep.IPAMConfig == nil || ep.IPAMConfig.IPv4Address.String() != w.ip {
			t.Errorf("network %s: static IP %+v, want %s", name, ep.IPAMConfig, w.ip)
		}
		if fmt.Sprint(ep.Aliases) != fmt.Sprint(w.aliases) {
			t.Errorf("network %s: aliases %v, want %v", name, ep.Aliases, w.aliases)
		}
	}
}

// --- Leftovers and failed cleanup ---

// A stopped container left under the parked name by an interrupted update,
// while the container itself exists, is that update's old copy: remove it
// and carry on.
func TestRecreateContainer_StoppedLeftoverRemovedAutomatically(t *testing.T) {
	h, _, a := recreateFixture(t)
	h.addContainer("web-sentinel-old", "nginx:0", false)

	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err != nil {
		t.Fatalf("recreateContainer: %v", err)
	}
	h.requireOnly(t, "web", "nginx:2", true)
}

// The update worked but the old container could not be removed: the hub
// must hear about the leftover, not just a bare success.
func TestHandleUpdateContainer_OldContainerLeftOverIsReported(t *testing.T) {
	h, old, a := recreateFixture(t)
	h.removeErr[old.id] = errors.New("device or resource busy")

	ur := updateResult(t, a, "web", "nginx:2")
	if ur.GetOutcome() != "success" {
		t.Fatalf("outcome = %q (%s), want success", ur.GetOutcome(), ur.GetError())
	}
	if !strings.Contains(ur.GetError(), "web-sentinel-old") {
		t.Errorf("result error = %q, want it to name the leftover web-sentinel-old", ur.GetError())
	}
}

func listedNames(t *testing.T, a *Agent) []string {
	t.Helper()
	got, err := a.listLocalContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range got {
		names = append(names, c.GetName())
	}
	sort.Strings(names)
	return names
}

// While a journalled swap of web is in progress (an update running, or one
// interrupted and waiting for recovery), the copy it set aside is part of
// that swap and is not reported to the hub as a container of its own.
func TestListLocalContainers_HidesSetAsideContainerOfAJournalledSwap(t *testing.T) {
	h := newFakeHost()
	h.addContainer("web", "nginx:2", true)
	old := h.addContainer("web-sentinel-old", "nginx:1", false)
	s := openTestStore(t)
	if err := s.SaveSwap(store.SwapRecord{Name: "web", Kind: "update", OldID: old.id, OldRunning: true}); err != nil {
		t.Fatal(err)
	}
	a := newSwapAgentOn(t, h, s, clock.Real{})

	if got := listedNames(t, a); fmt.Sprint(got) != "[web]" {
		t.Errorf("listed %v during the swap of web, want [web]", got)
	}
}

// A container under the set-aside name that no journal record explains
// (left by an agent from before the journal, or a container that just has
// that name) may be the only copy of the workload. Hiding it would make it
// vanish from Sentinel, and its queued update would then be pruned. It is
// reported under its own name.
func TestListLocalContainers_ReportsStrandedParkedContainer(t *testing.T) {
	h := newFakeHost()
	h.addContainer("web-sentinel-old", "nginx:1", false)
	a := newSwapAgent(t, h)

	if got := listedNames(t, a); fmt.Sprint(got) != "[web-sentinel-old]" {
		t.Errorf("listed %v, want the stranded [web-sentinel-old]", got)
	}
}

// A --rm container the agent stopped is being deleted by the daemon, even if
// it is still visible a moment later. Renaming it back and starting it would
// lose it; the rollback must recreate it instead.
func TestRecreateContainer_StoppedAutoRemoveRecreatedNotRenamedBack(t *testing.T) {
	h, old, a := recreateFixture(t)
	old.autoRemove = true
	h.lazyAutoRemove = true
	h.startErr["nginx:2"] = errors.New("port is already allocated")

	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err == nil {
		t.Fatal("recreateContainer returned no error although start failed")
	}
	if got := h.names(); len(got) != 1 || got[0] != "web" {
		t.Fatalf("containers on host = %v, want only web", got)
	}
	if c := h.byName("web"); !c.running || c.imageID != old.imageID {
		t.Errorf("web running=%v on image %s, want running on the old image %s", c.running, c.imageID, old.imageID)
	}
}

// When the broken replacement cannot be removed it still holds the real
// name, and the update's undo stops there (as the hub's does): the swap
// stays in the journal and nothing else may change web. Once the daemon
// lets go of the replacement, the agent's regular recovery removes it and
// brings the original back, running.
func TestRecreateContainer_UnremovableReplacementResolvedByRecovery(t *testing.T) {
	h, old, a := recreateFixture(t)
	h.startErr["nginx:2"] = errors.New("port is already allocated")
	h.removeErr[fakeID(2)] = errors.New("device or resource busy") // the replacement

	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); err == nil {
		t.Fatal("recreateContainer returned no error although start failed")
	}
	if _, err := a.recreateContainer(context.Background(), "web", "nginx:2"); !errors.Is(err, engine.ErrSwapPending) {
		t.Fatalf("second update error = %v, want ErrSwapPending while the first is unresolved", err)
	}

	delete(h.removeErr, fakeID(2))
	a.recoverSwaps(context.Background())
	c := h.byName("web")
	if c == nil || c.id != old.id || !c.running {
		t.Fatalf("web = %+v, want the original container back and running; containers = %v", c, h.names())
	}
	if got := h.names(); len(got) != 1 {
		t.Errorf("containers on host = %v, want only web", got)
	}
}

// If the container under the real name has never run (a replacement that
// was created but not started before the agent stopped), the stopped
// parked copy may be the working original. It is not removed.
func TestRecreateContainer_LeftoverKeptWhenRealContainerNeverRan(t *testing.T) {
	h := newFakeHost()
	h.addContainer("web", "nginx:2", false) // created, never started
	leftover := h.addContainer("web-sentinel-old", "nginx:1", false)
	leftover.started = true // the original, which ran until it was parked
	h.registry["nginx:3"] = "sha256:nginx-3"
	a := newSwapAgent(t, h)

	_, err := a.recreateContainer(context.Background(), "web", "nginx:3")
	if err == nil || !strings.Contains(err.Error(), "web-sentinel-old") {
		t.Fatalf("recreateContainer error = %v, want one naming web-sentinel-old", err)
	}
	if h.byName("web-sentinel-old") == nil {
		t.Error("the parked original was removed although web has never run")
	}
}
