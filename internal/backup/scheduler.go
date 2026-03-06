package backup

import (
	"context"
	"sync"

	cron "github.com/robfig/cron/v3"
)

// Scheduler runs backups on a cron schedule.
type Scheduler struct {
	mu      sync.Mutex
	mgr     *Manager
	log     Logger
	cron    *cron.Cron
	entryID cron.EntryID
}

// NewScheduler creates a backup scheduler. It does not start until SetSchedule is called.
func NewScheduler(mgr *Manager, log Logger) *Scheduler {
	return &Scheduler{
		mgr:  mgr,
		log:  log,
		cron: cron.New(),
	}
}

// SetSchedule configures the cron expression. Empty string disables scheduling.
func (s *Scheduler) SetSchedule(expr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing schedule.
	if s.entryID != 0 {
		s.cron.Remove(s.entryID)
		s.entryID = 0
	}

	if expr == "" {
		s.log.Info("backup schedule disabled")
		return nil
	}

	id, err := s.cron.AddFunc(expr, func() {
		if _, backupErr := s.mgr.CreateBackup(context.Background()); backupErr != nil {
			s.log.Error("scheduled backup failed", "error", backupErr)
		}
	})
	if err != nil {
		return err
	}

	s.entryID = id
	s.log.Info("backup schedule configured", "cron", expr)
	return nil
}

// Start begins the cron scheduler.
func (s *Scheduler) Start() {
	s.cron.Start()
}

// Stop shuts down the scheduler gracefully.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}
