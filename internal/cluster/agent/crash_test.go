package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// The hub's crash-injection test (internal/engine/crash_test.go), ported to
// the agent's update path: wherever the agent is killed during an update,
// or the daemon goes away, the agent started again recovers to exactly one
// copy of the container, under its own name, in the running state it had,
// with its original restart policy, and an empty swap journal.

// agentCrashScenario sets up one update for the crash-injection test.
type agentCrashScenario struct {
	name    string
	running bool // the container was running before the update
	host    container.HostConfig
	setup   func(h *fakeHost, old *fakeContainer)
}

var (
	crashAlways        = container.RestartPolicy{Name: container.RestartPolicyAlways}
	crashUnlessStopped = container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}
)

func agentCrashScenarios() []agentCrashScenario {
	startFails := func(h *fakeHost, _ *fakeContainer) { h.startErr["app:2"] = errors.New("OCI error") }
	// The agent goes down while the daemon stops the old container, and the
	// daemon finishes that stop after recovery has put it back.
	slowStop := func(h *fakeHost, old *fakeContainer) { h.slowStop[old.id] = true }
	return []agentCrashScenario{
		{name: "update", running: true, host: container.HostConfig{RestartPolicy: crashAlways}},
		{name: "update-start-fails", running: true, host: container.HostConfig{RestartPolicy: crashAlways}, setup: startFails},
		{name: "update-stopped", running: false, host: container.HostConfig{RestartPolicy: crashAlways}},
		{name: "update-rm", running: true, host: container.HostConfig{AutoRemove: true}},
		{name: "update-rm-start-fails", running: true, host: container.HostConfig{AutoRemove: true}, setup: startFails},
		{name: "update-slow-stop", running: true, host: container.HostConfig{RestartPolicy: crashUnlessStopped}, setup: slowStop},
		{name: "update-rm-slow-stop", running: true, host: container.HostConfig{AutoRemove: true}, setup: slowStop},
		// The new container exits right after it starts: the validation
		// after the grace period rolls the update back.
		{name: "update-exits-on-start", running: true, host: container.HostConfig{RestartPolicy: crashAlways},
			setup: func(h *fakeHost, _ *fakeContainer) { h.exitsOnStart["sha256:app-2"] = true }},
		{name: "update-rm-exits-on-start", running: true, host: container.HostConfig{AutoRemove: true},
			setup: func(h *fakeHost, _ *fakeContainer) { h.exitsOnStart["sha256:app-2"] = true }},
	}
}

// newAgentCrashFixture is a host with "app" on app:1 and app:2 in the
// registry, and an agent on a fresh database.
func newAgentCrashFixture(t *testing.T, sc agentCrashScenario) (*fakeHost, *Agent, *store.Store) {
	t.Helper()
	h := newFakeHost()
	h.addImage("app:1", "sha256:app-1", &container.Config{})
	h.registry["app:2"] = "sha256:app-2"
	h.imageConfigs["sha256:app-2"] = &container.Config{}
	host := sc.host
	old := h.addFromImage(t, "app", "app:1", nil, &host, nil, sc.running)
	if sc.setup != nil {
		sc.setup(h, old)
	}
	h.setCrash(0, false, false)
	s := openTestStore(t)
	// The update waits a grace period before it validates the new
	// container; the clock lets it pass at once, so a crash point falls
	// just after the wait, before the validation.
	a := newSwapAgentOn(t, h, s, instantClock{})
	a.settings.SetGracePeriod(30 * time.Second)
	return h, a, s
}

// runAgentUpdate runs the update of app to app:2 until it ends or the agent
// crashes. A stop the daemon was carrying out slowly happens once only.
func runAgentUpdate(h *fakeHost, a *Agent) {
	_ = runUntilCrash(func() error {
		_, err := runUpdate(a, "app", "app:2")
		return err
	})
	clear(h.slowStop)
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

// recoverAgentAfterCrash is the agent started again: its startup recovery
// runs, the daemon then finishes any stop it was still carrying out, and
// the agent's regular retries see the watch of restored containers through
// to its end. The background watch is held still, so it never acts.
func recoverAgentAfterCrash(t *testing.T, h *fakeHost, s *store.Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk := newStepClock(true)
	a := newSwapAgentOn(t, h, s, clk)
	a.swaps.SetShutdownContext(ctx)
	a.recoverSwaps(ctx)
	h.finishSlowStops()
	for range 3 {
		if recs, err := s.ListSwaps(); err != nil || len(recs) == 0 {
			return
		}
		clk.Advance(time.Hour)
		a.recoverSwaps(ctx)
	}
}

// checkAgentRecovered asserts the end state after recovery: exactly one copy
// of app (under any name the update could have given it), named app, in the
// running state it had, with its original restart policy, and an empty
// journal.
func checkAgentRecovered(t *testing.T, h *fakeHost, s *store.Store, sc agentCrashScenario, where string) {
	t.Helper()
	h.mu.Lock()
	var copies []fakeContainer
	for _, c := range h.containers {
		if c.name == "app" || strings.HasPrefix(c.name, "app-sentinel-") {
			copies = append(copies, *c)
		}
	}
	h.mu.Unlock()
	if len(copies) != 1 {
		t.Fatalf("%s: %d copies of app, want 1: %v", where, len(copies), h.names())
	}
	c := copies[0]
	if c.name != "app" {
		t.Errorf("%s: the copy is named %q, want app: %v", where, c.name, h.names())
	}
	// The agent keeps a container's running state, updated or not.
	if c.running != sc.running {
		t.Errorf("%s: running = %v, want %v (image %s)", where, c.running, sc.running, c.image)
	}
	if got := c.restartPolicy(); got != sc.host.RestartPolicy {
		t.Errorf("%s: restart policy = %+v, want %+v", where, got, sc.host.RestartPolicy)
	}
	if recs, err := s.ListSwaps(); err != nil || len(recs) != 0 {
		t.Errorf("%s: journal = %+v (err %v), want empty", where, recs, err)
	}
}

// Wherever the agent stops during an update (a crash), or the daemon goes
// away, the agent started again recovers to exactly one copy of the
// container with its original restart policy. Every call position of every
// update is tried.
func TestAgentCrashAtEveryStepRecovers(t *testing.T) {
	cases := 0
	defer func() { t.Logf("%d crash cases", cases) }()
	for _, sc := range agentCrashScenarios() {
		// Count the calls of an uninterrupted run.
		h, a, _ := newAgentCrashFixture(t, sc)
		runAgentUpdate(h, a)
		total := h.callCount()
		if total == 0 {
			t.Fatalf("%s: no Docker calls counted", sc.name)
		}

		modes := []struct {
			name         string
			panics, late bool
		}{{"crash-before-call", true, false}, {"crash-after-call", false, true}, {"daemon-gone", false, false}}
		for _, mode := range modes {
			for n := 1; n <= total; n++ {
				where := fmt.Sprintf("%s/%s %d of %d", sc.name, mode.name, n, total)
				h, a, s := newAgentCrashFixture(t, sc)
				h.setCrash(n, mode.panics, mode.late)
				runAgentUpdate(h, a)
				h.setCrash(0, false, false)

				recoverAgentAfterCrash(t, h, s)
				checkAgentRecovered(t, h, s, sc, where)
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
func TestAgentCrashDuringRecoveryRecovers(t *testing.T) {
	cases := 0
	defer func() { t.Logf("%d crash cases", cases) }()
	for _, sc := range agentCrashScenarios() {
		h, a, _ := newAgentCrashFixture(t, sc)
		runAgentUpdate(h, a)
		total := h.callCount()
		for n := 1; n <= total; n++ {
			// Find how many calls the recovery of this crash makes.
			h, a, s := newAgentCrashFixture(t, sc)
			h.setCrash(n, false, true)
			runAgentUpdate(h, a)
			h.setCrash(0, false, false)
			probe := cloneAgentCrashState(t, h, s)
			recoverAgentAfterCrash(t, probe.h, probe.s)
			recoveryCalls := probe.h.callCount()

			for m := 1; m <= recoveryCalls; m++ {
				where := fmt.Sprintf("%s: crash after call %d, recovery crash after call %d", sc.name, n, m)
				st := cloneAgentCrashState(t, h, s)
				st.h.setCrash(m, false, true)
				_ = runUntilCrash(func() error {
					recoverAgentAfterCrash(t, st.h, st.s)
					return nil
				})
				st.h.setCrash(0, false, false)
				recoverAgentAfterCrash(t, st.h, st.s)
				checkAgentRecovered(t, st.h, st.s, sc, where)
				cases++
				if t.Failed() {
					return
				}
			}
		}
	}
}

type agentCrashState struct {
	h *fakeHost
	s *store.Store
}

// cloneAgentCrashState copies the containers, images and journal of a
// crashed run, so several recoveries can be tried from the same point.
func cloneAgentCrashState(t *testing.T, h *fakeHost, s *store.Store) agentCrashState {
	t.Helper()
	h.mu.Lock()
	c := newFakeHost()
	maps.Copy(c.images, h.images)
	maps.Copy(c.imageIDs, h.imageIDs)
	maps.Copy(c.registry, h.registry)
	maps.Copy(c.imageConfigs, h.imageConfigs)
	maps.Copy(c.createErr, h.createErr)
	maps.Copy(c.startErr, h.startErr)
	maps.Copy(c.stopErr, h.stopErr)
	maps.Copy(c.renameErr, h.renameErr)
	maps.Copy(c.removeErr, h.removeErr)
	maps.Copy(c.exitsOnStart, h.exitsOnStart)
	c.nextID, c.volumeSeq = h.nextID, h.volumeSeq
	c.pendingStops = slices.Clone(h.pendingStops)
	for id, fc := range h.containers {
		cp := *fc
		cp.env = slices.Clone(fc.env)
		cp.labels = maps.Clone(fc.labels)
		cp.mounts = slices.Clone(fc.mounts)
		if fc.cfg != nil {
			cfg := *fc.cfg
			cfg.Labels = maps.Clone(fc.cfg.Labels)
			cp.cfg = &cfg
		}
		if fc.hostCfg != nil {
			hc := *fc.hostCfg
			cp.hostCfg = &hc
		}
		c.containers[id] = &cp
	}
	h.mu.Unlock()

	cs := openTestStore(t)
	recs, err := s.ListSwaps()
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		if err := cs.SaveSwap(rec); err != nil {
			t.Fatal(err)
		}
	}
	return agentCrashState{h: c, s: cs}
}

// An agent killed in the middle of an update puts the container back when
// it starts again, before it does anything else (here it stops at once:
// it is not enrolled).
func TestRun_RecoversAnInterruptedUpdateAtStartup(t *testing.T) {
	h := newFakeHost()
	h.addImage("app:1", "sha256:app-1", &container.Config{})
	old := h.addFromImage(t, "app", "app:1", nil, &container.HostConfig{RestartPolicy: crashAlways}, nil, true)
	h.registry["app:2"] = "sha256:app-2"
	h.imageConfigs["sha256:app-2"] = &container.Config{}
	s := openTestStore(t)

	// The first run is killed just after creating the replacement.
	first := newSwapAgentOn(t, h, s, newStepClock(true))
	stopAfterCreate(t, h, first)

	a := New(Config{DataDir: t.TempDir(), Store: s, Settings: first.settings}, h, noopLogger())
	if err := a.Run(context.Background()); err == nil {
		t.Fatal("Run of an agent that is not enrolled returned no error")
	}
	c := h.byName("app")
	if c == nil || c.id != old.id || !c.running {
		t.Fatalf("app = %+v, want the original container back and running; containers = %v", c, h.names())
	}
	if recs, err := s.ListSwaps(); err != nil || len(recs) != 0 {
		t.Errorf("journal = %+v (err %v), want empty", recs, err)
	}
}

// stopAfterCreate runs an update of app to app:2 and kills the agent just
// after the replacement is created.
func stopAfterCreate(t *testing.T, h *fakeHost, a *Agent) {
	t.Helper()
	h.createHook = func() { panic(errCrash) }
	defer func() { h.createHook = nil }()
	if err := runUntilCrash(func() error {
		_, err := runUpdate(a, "app", "app:2")
		return err
	}); !errors.Is(err, errCrash) {
		t.Fatalf("the update did not crash: %v", err)
	}
	if h.byName("app-sentinel-old") == nil {
		t.Fatalf("the crash left %v, want app set aside", h.names())
	}
}

// A stopping agent waits (within a bound) for the commands it is running,
// such as an update half way through, before it returns and its database
// is closed under them.
func TestRun_WaitsForRunningCommands(t *testing.T) {
	a := newSwapAgent(t, newFakeHost())
	a.inflight.Add(1)
	done := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		a.inflight.Add(-1)
		close(done)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_ = a.Run(ctx)
	select {
	case <-done:
	default:
		t.Errorf("Run returned after %v with a command still running", time.Since(start))
	}
}

// review (minor): the startup recovery ran before the settings cached from
// the hub were loaded, so it followed the defaults: with dependency
// awareness off on the hub, a recovered container's dependents were
// restarted all the same. The cached settings load first.
func TestRun_RecoveryFollowsTheCachedHubSettings(t *testing.T) {
	h := newFakeHost()
	h.addImage("db:1", "sha256:db-1", &container.Config{})
	old := h.addFromImage(t, "db-sentinel-old", "db:1", nil, nil, nil, false) // set aside and stopped
	repl := h.addFromImage(t, "db", "db:1", &container.Config{Labels: map[string]string{"sentinel.swap": "tok"}}, nil, nil, false)
	web := h.addFromImage(t, "web", "db:1", &container.Config{Labels: map[string]string{"sentinel.depends-on": "db"}}, nil, nil, true)
	s := openTestStore(t)
	if err := s.SaveSwap(store.SwapRecord{Name: "db", Kind: "update", OldID: old.id, NewID: repl.id, Token: "tok", OldRunning: true}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cached := newTestAgent(dir, h)
	cached.handleSettingsSync(&proto.SettingsSync{DependencyAware: false, ImageCleanup: true})

	a := New(Config{DataDir: dir, Store: s, Settings: config.NewTestConfig()}, h, noopLogger())
	_ = a.Run(context.Background()) // not enrolled: it stops after the recovery

	if c := h.byName("db"); c == nil || c.id != old.id || !c.running {
		t.Fatalf("db = %+v, want the old container back and running", c)
	}
	if slices.Contains(h.restartCalls, web.id) {
		t.Error("web was restarted although the hub has dependency awareness off")
	}
}
