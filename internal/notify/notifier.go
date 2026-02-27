// Package notify provides event notification for Docker Sentinel.
package notify

import (
	"context"
	"sync"
	"time"
)

// EventType identifies what happened during an update lifecycle.
type EventType string

const (
	EventUpdateAvailable  EventType = "update_available"
	EventUpdateStarted    EventType = "update_started"
	EventUpdateSucceeded  EventType = "update_succeeded"
	EventUpdateFailed     EventType = "update_failed"
	EventRollbackOK       EventType = "rollback_succeeded"
	EventRollbackFailed   EventType = "rollback_failed"
	EventVersionAvailable EventType = "version_available"
	EventContainerState   EventType = "container_state"
	EventDigest           EventType = "digest"
)

// AllEventTypes returns all event types that can be filtered for notifications.
func AllEventTypes() []EventType {
	return []EventType{
		EventUpdateAvailable,
		EventVersionAvailable,
		EventUpdateStarted,
		EventUpdateSucceeded,
		EventUpdateFailed,
		EventRollbackOK,
		EventRollbackFailed,
		EventContainerState,
		EventDigest,
	}
}

// Event represents a notification event.
type Event struct {
	Type           EventType `json:"type"`
	ContainerName  string    `json:"container_name"`
	OldImage       string    `json:"old_image,omitempty"`
	NewImage       string    `json:"new_image,omitempty"`
	OldDigest      string    `json:"old_digest,omitempty"`
	NewDigest      string    `json:"new_digest,omitempty"`
	Error          string    `json:"error,omitempty"`
	ContainerNames []string  `json:"container_names,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
}

// Notifier sends events to an external system.
type Notifier interface {
	Send(ctx context.Context, event Event) error
	Name() string
}

// Logger is a minimal logging interface to avoid importing the logging package.
type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

// Multi fans out events to multiple notifiers.
// It never returns errors — failures are logged but don't block updates.
type Multi struct {
	mu        sync.RWMutex
	notifiers []Notifier
	log       Logger
}

// NewMulti creates a dispatcher from the given notifiers.
func NewMulti(log Logger, notifiers ...Notifier) *Multi {
	return &Multi{notifiers: notifiers, log: log}
}

// Notify sends an event to all registered notifiers.
// Returns true if at least one notifier succeeded (or none are configured).
// Errors are logged but never propagated — notifications must not block updates.
func (m *Multi) Notify(ctx context.Context, event Event) bool {
	m.mu.RLock()
	notifiers := m.notifiers
	m.mu.RUnlock()

	if len(notifiers) == 0 {
		return true
	}

	anyOK := false
	for _, n := range notifiers {
		if err := n.Send(ctx, event); err != nil {
			m.log.Error("notification failed",
				"provider", n.Name(),
				"event", string(event.Type),
				"container", event.ContainerName,
				"error", err.Error(),
			)
		} else {
			anyOK = true
		}
	}
	return anyOK
}

// Reconfigure replaces the notifier chain at runtime.
func (m *Multi) Reconfigure(notifiers ...Notifier) {
	m.mu.Lock()
	m.notifiers = notifiers
	m.mu.Unlock()
}
