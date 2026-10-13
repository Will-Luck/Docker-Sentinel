package main

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func TestRetentionDaysSettingOverridesEnv(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := &config.Config{HistoryRetentionDays: 90}
	if got := retentionDays(cfg, db); got != 90 {
		t.Fatalf("got %d, want env default 90", got)
	}
	_ = db.SaveSetting(store.SettingHistoryRetentionDays, strconv.Itoa(30))
	if got := retentionDays(cfg, db); got != 30 {
		t.Fatalf("got %d, want saved 30", got)
	}
	_ = db.SaveSetting(store.SettingHistoryRetentionDays, "0")
	if got := retentionDays(cfg, db); got != 0 {
		t.Fatalf("got %d, want 0 (keep everything)", got)
	}
}
