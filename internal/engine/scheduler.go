package engine

import (
	"context"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
)

// Scheduler runs scan cycles at the configured poll interval.
type Scheduler struct {
	updater *Updater
	cfg     *config.Config
	log     *logging.Logger
	clock   clock.Clock
}

// NewScheduler creates a Scheduler.
func NewScheduler(u *Updater, cfg *config.Config, log *logging.Logger, clk clock.Clock) *Scheduler {
	return &Scheduler{
		updater: u,
		cfg:     cfg,
		log:     log,
		clock:   clk,
	}
}

// Run starts the scan loop. It performs an initial scan immediately,
// then scans at every poll interval. Exits when ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) error {
	s.log.Info("starting initial scan")
	result := s.updater.Scan(ctx)
	s.logResult(result)

	for {
		select {
		case <-s.clock.After(s.cfg.PollInterval):
			s.log.Info("starting scheduled scan")
			result := s.updater.Scan(ctx)
			s.logResult(result)
		case <-ctx.Done():
			s.log.Info("scheduler stopped")
			return nil
		}
	}
}

func (s *Scheduler) logResult(r ScanResult) {
	s.log.Info("scan complete",
		"total", r.Total,
		"skipped", r.Skipped,
		"auto", r.AutoCount,
		"queued", r.Queued,
		"updated", r.Updated,
		"failed", r.Failed,
		"errors", len(r.Errors),
	)
}
