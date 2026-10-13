package main

import (
	"log/slog"
	"os"
)

// warnRemovedEnv logs a single warning when environment variables for the
// removed Portainer connector are still set. Values are never logged.
func warnRemovedEnv(log *slog.Logger) {
	if os.Getenv("SENTINEL_PORTAINER_URL") == "" && os.Getenv("SENTINEL_PORTAINER_TOKEN") == "" {
		return
	}
	log.Warn("Portainer support was removed in v2.17.0; SENTINEL_PORTAINER_URL and SENTINEL_PORTAINER_TOKEN are ignored")
}
