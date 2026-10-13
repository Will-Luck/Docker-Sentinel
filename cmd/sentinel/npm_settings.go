package main

import (
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// npmSettings returns the Nginx Proxy Manager URL, email and password: the
// environment where it is set, otherwise the saved setting. Startup and the
// Test connection button both use it, so they agree.
func npmSettings(cfg *config.Config, load func(string) (string, error)) (url, email, password string) {
	pick := func(env, key string) string {
		if env != "" {
			return env
		}
		if v, err := load(key); err == nil {
			return v
		}
		return ""
	}
	return pick(cfg.NPMURL, store.SettingNPMURL), pick(cfg.NPMEmail, store.SettingNPMEmail), pick(cfg.NPMPassword, store.SettingNPMPassword)
}
