package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/moby/moby/api/types/container"
)

// An update stops the old container with the container's own stop timeout
// when it is longer than Sentinel's, instead of killing it after 30s.
func TestUpdateStopsWithTheContainersStopTimeout(t *testing.T) {
	f, u, old := setupFakeUpdate(t, true, nil)
	long := 120
	old.cfg.StopTimeout = &long

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := f.stopTimeouts["old"]; got != 120 {
		t.Errorf("old container stopped with %ds, want its own 120s", got)
	}
}

// startingHealth reports "starting" for the first n inspects, then healthy.
type startingHealth struct {
	docker.API
	n     int32
	calls atomic.Int32
	hc    *container.HealthConfig
}

func (s *startingHealth) InspectContainer(ctx context.Context, id string) (container.InspectResponse, error) {
	status := container.Healthy
	if s.calls.Add(1) <= s.n {
		status = container.Starting
	}
	return container.InspectResponse{
		ID:     id,
		State:  &container.State{Running: true, Health: &container.Health{Status: status}},
		Config: &container.Config{Healthcheck: s.hc},
	}, nil
}

// advancingClock moves on by the time waited for.
type advancingClock struct{ *mockClock }

func (c advancingClock) After(d time.Duration) <-chan time.Time {
	c.Advance(d)
	return c.mockClock.After(0)
}

// Validation waits as long as the container's healthcheck can take to
// decide (start period, then retries at the interval), not a fixed 60s.
func TestValidationWaitsForTheHealthcheck(t *testing.T) {
	hc := &container.HealthConfig{Interval: 30 * time.Second, Retries: 3, StartPeriod: 2 * time.Minute}
	d := &startingHealth{API: newMockDocker(), n: 100, hc: hc} // healthy after ~200s
	u, clk := newTestUpdater(t, newMockDocker())
	u.docker = d
	u.clock = advancingClock{clk}

	healthy, err := u.validateContainer(context.Background(), "new")
	if err != nil || !healthy {
		t.Errorf("healthy = %v err = %v, want it to wait for the healthcheck", healthy, err)
	}
}

// Post-update hooks run in the container that stays: finalise replaces the
// validated container to drop the maintenance label, which killed a hook
// run before it and lost what it did inside the container.
func TestPostUpdateHooksRunInTheFinalContainer(t *testing.T) {
	f, u, old := setupFakeUpdate(t, true, nil)
	old.cfg.Labels = map[string]string{"sentinel.hook.post-update": "warm-cache"}
	u.SetHookRunner(hooks.NewRunner(f, &hookStoreStub{hooks: make(map[string][]hooks.Hook)}, slog.New(slog.NewTextHandler(io.Discard, nil))))
	u.cfg.SetHooksEnabled(true)

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	final := f.byName("app")
	if final == nil || len(f.execCalls) != 1 || f.execCalls[0] != final.id {
		t.Errorf("hook ran in %v, want only the final container %v (%v)", f.execCalls, final, f.names())
	}
}

// A rollback that itself failed is recorded as failed, not as a rollback.
func TestFailedRollbackIsRecordedAsFailed(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, nil)
	f.failStart["app-1"] = errors.New("OCI error")
	f.failStart["old"] = errors.New("port is already allocated")

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the update to fail")
	}
	history, _ := u.store.ListHistoryByContainer("app", 10)
	if len(history) != 1 || history[0].Outcome != "failed" || !strings.Contains(history[0].Error, "rollback failed") {
		t.Errorf("history = %+v, want one failed row saying the rollback failed", history)
	}
}

// A stop timeout longer than the swap's own time budget still gets its full
// time: the swap's context allows for it (it allowed 3 minutes in all, so a
// 5-minute stop_grace_period was cut off and the old container left down).
func TestSwapAllowsTheWholeStopTimeout(t *testing.T) {
	f, u, old := setupFakeUpdate(t, true, nil)
	long := 300
	old.cfg.StopTimeout = &long

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := f.stopBudget["old"]; got < 300*time.Second {
		t.Errorf("the stop had %v left on its context, want at least the 300s stop timeout", got)
	}
}

// If a stop still runs out of time, its outcome is unknown: the undo starts
// the old container if it stopped, and keeps the journal record (with the
// stop in flight) so recovery watches it, instead of forgetting it.
func TestUndoAfterAStopThatTimedOut(t *testing.T) {
	savedTimeout, savedMargin := swapTimeout, swapStopMargin
	swapTimeout, swapStopMargin = 50*time.Millisecond, 0
	defer func() { swapTimeout, swapStopMargin = savedTimeout, savedMargin }()
	f, u, _ := setupFakeUpdate(t, true, nil)
	f.stopBlocks = map[string]bool{"old": true}
	old, _ := f.InspectContainer(context.Background(), "old")
	sw := newSwap(f, u.log, u.store, swapSpec{kind: swapUpdate, name: "app", oldID: "old", old: old, snapshot: []byte(`{}`)})

	if err := sw.replace(context.Background(), recreateFrom(old), true, nil); err == nil {
		t.Fatal("expected the stop to run out of time")
	}
	if err := sw.undo(context.Background()); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if c := f.byName("app"); c == nil || c.id != "old" || !c.running {
		t.Errorf("containers = %v, want the old container back and running", f.names())
	}
	recs := journal(t, u)
	if len(recs) != 1 || !recs[0].Stopping {
		t.Errorf("journal = %+v, want the record kept with the stop in flight", recs)
	}
}
