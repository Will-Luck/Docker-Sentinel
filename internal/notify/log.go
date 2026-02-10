package notify

import "context"

// LogNotifier writes every event as a structured log line. It is always
// enabled and serves as a guaranteed notification record.
type LogNotifier struct {
	log Logger
}

// NewLogNotifier creates a notifier that logs events using structured logging.
func NewLogNotifier(log Logger) *LogNotifier {
	return &LogNotifier{log: log}
}

// Name returns the provider name for logging.
func (l *LogNotifier) Name() string { return "log" }

// Send writes the event fields as structured key-value pairs at Info level.
func (l *LogNotifier) Send(_ context.Context, event Event) error {
	l.log.Info("notification event",
		"type", string(event.Type),
		"container", event.ContainerName,
		"old_image", event.OldImage,
		"new_image", event.NewImage,
		"old_digest", event.OldDigest,
		"new_digest", event.NewDigest,
		"error", event.Error,
		"timestamp", event.Timestamp.String(),
	)
	return nil
}
