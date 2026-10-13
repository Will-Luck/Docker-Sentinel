package main

import (
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
)

func TestApplySavedOverrides_SavedBeatsEnv(t *testing.T) {
	t.Setenv("SENTINEL_IMAGE_BACKUP", "true")
	t.Setenv("SENTINEL_REMOVE_VOLUMES", "true")
	t.Setenv("SENTINEL_ROLLBACK_POLICY", "manual")
	cfg := config.Load()

	applySavedOverrides(cfg, map[string]string{
		"image_backup":    "false",
		"remove_volumes":  "false",
		"rollback_policy": "", // saved as "no change"
	})
	if cfg.ImageBackup() || cfg.RemoveVolumes() {
		t.Errorf("saved false must override env true: backup=%v volumes=%v", cfg.ImageBackup(), cfg.RemoveVolumes())
	}
	if cfg.RollbackPolicy() != "" {
		t.Errorf("rollback policy = %q, want the saved empty value to override env", cfg.RollbackPolicy())
	}
}

func TestApplySavedOverrides_NothingSavedKeepsEnv(t *testing.T) {
	t.Setenv("SENTINEL_IMAGE_BACKUP", "true")
	t.Setenv("SENTINEL_ROLLBACK_POLICY", "pinned")
	cfg := config.Load()
	applySavedOverrides(cfg, map[string]string{})
	if !cfg.ImageBackup() || cfg.RollbackPolicy() != "pinned" {
		t.Errorf("env values lost: backup=%v policy=%q", cfg.ImageBackup(), cfg.RollbackPolicy())
	}
}
