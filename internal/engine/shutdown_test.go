package engine

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
)

// neverClock is a clock whose timers never fire, so a select on a timer
// and a cancelled context always takes the context.
type neverClock struct{ now time.Time }

func (c neverClock) Now() time.Time                       { return c.now }
func (c neverClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }
func (c neverClock) Since(t time.Time) time.Duration      { return c.now.Sub(t) }

func summary(id, name string, state container.ContainerState, labels map[string]string) container.Summary {
	return container.Summary{ID: id, Names: []string{"/" + name}, State: state, Labels: labels}
}

// assertOps checks that want appear in the mock's call log in this order,
// and that none of notWant appear.
func assertOps(t *testing.T, mock *mockDocker, want []string, notWant ...string) {
	t.Helper()
	last := -1
	for _, op := range want {
		i := opIndex(mock, op)
		if i < 0 || i < last {
			t.Fatalf("want %v in this order, got ops=%v", want, mock.ops)
		}
		last = i
	}
	for _, op := range notWant {
		if opIndex(mock, op) >= 0 {
			t.Errorf("unexpected %q: ops=%v", op, mock.ops)
		}
	}
}

// Sentinel stopped after renaming the old container aside, before the
// replacement existed: the old container gets its name back and starts.
func TestRecoverInterruptedUpdatesRestoresRenamedContainer(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{summary("old", "app-sentinel-old", container.StateExited, nil)}
	u, _ := newTestUpdater(t, mock)

	u.RecoverInterruptedUpdates(context.Background())

	assertOps(t, mock, []string{"rename old app", "start old"})
}

func TestRecoverInterruptedUpdatesRenamesRunningOldBack(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{summary("old", "app-sentinel-old", container.StateRunning, nil)}
	u, _ := newTestUpdater(t, mock)

	u.RecoverInterruptedUpdates(context.Background())

	assertOps(t, mock, []string{"rename old app"}, "start old")
}

// The replacement runs and was validated (no maintenance label): only the
// removal of the old container was left to do.
func TestRecoverInterruptedUpdatesRemovesReplacedContainer(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		summary("new", "app", container.StateRunning, nil),
		summary("old", "app-sentinel-old", container.StateExited, nil),
	}
	u, _ := newTestUpdater(t, mock)

	u.RecoverInterruptedUpdates(context.Background())

	assertOps(t, mock, []string{"remove old"}, "remove new", "rename old app")
}

// The replacement still carries the maintenance label, so it was never
// validated: the old container comes back.
func TestRecoverInterruptedUpdatesRestoresOldOverUnvalidatedReplacement(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		summary("new", "app", container.StateRunning, map[string]string{"sentinel.maintenance": "true"}),
		summary("old", "app-sentinel-old", container.StateExited, nil),
	}
	u, _ := newTestUpdater(t, mock)

	u.RecoverInterruptedUpdates(context.Background())

	assertOps(t, mock, []string{"remove new", "rename old app", "start old"}, "remove old", "rename new app-sentinel-old")
}

func TestRecoverInterruptedUpdatesRestoresOldOverStoppedReplacement(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		summary("new", "app", container.StateCreated, nil),
		summary("old", "app-sentinel-old", container.StateExited, nil),
	}
	u, _ := newTestUpdater(t, mock)

	u.RecoverInterruptedUpdates(context.Background())

	assertOps(t, mock, []string{"remove new", "rename old app", "start old"}, "remove old")
}

// A running container left with the maintenance label (Sentinel stopped
// between validation and finalise) is finalised, and a maintenance flag
// left set in the store is cleared.
func TestRecoverInterruptedUpdatesFinalisesLabelledContainer(t *testing.T) {
	mock := newMockDocker()
	labels := map[string]string{"sentinel.maintenance": "true"}
	mock.containers = []container.Summary{
		summary("web-id", "web", container.StateRunning, labels),
		summary("db-id", "db", container.StateRunning, nil),
	}
	mock.inspectResults["web-id"] = container.InspectResponse{
		ID:              "web-id",
		State:           &container.State{Running: true},
		Config:          &container.Config{Image: "web:2", Labels: labels},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}
	u, _ := newTestUpdater(t, mock)
	_ = u.store.SetMaintenance("db", true)

	u.RecoverInterruptedUpdates(context.Background())

	assertOps(t, mock, []string{"rename web-id web-sentinel-old", "create web", "start new-web", "remove web-id"}, "create db")
	if _, ok := mock.createHistory[0].Labels["sentinel.maintenance"]; ok {
		t.Error("finalised container still carries the maintenance label")
	}
	if on, _ := u.store.GetMaintenance("db"); on {
		t.Error("stale maintenance flag not cleared")
	}
}

func TestUpdateContainerRefusesSetAsideContainer(t *testing.T) {
	mock := newMockDocker()
	u, _ := newTestUpdater(t, mock)

	if err := u.UpdateContainer(context.Background(), "old", "app-sentinel-old", ""); err == nil {
		t.Fatal("expected an error for a container set aside by a swap")
	}
	if len(mock.ops) != 0 {
		t.Errorf("ops = %v, want none", mock.ops)
	}
}

// A shutdown during the grace period must not leave the old container
// stopped under its replaced name and an unvalidated replacement running:
// the old container comes back, quietly, as for a cancelled hook.
func TestUpdateContainerCancelledDuringGraceRestoresOld(t *testing.T) {
	mock, u := setupUpdateWithScanVerify(t)
	u.clock = neverClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := u.UpdateContainer(ctx, "aaa", "nginx", ""); err == nil {
		t.Fatal("expected the cancellation to be returned")
	}
	restored := []string{"start new-nginx", "remove new-nginx", "rename aaa nginx", "start aaa"}
	last := -1
	for _, op := range restored {
		i := opIndex(mock, op)
		if i < 0 || i < last {
			t.Fatalf("want %v in this order, got ops=%v", restored, mock.ops)
		}
		last = i
	}
	if on, _ := u.store.GetMaintenance("nginx"); on {
		t.Error("maintenance flag left set")
	}
	if history, _ := u.store.ListHistory(10, ""); len(history) != 0 {
		t.Errorf("history = %+v, want none for a cancelled update", history)
	}
}
