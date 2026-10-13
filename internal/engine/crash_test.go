package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// crashScenario sets up one operation for the crash-injection test.
type crashScenario struct {
	name    string
	running bool // the container was running before the operation
	host    *container.HostConfig
	setup   func(f *fakeDocker, u *Updater)
	run     func(u *Updater) error
}

var always = container.RestartPolicy{Name: container.RestartPolicyAlways}

func crashScenarios() []crashScenario {
	update := func(u *Updater) error { return u.UpdateContainer(context.Background(), "old", "app", "") }
	return []crashScenario{
		{name: "update", running: true, host: &container.HostConfig{RestartPolicy: always}, run: update},
		{name: "update-start-fails", running: true, host: &container.HostConfig{RestartPolicy: always},
			setup: func(f *fakeDocker, _ *Updater) { f.failStart["app-1"] = errors.New("OCI error") }, run: update},
		{name: "update-stopped", running: false, host: &container.HostConfig{RestartPolicy: always}, run: update},
		{name: "update-rm", running: true, host: &container.HostConfig{AutoRemove: true}, run: update},
		{name: "rollback", running: true, host: &container.HostConfig{RestartPolicy: always},
			setup: rollbackSetup(&container.HostConfig{RestartPolicy: always}), run: rollback},
		{name: "rollback-start-fails", running: true, host: &container.HostConfig{RestartPolicy: always},
			setup: func(f *fakeDocker, u *Updater) {
				rollbackSetup(&container.HostConfig{RestartPolicy: always})(f, u)
				f.failStart["app-1"] = errors.New("OCI error")
			}, run: rollback},
		{name: "rollback-rm", running: true, host: &container.HostConfig{AutoRemove: true},
			setup: rollbackSetup(&container.HostConfig{AutoRemove: true}), run: rollback},
		{name: "rollback-rm-start-fails", running: true, host: &container.HostConfig{AutoRemove: true},
			setup: func(f *fakeDocker, u *Updater) {
				rollbackSetup(&container.HostConfig{AutoRemove: true})(f, u)
				f.failStart["app-1"] = errors.New("OCI error")
			}, run: rollback},
		// Sentinel goes down while the daemon stops the old container, and
		// the daemon finishes that stop after recovery put it back.
		{name: "update-slow-stop", running: true, host: &container.HostConfig{RestartPolicy: unlessStopped},
			setup: func(f *fakeDocker, _ *Updater) { f.slowStop["old"] = true }, run: update},
		{name: "update-rm-slow-stop", running: true, host: &container.HostConfig{AutoRemove: true},
			setup: func(f *fakeDocker, _ *Updater) { f.slowStop["old"] = true }, run: update},
		{name: "finalise-slow-stop", running: true, host: &container.HostConfig{RestartPolicy: unlessStopped},
			setup: func(f *fakeDocker, _ *Updater) { f.slowStop["app-1"] = true }, run: update},
		{name: "rollback-slow-stop", running: true, host: &container.HostConfig{RestartPolicy: unlessStopped},
			setup: func(f *fakeDocker, u *Updater) {
				rollbackSetup(&container.HostConfig{RestartPolicy: unlessStopped})(f, u)
				f.slowStop["old"] = true
			}, run: rollback},
	}
}

func rollback(u *Updater) error { return u.RollbackContainer(context.Background(), "app") }

// rollbackSetup makes the live container run the new image and stores a
// snapshot of the old one to roll back to.
func rollbackSetup(host *container.HostConfig) func(f *fakeDocker, u *Updater) {
	return func(f *fakeDocker, u *Updater) {
		f.containers["old"].imageID = "sha256:new"
		snap, _ := json.Marshal(container.InspectResponse{
			ID: "prev", Name: "/app", Image: "sha256:old",
			Config:     &container.Config{Image: "app:latest", Labels: map[string]string{}},
			HostConfig: host,
		})
		if err := u.store.SaveSnapshot("app", snap); err != nil {
			panic(err)
		}
	}
}

// newCrashFixture builds the fake and an Updater on a fresh store for sc.
func newCrashFixture(t *testing.T, sc crashScenario) (*fakeDocker, *Updater, *store.Store) {
	t.Helper()
	f := newFakeDocker()
	f.imageIDs["app:latest"] = "sha256:new"
	f.imageIDs["sha256:old"] = "sha256:old"
	f.imageIDs["sha256:new"] = "sha256:new"
	host := *sc.host
	old := f.add("old", "app", "app:latest", sc.running, &container.Config{Labels: map[string]string{}}, &host)
	old.imageID = "sha256:old"
	s := testStore(t)
	u := newFakeUpdaterOn(t, f, s)
	if sc.setup != nil {
		sc.setup(f, u)
	}
	return f, u, s
}

// recoverAfterCrash is a restarted Sentinel: its startup recovery runs,
// the daemon then finishes any stop it was still carrying out, and the
// scans' retries see the watch of restored containers through to its end.
// The background watch is held still, so it never acts.
func recoverAfterCrash(t *testing.T, f *fakeDocker, s *store.Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := newStepClock(true)
	u := newFakeUpdaterWith(t, f, s, clk)
	u.RecoverInterruptedUpdates(ctx)
	f.finishSlowStops()
	for range 3 {
		if recs, err := s.ListSwaps(); err != nil || len(recs) == 0 {
			return
		}
		clk.Advance(time.Hour)
		u.resolvePendingSwaps(ctx)
	}
}

// runOp runs the scenario's operation until it ends or crashes. A stop the
// daemon was carrying out slowly happens once only.
func runOp(f *fakeDocker, sc crashScenario, u *Updater) {
	_ = runUntilCrash(func() error { return sc.run(u) })
	clear(f.slowStop)
}

// runUntilCrash runs op and swallows the simulated crash.
func runUntilCrash(op func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if r != errCrash { //nolint:errorlint // the panic value is the sentinel itself
				panic(r)
			}
			err = errCrash
		}
	}()
	return op()
}

// checkRecovered asserts the end state after recovery: exactly one copy of
// the service under its own name, running if it ran before, with its
// original restart policy, no maintenance label and an empty journal.
func checkRecovered(t *testing.T, f *fakeDocker, s *store.Store, sc crashScenario, where string) {
	t.Helper()
	var copies []*fakeContainer
	for _, c := range f.containers {
		if strings.TrimSuffix(c.name, replacedSuffix) == "app" {
			copies = append(copies, c)
		}
	}
	if len(copies) != 1 {
		t.Fatalf("%s: %d copies of app, want 1: %v", where, len(copies), f.names())
	}
	c := copies[0]
	if c.name != "app" {
		t.Errorf("%s: the copy is named %q, want app: %v", where, c.name, f.names())
	}
	// An update starts the new container even when the old one was
	// stopped (it has to, to validate it), so a stopped container ends up
	// either stopped on its old image or running on the new one.
	wantRunning := sc.running || c.imageID != "sha256:old"
	if c.running != wantRunning {
		t.Errorf("%s: running = %v, want %v: %v", where, c.running, wantRunning, f.names())
	}
	if c.host.RestartPolicy != sc.host.RestartPolicy {
		t.Errorf("%s: restart policy = %+v, want %+v", where, c.host.RestartPolicy, sc.host.RestartPolicy)
	}
	if c.running && c.cfg.Labels["sentinel.maintenance"] == "true" {
		t.Errorf("%s: the running copy still carries the maintenance label", where)
	}
	if recs, err := s.ListSwaps(); err != nil || len(recs) != 0 {
		t.Errorf("%s: journal = %+v (err %v), want empty", where, recs, err)
	}
}

// The most important property of the swap: wherever Sentinel stops (a
// crash), or the daemon goes away, a restarted Sentinel recovers to exactly
// one copy of the service with its original restart policy. Every call
// position of every operation is tried.
func TestCrashAtEveryStepRecovers(t *testing.T) {
	cases := 0
	defer func() { t.Logf("%d crash cases", cases) }()
	for _, sc := range crashScenarios() {
		// Count the calls of an uninterrupted run.
		f, u, _ := newCrashFixture(t, sc)
		runOp(f, sc, u)
		total := f.callCount()
		if total == 0 {
			t.Fatalf("%s: no container calls counted", sc.name)
		}

		modes := []struct {
			name         string
			panics, late bool
		}{{"crash-before-call", true, false}, {"crash-after-call", false, true}, {"daemon-gone", false, false}}
		for _, mode := range modes {
			for n := 1; n <= total; n++ {
				where := fmt.Sprintf("%s/%s %d of %d", sc.name, mode.name, n, total)
				f, u, s := newCrashFixture(t, sc)
				f.setCrash(n, mode.panics, mode.late)
				runOp(f, sc, u)
				f.setCrash(0, false, false)

				recoverAfterCrash(t, f, s)
				checkRecovered(t, f, s, sc, where)
				cases++
				if t.Failed() {
					return
				}
			}
		}
	}
}

// Recovery itself can be interrupted too. For every crash point of an
// update, crash the recovery at every point as well, then let a third
// start recover cleanly.
func TestCrashDuringRecoveryRecovers(t *testing.T) {
	cases := 0
	defer func() { t.Logf("%d crash cases", cases) }()
	for _, sc := range crashScenarios() {
		f, u, _ := newCrashFixture(t, sc)
		runOp(f, sc, u)
		total := f.callCount()
		for n := 1; n <= total; n++ {
			// Find how many calls the recovery of this crash makes.
			f, u, s := newCrashFixture(t, sc)
			f.setCrash(n, false, true)
			runOp(f, sc, u)
			f.setCrash(0, false, false)
			probe := cloneCrashState(t, f, s)
			recoverAfterCrash(t, probe.f, probe.s)
			recoveryCalls := probe.f.callCount()

			for m := 1; m <= recoveryCalls; m++ {
				where := fmt.Sprintf("%s: crash after call %d, recovery crash after call %d", sc.name, n, m)
				st := cloneCrashState(t, f, s)
				st.f.setCrash(m, false, true)
				_ = runUntilCrash(func() error {
					recoverAfterCrash(t, st.f, st.s)
					return nil
				})
				st.f.setCrash(0, false, false)
				recoverAfterCrash(t, st.f, st.s)
				checkRecovered(t, st.f, st.s, sc, where)
				cases++
				if t.Failed() {
					return
				}
			}
		}
	}
}

type crashState struct {
	f *fakeDocker
	s *store.Store
}

// cloneCrashState copies the containers, image tags and journal of a
// crashed run, so several recoveries can be tried from the same point.
func cloneCrashState(t *testing.T, f *fakeDocker, s *store.Store) crashState {
	t.Helper()
	c := newFakeDocker()
	for k, v := range f.imageIDs {
		c.imageIDs[k] = v
	}
	for id, fc := range f.containers {
		cp := *fc
		cp.cfg = cloneConfig(fc.cfg)
		h := *fc.host
		cp.host = &h
		c.containers[id] = &cp
	}
	c.seq = f.seq
	c.pendingStops = slices.Clone(f.pendingStops)
	cs := testStore(t)
	recs, err := s.ListSwaps()
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		if err := cs.SaveSwap(rec); err != nil {
			t.Fatal(err)
		}
	}
	return crashState{f: c, s: cs}
}
