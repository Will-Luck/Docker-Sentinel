package engine

import (
	"context"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/moby/moby/api/types/container"
)

// testSettings is a simple in-memory SettingsReader for tests.
type testSettings struct {
	data map[string]string
}

func (ts *testSettings) LoadSetting(key string) (string, error) {
	return ts.data[key], nil
}

func TestSchedulerWaitsForReadyGate(t *testing.T) {
	mock := newMockDocker()
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	cfg.SetGracePeriod(1 * time.Second)
	cfg.SetPollInterval(1 * time.Hour)
	notifier := notify.NewMulti(log)
	u := NewUpdater(mock, checker, s, q, cfg, log, clk, notifier, nil)
	sched := NewScheduler(u, cfg, log, clk)

	gate := make(chan struct{})
	sched.SetReadyGate(gate)

	scanned := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = sched.Run(ctx)
		close(scanned)
	}()

	// Scheduler should be blocked on the gate.
	select {
	case <-scanned:
		t.Fatal("scheduler ran before gate was opened")
	case <-time.After(50 * time.Millisecond):
	}

	// Open the gate — scheduler should proceed and complete initial scan.
	close(gate)
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-scanned
}

func TestSchedulerRunsInitialScan(t *testing.T) {
	mock := newMockDocker()
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	cfg.SetGracePeriod(1 * time.Second)
	cfg.SetPollInterval(1 * time.Hour)
	notifier := notify.NewMulti(log)
	u := NewUpdater(mock, checker, s, q, cfg, log, clk, notifier, nil)
	sched := NewScheduler(u, cfg, log, clk)

	// Cancel immediately after the initial scan.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Give scheduler time to complete initial scan, then cancel.
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := sched.Run(ctx)
	if err != nil {
		t.Errorf("Run() returned error: %v", err)
	}
}

func TestMaybeSelfUpdate_ManualModeSkips(t *testing.T) {
	mock := newMockDocker()
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	cfg.SetGracePeriod(1 * time.Second)
	notifier := notify.NewMulti(log)
	u := NewUpdater(mock, checker, s, q, cfg, log, clk, notifier, nil)

	settings := &testSettings{data: map[string]string{"self_update_mode": "manual"}}
	sched := NewScheduler(u, cfg, log, clk)
	sched.SetSettingsReader(settings)
	sched.SetSelfUpdater(NewSelfUpdater(mock, log))

	// Simulate: scan detected a self-update and queued it.
	q.Add(PendingUpdate{ContainerName: "sentinel", CurrentImage: "ghcr.io/test:1.0"})
	u.selfUpdateQueued.Store(true)
	u.selfUpdateKey.Store("sentinel")

	// Call maybeSelfUpdate — should NOT remove from queue (mode=manual).
	sched.maybeSelfUpdate()

	if q.Len() != 1 {
		t.Errorf("queue.Len() = %d, want 1 (manual mode should not auto-update)", q.Len())
	}
}

func TestMaybeSelfUpdate_AutoModeWhenBusy(t *testing.T) {
	mock := newMockDocker()
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	cfg.SetGracePeriod(1 * time.Second)
	notifier := notify.NewMulti(log)
	u := NewUpdater(mock, checker, s, q, cfg, log, clk, notifier, nil)

	settings := &testSettings{data: map[string]string{"self_update_mode": "auto"}}
	sched := NewScheduler(u, cfg, log, clk)
	sched.SetSettingsReader(settings)
	sched.SetSelfUpdater(NewSelfUpdater(mock, log))

	// Simulate: self-update queued + another container updating.
	q.Add(PendingUpdate{ContainerName: "sentinel", CurrentImage: "ghcr.io/test:1.0"})
	u.selfUpdateQueued.Store(true)
	u.selfUpdateKey.Store("sentinel")
	u.tryLock("some-other-container")

	// Call maybeSelfUpdate — should NOT trigger (not idle).
	sched.maybeSelfUpdate()

	if q.Len() != 1 {
		t.Errorf("queue.Len() = %d, want 1 (should defer when busy)", q.Len())
	}

	u.unlock("some-other-container")
}

func TestMaybeSelfUpdate_AutoModeWhenIdle(t *testing.T) {
	mock := newMockDocker()
	// Provide a sentinel container so SelfUpdater.Update can find it.
	mock.containers = []container.Summary{
		{ID: "self123", Names: []string{"/sentinel"}, Image: "ghcr.io/test:1.0",
			Labels: map[string]string{"sentinel.self": "true"}},
	}
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	cfg.SetGracePeriod(1 * time.Second)
	notifier := notify.NewMulti(log)
	u := NewUpdater(mock, checker, s, q, cfg, log, clk, notifier, nil)

	settings := &testSettings{data: map[string]string{"self_update_mode": "auto"}}
	sched := NewScheduler(u, cfg, log, clk)
	sched.SetSettingsReader(settings)
	sched.SetSelfUpdater(NewSelfUpdater(mock, log))

	// Simulate: self-update queued, system idle.
	q.Add(PendingUpdate{
		ContainerName: "sentinel",
		CurrentImage:  "ghcr.io/test:1.0",
		NewerVersions: []string{"2.0"},
	})
	u.selfUpdateQueued.Store(true)
	u.selfUpdateKey.Store("sentinel")

	// Call maybeSelfUpdate — should remove from queue (auto + idle).
	sched.maybeSelfUpdate()

	if q.Len() != 0 {
		t.Errorf("queue.Len() = %d, want 0 (auto mode + idle should trigger)", q.Len())
	}
	// The actual Update() goroutine will fail (mock Docker doesn't fully support it),
	// but we verified the queue was drained which proves the logic triggered.
}

func TestMatchesFilter(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		patterns []string
		want     bool
	}{
		{"exact match", "nginx", []string{"nginx"}, true},
		{"second pattern matches", "nginx", []string{"redis", "nginx"}, true},
		{"no match", "nginx", []string{"redis"}, false},
		{"empty patterns", "nginx", []string{}, false},
		{"wildcard suffix", "nginx-prod", []string{"nginx*"}, true},
		{"wildcard prefix", "my-nginx", []string{"*nginx"}, true},
		{"wildcard suffix with dash", "web-app", []string{"web-*"}, true},
		{"question mark wildcard", "web-app", []string{"???-app"}, true},
		{"none match multiple", "nginx", []string{"redis", "postgres"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchesFilter(tt.input, tt.patterns)
			if got != tt.want {
				t.Errorf("MatchesFilter(%q, %v) = %v, want %v", tt.input, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestLastScanTimeZeroBeforeScan(t *testing.T) {
	mock := newMockDocker()
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	cfg.SetPollInterval(1 * time.Hour)
	notifier := notify.NewMulti(log)
	u := NewUpdater(mock, checker, s, q, cfg, log, clk, notifier, nil)
	sched := NewScheduler(u, cfg, log, clk)

	if !sched.LastScanTime().IsZero() {
		t.Errorf("LastScanTime() = %v before any scan, want zero time", sched.LastScanTime())
	}
}

func TestSetPollInterval(t *testing.T) {
	mock := newMockDocker()
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	cfg.SetPollInterval(1 * time.Hour)
	notifier := notify.NewMulti(log)
	u := NewUpdater(mock, checker, s, q, cfg, log, clk, notifier, nil)
	sched := NewScheduler(u, cfg, log, clk)

	sched.SetPollInterval(30 * time.Minute)
	if cfg.PollInterval() != 30*time.Minute {
		t.Errorf("PollInterval() = %v after SetPollInterval, want 30m", cfg.PollInterval())
	}
}

func TestSetScanCallback(t *testing.T) {
	mock := newMockDocker()
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	cfg.SetPollInterval(1 * time.Hour)
	notifier := notify.NewMulti(log)
	u := NewUpdater(mock, checker, s, q, cfg, log, clk, notifier, nil)
	sched := NewScheduler(u, cfg, log, clk)

	called := false
	sched.SetScanCallback(func() { called = true })

	// Run a manual scan which triggers logResult -> scanCallback.
	ctx := context.Background()
	sched.TriggerScan(ctx)

	if !called {
		t.Error("scan callback was not called after TriggerScan")
	}
}

func TestTriggerScanUpdatesLastScanTime(t *testing.T) {
	mock := newMockDocker()
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	cfg.SetPollInterval(1 * time.Hour)
	notifier := notify.NewMulti(log)
	u := NewUpdater(mock, checker, s, q, cfg, log, clk, notifier, nil)
	sched := NewScheduler(u, cfg, log, clk)

	if !sched.LastScanTime().IsZero() {
		t.Fatal("LastScanTime() should be zero before scan")
	}

	sched.TriggerScan(context.Background())

	if sched.LastScanTime().IsZero() {
		t.Error("LastScanTime() is still zero after TriggerScan")
	}
	if !sched.LastScanTime().Equal(clk.Now()) {
		t.Errorf("LastScanTime() = %v, want %v", sched.LastScanTime(), clk.Now())
	}
}

func TestMaybeSelfUpdate_ConcurrentGuard(t *testing.T) {
	mock := newMockDocker()
	mock.containers = []container.Summary{
		{ID: "self123", Names: []string{"/sentinel"}, Image: "ghcr.io/test:1.0",
			Labels: map[string]string{"sentinel.self": "true"}},
	}
	s := testStore(t)
	q := NewQueue(s, nil, nil)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	cfg.SetGracePeriod(1 * time.Second)
	notifier := notify.NewMulti(log)
	u := NewUpdater(mock, checker, s, q, cfg, log, clk, notifier, nil)

	settings := &testSettings{data: map[string]string{"self_update_mode": "auto"}}
	sched := NewScheduler(u, cfg, log, clk)
	sched.SetSettingsReader(settings)
	sched.SetSelfUpdater(NewSelfUpdater(mock, log))

	// Simulate a self-update already in progress.
	sched.selfUpdating.Store(true)

	q.Add(PendingUpdate{
		ContainerName: "sentinel",
		CurrentImage:  "ghcr.io/test:1.0",
		NewerVersions: []string{"2.0"},
	})
	u.selfUpdateQueued.Store(true)
	u.selfUpdateKey.Store("sentinel")

	sched.maybeSelfUpdate()

	// Queue should still have the item (guard prevented the update).
	if q.Len() != 1 {
		t.Errorf("queue.Len() = %d, want 1 (concurrent guard should prevent update)", q.Len())
	}
}
