package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
)

// eventDocker records the order of lifecycle calls made through it.
type eventDocker struct {
	*mockDocker
	mu     sync.Mutex
	events []string
}

func (e *eventDocker) note(ev string) {
	e.mu.Lock()
	e.events = append(e.events, ev)
	e.mu.Unlock()
}

func (e *eventDocker) StartContainer(ctx context.Context, id string) error {
	e.note("start:" + id)
	return e.mockDocker.StartContainer(ctx, id)
}

func (e *eventDocker) StopContainer(ctx context.Context, id string, timeout int) error {
	e.note("stop:" + id)
	return e.mockDocker.StopContainer(ctx, id, timeout)
}

func (e *eventDocker) RemoveContainer(ctx context.Context, id string) error {
	e.note("remove:" + id)
	return e.mockDocker.RemoveContainer(ctx, id)
}

func newSelfUpdateFixture(image string) (*eventDocker, *SelfUpdater) {
	mock := newMockDocker()
	mock.containers = []container.Summary{sentinelContainer("abc123", "sentinel", image)}
	mock.inspectResults["abc123"] = sentinelInspect(image)
	ev := &eventDocker{mockDocker: mock}
	return ev, NewSelfUpdater(ev, newTestSelfUpdater(mock).log)
}

// After the replacement has started, the old instance must be stopped: it
// still holds the BoltDB file lock, and the new instance exits at startup
// without it.
func TestSelfUpdateStopsOldContainerAfterReplacementStarts(t *testing.T) {
	ev, su := newSelfUpdateFixture("img:1.0")
	if err := su.Update(context.Background(), "img:2.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"start:new-sentinel", "stop:abc123"}
	if !slices.Equal(ev.events, want) {
		t.Errorf("lifecycle calls = %v, want %v", ev.events, want)
	}
}

// If the replacement cannot start, the old instance keeps running and gets its
// name back: it is never stopped before the new one is known to be up.
func TestSelfUpdateStartFailureLeavesOldContainerRunning(t *testing.T) {
	ev, su := newSelfUpdateFixture("img:1.0")
	ev.startErr = map[string]error{"new-sentinel": fmt.Errorf("Bind for 0.0.0.0:8080 failed: port is already allocated")}

	err := su.Update(context.Background(), "img:2.0")
	if err == nil {
		t.Fatal("expected an error when the replacement cannot start")
	}
	if !strings.Contains(err.Error(), "port is already allocated") || !strings.Contains(err.Error(), "published") {
		t.Errorf("error %q should carry the daemon message and say published ports are the likely cause", err)
	}
	if slices.Contains(ev.events, "stop:abc123") {
		t.Errorf("old container was stopped although the replacement never started: %v", ev.events)
	}
	if !slices.Contains(ev.events, "remove:new-sentinel") {
		t.Errorf("failed replacement was not removed: %v", ev.events)
	}
	last := ev.renameCalls[len(ev.renameCalls)-1]
	if last.id != "abc123" || last.newName != "sentinel" {
		t.Errorf("old container not renamed back, last rename = %+v", last)
	}
}

// A registry outage must not block an update to an image already on the host.
func TestSelfUpdateToleratesPullFailureWhenImageIsLocal(t *testing.T) {
	_, su := newSelfUpdateFixture("img:1.0")
	mock := su.docker.(*eventDocker).mockDocker
	mock.pullErr["img:2.0"] = fmt.Errorf("registry unreachable")
	mock.imageDigests["img:2.0"] = "img@sha256:cafe"

	if err := su.Update(context.Background(), "img:2.0"); err != nil {
		t.Fatalf("update failed although img:2.0 is available locally: %v", err)
	}
}

func TestSelfUpdatePullFailureWithoutLocalImageStopsBeforeAnyChange(t *testing.T) {
	ev, su := newSelfUpdateFixture("img:1.0")
	ev.pullErr["img:2.0"] = fmt.Errorf("registry unreachable")
	ev.imageDigestErr["img:2.0"] = fmt.Errorf("no such image")

	if err := su.Update(context.Background(), "img:2.0"); err == nil {
		t.Fatal("expected an error")
	}
	if len(ev.renameCalls) != 0 || len(ev.createCalls) != 0 {
		t.Errorf("pull failure changed state: renames=%v creates=%v", ev.renameCalls, ev.createCalls)
	}
}

func oldInstance(id, name, state string) container.Summary {
	c := sentinelContainer(id, name, "img:1.0")
	c.State = container.ContainerState(state)
	return c
}

// Leftover "<name>-old-<unix>" instances from earlier self-updates are removed
// at startup, but only stopped ones that have a running replacement.
func TestCleanupOldInstancesRemovesOnlyStoppedLeftovers(t *testing.T) {
	mock := newMockDocker()
	other := oldInstance("keep-other", "grafana-old-111", "exited")
	other.Labels = map[string]string{}
	mock.containers = []container.Summary{
		oldInstance("cur", "sentinel", "running"),
		oldInstance("gone1", "sentinel-old-1700000000", "exited"),
		oldInstance("gone2", "sentinel-old-1700000999", "created"),
		oldInstance("keep-running", "sentinel-old-1700001111", "running"),
		oldInstance("keep-paused", "sentinel-old-1700002222", "paused"),
		oldInstance("keep-restarting", "sentinel-old-1700003333", "restarting"),
		oldInstance("keep-dead", "sentinel-old-1700004444", "dead"),
		other,
		oldInstance("keep-orphan", "lost-old-5", "exited"),
		oldInstance("keep-name", "sentinel-old-backup", "exited"),
	}
	mock.inspectResults["cur"] = container.InspectResponse{State: &container.State{Running: true}}
	su := NewSelfUpdater(mock, newTestSelfUpdater(mock).log)
	su.selfID = func() string { return "cur" }

	su.CleanupOldInstances(context.Background())

	got := slices.Clone(mock.removeCalls)
	slices.Sort(got)
	if want := []string{"gone1", "gone2"}; !slices.Equal(got, want) {
		t.Errorf("removed %v, want %v", got, want)
	}
}

// The scheduler cleans up leftovers when it starts, before any scan.
func TestSchedulerStartupRemovesOldInstances(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		oldInstance("cur", "sentinel", "running"),
		oldInstance("gone", "sentinel-old-1700000000", "exited"),
	}
	mock.inspectResults["cur"] = container.InspectResponse{State: &container.State{Running: true}}
	u, clk := newTestUpdater(t, mock)
	sched := NewScheduler(u, u.cfg, u.log, clk)
	su := NewSelfUpdater(mock, u.log)
	su.selfID = func() string { return "cur" }
	sched.SetSelfUpdater(su)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = sched.Run(ctx)

	if !slices.Contains(mock.removeCalls, "gone") {
		t.Errorf("removeCalls = %v, want the leftover container removed at startup", mock.removeCalls)
	}
}

// The replacement must be told it is a self-update replacement so it waits for
// the previous instance to release the database lock.
func TestSelfUpdateMarksReplacementForLockWait(t *testing.T) {
	ev, su := newSelfUpdateFixture("img:1.0")
	if err := su.Update(context.Background(), "img:2.0"); err != nil {
		t.Fatal(err)
	}
	cfg := ev.createConfigs["sentinel"]
	if !slices.Contains(cfg.Env, "SENTINEL_SELF_UPDATE_REPLACEMENT=1") {
		t.Errorf("replacement env = %v, want SENTINEL_SELF_UPDATE_REPLACEMENT=1", cfg.Env)
	}
	if !slices.Contains(cfg.Env, "SENTINEL_WEB_PORT=8080") {
		t.Errorf("original env lost: %v", cfg.Env)
	}
}

// If the old instance cannot be stopped, the replacement is removed and the
// old one gets its name back instead of leaving two half-working instances.
func TestSelfUpdateStopFailureRollsBack(t *testing.T) {
	ev, su := newSelfUpdateFixture("img:1.0")
	ev.stopErr = map[string]error{"abc123": fmt.Errorf("daemon busy")}

	if err := su.Update(context.Background(), "img:2.0"); err == nil {
		t.Fatal("expected an error when the old instance cannot be stopped")
	}
	if !slices.Contains(ev.events, "remove:new-sentinel") {
		t.Errorf("replacement not removed: %v", ev.events)
	}
	last := ev.renameCalls[len(ev.renameCalls)-1]
	if last.id != "abc123" || last.newName != "sentinel" {
		t.Errorf("old container not renamed back, last rename = %+v", last)
	}
}

func withSocket(inspect container.InspectResponse) container.InspectResponse {
	inspect.ID = "abc123"
	inspect.Mounts = []container.MountPoint{{Type: "bind", Source: "/run/docker.sock", Destination: "/var/run/docker.sock"}}
	return inspect
}

// With the Docker socket mounted the swap is handed to a helper container
// instead of being done in-process: the running instance never starts the
// replacement or stops itself, because it cannot free its own published ports.
func TestSelfUpdateUsesHelperWhenSocketIsMounted(t *testing.T) {
	ev, su := newSelfUpdateFixture("img:1.0")
	ev.inspectResults["abc123"] = withSocket(sentinelInspect("img:1.0"))
	su.poll = time.Millisecond

	err := su.Update(context.Background(), "img:2.0")
	if err == nil {
		t.Fatal("Update returned nil; with a helper it only returns when the helper did not replace us")
	}
	cfg := ev.createConfigs["sentinel-self-update-helper"]
	if cfg == nil || cfg.Image != "img:1.0" || !slices.Equal(cfg.Cmd, []string{"self-update-helper"}) {
		t.Fatalf("helper config = %+v", cfg)
	}
	if slices.Contains(ev.events, "start:new-sentinel") || slices.Contains(ev.events, "stop:abc123") {
		t.Errorf("the running instance did the swap itself: %v", ev.events)
	}
}

// Over a TCP Docker host there is no socket to give a helper: replace in-process.
func TestSelfUpdateFallsBackInProcessForTCPDockerHost(t *testing.T) {
	ev, su := newSelfUpdateFixture("img:1.0")
	ev.inspectResults["abc123"] = withSocket(sentinelInspect("img:1.0"))
	su.SetDockerSock("tcp://10.0.0.5:2376")

	if err := su.Update(context.Background(), "img:2.0"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ev.events, "start:new-sentinel") || !slices.Contains(ev.events, "stop:abc123") {
		t.Errorf("in-process swap did not run: %v", ev.events)
	}
	if ev.createConfigs["sentinel-self-update-helper"] != nil {
		t.Error("a helper was created for a TCP Docker host")
	}
}

func selfUpdaterWithGate(t *testing.T, mode verify.Mode, v ImageVerifier) (*eventDocker, *SelfUpdater) {
	t.Helper()
	ev, su := newSelfUpdateFixture("img:1.0")
	u, _ := newTestUpdater(t, ev.mockDocker)
	u.SetVerifyMode(mode)
	if v != nil {
		u.SetVerifier(v)
	}
	su.SetUpdater(u)
	return ev, su
}

// The new Sentinel image goes through the same signature gate as any other
// update: in enforce mode it is refused, before anything changes, when cosign
// is missing or the signature does not verify.
func TestSelfUpdateEnforceRefusesWithoutVerifier(t *testing.T) {
	ev, su := selfUpdaterWithGate(t, verify.ModeEnforce, nil)
	err := su.Update(context.Background(), "img:2.0")
	if !errors.Is(err, ErrVerifierUnavailable) {
		t.Fatalf("err = %v, want ErrVerifierUnavailable", err)
	}
	if len(ev.pullCalls) != 0 || len(ev.renameCalls) != 0 {
		t.Errorf("something changed: pulls=%v renames=%v", ev.pullCalls, ev.renameCalls)
	}
}

func TestSelfUpdateEnforceRefusesUnverifiedImage(t *testing.T) {
	mv := &mockImageVerifier{result: &verify.Result{Verified: false, Error: "no matching signatures"}}
	ev, su := selfUpdaterWithGate(t, verify.ModeEnforce, mv)
	err := su.Update(context.Background(), "img:2.0")
	if err == nil || !strings.Contains(err.Error(), "no matching signatures") {
		t.Fatalf("err = %v", err)
	}
	if len(ev.pullCalls) != 0 {
		t.Errorf("image pulled despite the refusal: %v", ev.pullCalls)
	}
}

func TestSelfUpdateWarnModeProceedsOnFailedVerification(t *testing.T) {
	mv := &mockImageVerifier{result: &verify.Result{Verified: false, Error: "no matching signatures"}}
	ev, su := selfUpdaterWithGate(t, verify.ModeWarn, mv)
	if err := su.Update(context.Background(), "img:2.0"); err != nil {
		t.Fatalf("warn mode should proceed: %v", err)
	}
	if len(ev.createCalls) != 1 {
		t.Errorf("update did not run: %v", ev.createCalls)
	}
}

// A host restart between "stop old" and "start new" leaves this instance
// parked under its -old- name with the replacement never started. At startup
// it removes the replacement and takes its own name back.
func TestStartupPutsAParkedInstanceBackAfterAnInterruptedSwap(t *testing.T) {
	mock := newMockDocker()
	self := map[string]string{"sentinel.self": "true"}
	mock.containers = []container.Summary{
		{ID: "abcdef1234567890", Names: []string{"/sentinel-old-1700000000"}, State: "running", Labels: self},
		{ID: "new1", Names: []string{"/sentinel"}, State: "created", Labels: self},
	}
	su := NewSelfUpdater(mock, newTestSelfUpdater(mock).log)
	su.selfID = func() string { return "abcdef123456" }

	su.CleanupOldInstances(context.Background())

	if !slices.Contains(mock.removeCalls, "new1") {
		t.Errorf("replacement not removed: %v", mock.removeCalls)
	}
	if len(mock.renameCalls) != 1 || mock.renameCalls[0].id != "abcdef1234567890" || mock.renameCalls[0].newName != "sentinel" {
		t.Errorf("renames = %+v, want this instance renamed back to sentinel", mock.renameCalls)
	}
}

// After an in-process upgrade the old container only exits once the new one has
// started, so the cleanup runs again a while after start.
func TestCleanupAfterRemovesALeftoverThatAppearedAfterStartup(t *testing.T) {
	mock := newMockDocker()
	self := map[string]string{"sentinel.self": "true"}
	mock.containers = []container.Summary{{ID: "cur", Names: []string{"/sentinel"}, State: "running", Labels: self}}
	mock.inspectResults["cur"] = container.InspectResponse{State: &container.State{Running: true}}
	live := &growingList{mockDocker: mock}
	live.list = mock.containers
	su := NewSelfUpdater(live, newTestSelfUpdater(mock).log)
	su.selfID = func() string { return "cur" }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	su.CleanupOldInstances(ctx)
	su.CleanupAfter(ctx, 20*time.Millisecond)
	live.add(container.Summary{ID: "old", Names: []string{"/sentinel-old-1700000000"}, State: "exited", Labels: self})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mock.mu.Lock()
		got := slices.Contains(mock.removeCalls, "old")
		mock.mu.Unlock()
		if got {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the leftover that appeared after startup was never removed")
}

// growingList is a Docker double whose container list can change while a
// background goroutine reads it.
type growingList struct {
	*mockDocker
	lmu  sync.Mutex
	list []container.Summary
}

func (g *growingList) add(c container.Summary) {
	g.lmu.Lock()
	g.list = append(slices.Clone(g.list), c)
	g.lmu.Unlock()
}

func (g *growingList) ListAllContainers(context.Context) ([]container.Summary, error) {
	g.lmu.Lock()
	defer g.lmu.Unlock()
	return slices.Clone(g.list), nil
}
