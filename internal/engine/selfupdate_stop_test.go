package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

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
		other,
		oldInstance("keep-orphan", "lost-old-5", "exited"),
		oldInstance("keep-name", "sentinel-old-backup", "exited"),
	}
	su := NewSelfUpdater(mock, newTestSelfUpdater(mock).log)

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
	u, clk := newTestUpdater(t, mock)
	sched := NewScheduler(u, u.cfg, u.log, clk)
	sched.SetSelfUpdater(NewSelfUpdater(mock, u.log))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = sched.Run(ctx)

	if !slices.Contains(mock.removeCalls, "gone") {
		t.Errorf("removeCalls = %v, want the leftover container removed at startup", mock.removeCalls)
	}
}
