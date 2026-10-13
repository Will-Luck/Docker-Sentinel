package main

import "github.com/Will-Luck/Docker-Sentinel/internal/config"

// applySavedOverrides makes a value saved in the UI replace the environment
// default for the settings the engine reads from the runtime config. A key
// that was never saved leaves the environment value in force; a saved empty
// rollback policy ("no change") is still a saved value and wins.
func applySavedOverrides(cfg *config.Config, saved map[string]string) {
	if v, ok := saved["image_backup"]; ok && v != "" {
		cfg.SetImageBackup(v == "true")
	}
	if v, ok := saved["remove_volumes"]; ok && v != "" {
		cfg.SetRemoveVolumes(v == "true")
	}
	if v, ok := saved["rollback_policy"]; ok {
		cfg.SetRollbackPolicy(v)
	}
}
