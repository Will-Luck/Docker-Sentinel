package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

var unlessStopped = container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}

// slowStopUpdate runs an update of an unless-stopped container and kills
// Sentinel while the daemon is stopping the old container, as a SIGKILL of
// Sentinel during `docker stop -t 30` does. The daemon's stop is left
// pending in the fake.
func slowStopUpdate(t *testing.T) (*fakeDocker, *store.Store) {
	t.Helper()
	f := newFakeDocker()
	f.imageIDs["app:latest"] = "sha256:new"
	old := f.add("old", "app", "app:latest", true, &container.Config{Labels: map[string]string{}}, &container.HostConfig{RestartPolicy: unlessStopped})
	old.imageID = "sha256:old"
	f.slowStop["old"] = true
	s := testStore(t)
	u := newFakeUpdaterOn(t, f, s)
	if err := runUntilCrash(func() error { return u.UpdateContainer(context.Background(), "old", "app", "") }); !errors.Is(err, errCrash) {
		t.Fatalf("update: %v, want Sentinel to go down while the old container stops", err)
	}
	return f, s
}

func assertRunningAgain(t *testing.T, f *fakeDocker) {
	t.Helper()
	c := f.byName("app")
	if c == nil || c.id != "old" || !c.running {
		t.Fatalf("containers = %v, want the old container running again as app", f.names())
	}
	if c.host.RestartPolicy != unlessStopped {
		t.Errorf("restart policy = %+v, want unless-stopped", c.host.RestartPolicy)
	}
}

// Recovery puts the old container back while the daemon is still carrying
// out the stop Sentinel asked for before it was killed. That stop then ends
// and kills the container, and an unless-stopped policy does not start a
// container stopped that way. The journal keeps the swap until the stop
// timeout has passed, so the next scan, or a Sentinel restarted again in
// that window, starts the container again and records it in the history.
func TestRecoveryStartsAgainAContainerTheDaemonStoppedLate(t *testing.T) {
	for _, next := range []string{"scan", "restart"} {
		t.Run(next, func(t *testing.T) {
			f, s := slowStopUpdate(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			clk := newStepClock(true)
			u := newFakeUpdaterWith(t, f, s, clk)

			u.RecoverInterruptedUpdates(ctx)
			assertRunningAgain(t, f)
			f.finishSlowStops() // the daemon's stop ends after the restore

			clk.Advance(20 * time.Second) // within the 30s stop timeout
			if next == "scan" {
				u.resolvePendingSwaps(ctx)
			} else {
				newFakeUpdaterWith(t, f, s, clk).RecoverInterruptedUpdates(ctx)
			}
			assertRunningAgain(t, f)
			if len(journal(t, u)) != 1 {
				t.Errorf("journal = %+v, want the swap kept until the stop timeout has passed", journal(t, u))
			}

			clk.Advance(30 * time.Second)
			u.resolvePendingSwaps(ctx)
			assertRunningAgain(t, f)
			if len(journal(t, u)) != 0 {
				t.Errorf("journal = %+v, want it empty once the stop timeout has passed", journal(t, u))
			}
			assertOneRestartRow(t, s)
		})
	}
}

func assertOneRestartRow(t *testing.T, s *store.Store) {
	t.Helper()
	history, err := s.ListHistoryByContainer("app", 10)
	if err != nil || len(history) != 1 || history[0].Outcome != "rollback" || history[0].OldImage != "app:latest" {
		t.Errorf("history = %+v (err %v), want one rollback row saying it was started again", history, err)
	}
}

// The swap journals the stop and its timeout before the stop is asked for.
func TestSwapJournalsTheStopBeforeAskingForIt(t *testing.T) {
	f, s := slowStopUpdate(t)
	recs, err := s.ListSwaps()
	if err != nil || len(recs) != 1 || !recs[0].Stopping || recs[0].StopTimeout != 30 {
		t.Fatalf("journal = %+v (err %v), want the stop and its 30s timeout recorded", recs, err)
	}
	if !slices.Contains(f.ops(), "stop old") {
		t.Fatalf("ops = %v, want the stop asked for", f.ops())
	}
}

// A stop that returned is over: the record no longer says it is in flight,
// so a later recovery does not watch for it.
func TestSwapClearsTheStopOnceItReturns(t *testing.T) {
	f, u, _ := setupFakeUpdate(t, true, &container.HostConfig{RestartPolicy: always})
	f.failStart["app-1"] = errors.New("OCI error")
	f.failPolicyTo = map[container.RestartPolicyMode]error{container.RestartPolicyAlways: errors.New("daemon busy")}
	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err == nil {
		t.Fatal("expected the update to fail")
	}
	recs := journal(t, u)
	if len(recs) != 1 || recs[0].Stopping {
		t.Fatalf("journal = %+v, want the record kept (the policy restore failed) without the stop in flight", recs)
	}
}

// Without a restart in between, the background watch started by recovery
// finds the container the late stop ended at its next check, starts it
// again, and lets the record go once the stop timeout has passed. Shutdown
// ends it.
func TestWatchStartsAgainAContainerTheDaemonStoppedLate(t *testing.T) {
	f, s := slowStopUpdate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := newStepClock(false)
	u := newFakeUpdaterWith(t, f, s, clk)

	u.RecoverInterruptedUpdates(ctx)
	clk.waitForTimer(t) // the watch waits for its first check
	assertRunningAgain(t, f)
	f.finishSlowStops()

	clk.Advance(watchInterval)
	clk.waitForTimer(t) // that check is done, the next one waits
	assertRunningAgain(t, f)
	if len(journal(t, u)) != 1 {
		t.Errorf("journal = %+v, want the swap still watched", journal(t, u))
	}

	clk.Advance(time.Minute)
	u.watchers.Wait()
	assertRunningAgain(t, f)
	if len(journal(t, u)) != 0 {
		t.Errorf("journal = %+v, want it empty once the watch is over", journal(t, u))
	}
	assertOneRestartRow(t, s)
}

// While the restored container is watched, an update says so instead of
// pointing at the manual steps for a swap that could not be resolved.
func TestUpdateDuringWatchSaysWhy(t *testing.T) {
	f, s := slowStopUpdate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := newFakeUpdaterWith(t, f, s, newStepClock(true))
	u.RecoverInterruptedUpdates(ctx)

	err := u.UpdateContainer(ctx, "old", "app", "")
	if !errors.Is(err, ErrSwapPending) || !strings.Contains(err.Error(), "watches it until 00:00:40") {
		t.Errorf("err = %v, want it to say the container is watched until 00:00:40", err)
	}
}

// Shutdown ends the watch; the record stays for the next start.
func TestWatchEndsOnShutdown(t *testing.T) {
	f, s := slowStopUpdate(t)
	ctx, cancel := context.WithCancel(context.Background())
	clk := newStepClock(false)
	u := newFakeUpdaterWith(t, f, s, clk)

	u.RecoverInterruptedUpdates(ctx)
	clk.waitForTimer(t)
	cancel()
	u.watchers.Wait()

	if len(journal(t, u)) != 1 {
		t.Errorf("journal = %+v, want the watched record kept for the next start", journal(t, u))
	}
}
