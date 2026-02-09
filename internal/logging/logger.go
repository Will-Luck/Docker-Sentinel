package logging

import (
	"log/slog"
	"os"
)

// Logger wraps slog for structured logging.
type Logger struct {
	*slog.Logger
}

// New creates a Logger that outputs text or JSON depending on config.
func New(jsonMode bool) *Logger {
	var handler slog.Handler
	if jsonMode {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	}
	return &Logger{slog.New(handler)}
}
