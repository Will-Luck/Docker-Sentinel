package main

import (
	"context"
	"strconv"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// retentionDays returns how many days of history and activity log to keep:
// the saved setting if there is one, else SENTINEL_HISTORY_RETENTION_DAYS.
// 0 keeps everything.
func retentionDays(cfg *config.Config, db *store.Store) int {
	if v, _ := db.LoadSetting(store.SettingHistoryRetentionDays); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return cfg.HistoryRetentionDays
}

// pruneOnce applies the retention policy once.
func pruneOnce(cfg *config.Config, db *store.Store, log *logging.Logger, now time.Time) {
	days := retentionDays(cfg, db)
	if days <= 0 {
		return
	}
	cutoff := now.AddDate(0, 0, -days)
	h, err := db.PruneHistory(cutoff)
	if err != nil {
		log.Warn("history retention failed", "error", err)
	}
	l, err := db.PruneLogs(cutoff)
	if err != nil {
		log.Warn("log retention failed", "error", err)
	}
	if h+l > 0 {
		log.Info("pruned old records", "history", h, "log", l, "keep_days", days)
	}
}

// runRetention prunes at startup and then daily until ctx ends.
func runRetention(ctx context.Context, cfg *config.Config, db *store.Store, log *logging.Logger) {
	pruneOnce(cfg, db, log, time.Now())
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			pruneOnce(cfg, db, log, now)
		}
	}
}
