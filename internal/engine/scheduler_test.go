package engine

import (
	"context"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
)

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
