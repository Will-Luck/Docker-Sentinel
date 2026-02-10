package engine

import (
	"context"
	"testing"
	"time"

	"github.com/GiteaLN/Docker-Sentinel/internal/config"
	"github.com/GiteaLN/Docker-Sentinel/internal/logging"
	"github.com/GiteaLN/Docker-Sentinel/internal/notify"
	"github.com/GiteaLN/Docker-Sentinel/internal/registry"
)

func TestSchedulerRunsInitialScan(t *testing.T) {
	mock := newMockDocker()
	s := testStore(t)
	q := NewQueue(s)
	log := logging.New(false)
	clk := newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checker := registry.NewChecker(mock, log)
	cfg := &config.Config{
		DefaultPolicy: "manual",
		GracePeriod:   1 * time.Second,
		PollInterval:  1 * time.Hour,
	}
	notifier := notify.NewMulti(log)
	u := NewUpdater(mock, checker, s, q, cfg, log, clk, notifier)
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
