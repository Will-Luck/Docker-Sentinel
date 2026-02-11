package engine

import (
	"context"
	"path"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
)

// SettingsReader reads runtime settings from the store.
type SettingsReader interface {
	LoadSetting(key string) (string, error)
}

// Scheduler runs scan cycles at the configured poll interval.
type Scheduler struct {
	updater  *Updater
	cfg      *config.Config
	log      *logging.Logger
	clock    clock.Clock
	settings SettingsReader
	resetCh  chan struct{}
	lastScan time.Time
}

// NewScheduler creates a Scheduler.
func NewScheduler(u *Updater, cfg *config.Config, log *logging.Logger, clk clock.Clock) *Scheduler {
	return &Scheduler{
		updater: u,
		cfg:     cfg,
		log:     log,
		clock:   clk,
		resetCh: make(chan struct{}, 1),
	}
}

// SetSettingsReader attaches a settings reader for runtime pause/filter checks.
func (s *Scheduler) SetSettingsReader(sr SettingsReader) {
	s.settings = sr
}

// Run starts the scan loop. It performs an initial scan immediately,
// then scans at every poll interval. Exits when ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) error {
	if !s.isPaused() {
		s.log.Info("starting initial scan")
		result := s.updater.Scan(ctx)
		s.lastScan = s.clock.Now()
		s.logResult(result)
	} else {
		s.log.Info("scheduler is paused, skipping initial scan")
	}

	for {
		select {
		case <-s.clock.After(s.cfg.PollInterval()):
			if s.isPaused() {
				s.log.Info("scheduler is paused, skipping scheduled scan")
				continue
			}
			s.log.Info("starting scheduled scan")
			result := s.updater.Scan(ctx)
			s.lastScan = s.clock.Now()
			s.logResult(result)
		case <-s.resetCh:
			s.log.Info("poll interval changed, resetting timer", "interval", s.cfg.PollInterval())
			// Timer resets on next loop iteration
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

// SetPollInterval updates the poll interval at runtime and signals the scheduler to reset its timer.
func (s *Scheduler) SetPollInterval(d time.Duration) {
	s.cfg.SetPollInterval(d)
	s.log.Info("poll interval updated", "interval", d)
	// Non-blocking send to signal the run loop.
	select {
	case s.resetCh <- struct{}{}:
	default:
	}
}

// TriggerScan runs an immediate scan cycle outside the normal timer.
func (s *Scheduler) TriggerScan(ctx context.Context) {
	s.log.Info("starting manual scan")
	result := s.updater.Scan(ctx)
	s.lastScan = s.clock.Now()
	s.logResult(result)
}

// LastScanTime returns when the last scan completed.
func (s *Scheduler) LastScanTime() time.Time {
	return s.lastScan
}

// isPaused checks whether the scheduler is paused via a runtime setting.
func (s *Scheduler) isPaused() bool {
	if s.settings == nil {
		return false
	}
	val, err := s.settings.LoadSetting("paused")
	if err != nil {
		s.log.Warn("failed to read pause setting", "error", err)
		return false
	}
	return val == "true"
}

// MatchesFilter checks whether a container name matches any of the given glob patterns.
func MatchesFilter(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, name); matched {
			return true
		}
	}
	return false
}
