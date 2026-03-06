package engine

import (
	"context"
	"path"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

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
	updater      *Updater
	cfg          *config.Config
	log          *logging.Logger
	clock        clock.Clock
	settings     SettingsReader
	selfUpdater  *SelfUpdater // optional: for auto self-update when idle
	resetCh      chan struct{}
	mu           sync.Mutex
	lastScan     time.Time
	readyGate    <-chan struct{} // if set, wait for close before initial scan
	scanCallback func()          // called after each scan completes (optional)
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

// SetScanCallback registers a function called after each scan completes.
func (s *Scheduler) SetScanCallback(fn func()) {
	s.scanCallback = fn
}

// SetSelfUpdater attaches a self-updater for optional auto self-update when idle.
func (s *Scheduler) SetSelfUpdater(su *SelfUpdater) {
	s.selfUpdater = su
}

// SetReadyGate sets a channel the scheduler waits on before running the initial
// scan. Used after fresh setup so that no scans fire until the user has loaded
// the dashboard and can actually see the results.
func (s *Scheduler) SetReadyGate(ch <-chan struct{}) {
	s.readyGate = ch
}

// Run starts the scan loop. It performs an initial scan immediately,
// then scans at every poll interval. Exits when ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) error {
	if s.readyGate != nil {
		s.log.Info("deferring initial scan until dashboard is loaded")
		select {
		case <-s.readyGate:
		case <-ctx.Done():
			s.log.Info("scheduler stopped")
			return nil
		}
	}

	if !s.isPaused() {
		s.log.Info("starting initial scan")
		result := s.updater.Scan(ctx, ScanScheduled)
		s.mu.Lock()
		s.lastScan = s.clock.Now()
		s.mu.Unlock()
		s.logResult(result)
	} else {
		s.log.Info("scheduler is paused, skipping initial scan")
	}

	for {
		select {
		case <-s.nextTick():
			if s.isPaused() {
				s.log.Info("scheduler is paused, skipping scheduled scan")
				continue
			}
			s.log.Info("starting scheduled scan")
			result := s.updater.Scan(ctx, ScanScheduled)
			s.mu.Lock()
			s.lastScan = s.clock.Now()
			s.mu.Unlock()
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
	if s.scanCallback != nil {
		s.scanCallback()
	}
	s.maybeSelfUpdate()
}

// maybeSelfUpdate triggers a self-update if:
// 1. self_update_mode is "auto"
// 2. a self-update was queued during this scan
// 3. no other container updates are in progress
func (s *Scheduler) maybeSelfUpdate() {
	if s.selfUpdater == nil || s.settings == nil {
		return
	}
	if !s.updater.SelfUpdateQueued() {
		return
	}
	mode, err := s.settings.LoadSetting("self_update_mode")
	if err != nil {
		s.log.Warn("failed to read self_update_mode", "error", err)
		return
	}
	if mode != "auto" {
		return
	}
	if !s.updater.IsIdle() {
		s.log.Info("self-update deferred: other updates in progress")
		return
	}

	// Look up the queued self-update entry by key stored during scan.
	key := s.updater.SelfUpdateKey()
	if key == "" {
		return
	}
	item, ok := s.updater.queue.Get(key)
	if !ok {
		return
	}

	// Build target image from the newest version.
	var targetImage string
	if len(item.NewerVersions) > 0 {
		targetImage = replaceTag(item.CurrentImage, item.NewerVersions[0])
	}

	// Remove from queue before triggering so it doesn't re-fire.
	s.updater.queue.Remove(key)

	s.log.Info("auto self-update triggered (idle, mode=auto)", "target", targetImage)
	go func() {
		if err := s.selfUpdater.Update(context.Background(), targetImage); err != nil {
			s.log.Error("auto self-update failed", "error", err)
		}
	}()
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
	result := s.updater.Scan(ctx, ScanManual)
	s.mu.Lock()
	s.lastScan = s.clock.Now()
	s.mu.Unlock()
	s.logResult(result)
}

// LastScanTime returns when the last scan completed.
func (s *Scheduler) LastScanTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// nextTick returns a channel that fires at the next scheduled time.
// If a cron schedule is configured, it computes the next fire time from the expression.
// Otherwise, it falls back to the poll interval.
func (s *Scheduler) nextTick() <-chan time.Time {
	if sched := s.cfg.Schedule(); sched != "" {
		parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		schedule, err := parser.Parse(sched)
		if err != nil {
			s.log.Warn("invalid cron schedule, falling back to poll interval", "schedule", sched, "error", err)
			return s.clock.After(s.cfg.PollInterval())
		}
		now := s.clock.Now()
		next := schedule.Next(now)
		wait := next.Sub(now)
		if wait < 0 {
			wait = 0
		}
		s.log.Debug("next cron tick", "schedule", sched, "next", next, "wait", wait)
		return s.clock.After(wait)
	}
	return s.clock.After(s.cfg.PollInterval())
}

// SetSchedule updates the cron schedule at runtime and signals the scheduler to reset.
func (s *Scheduler) SetSchedule(sched string) {
	s.cfg.SetSchedule(sched)
	s.log.Info("schedule updated", "schedule", sched)
	select {
	case s.resetCh <- struct{}{}:
	default:
	}
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
