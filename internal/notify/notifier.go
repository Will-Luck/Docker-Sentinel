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
//
// When batchWindow is set (> 0), batchable events are buffered and flushed
// as aggregated summaries after the window elapses. Non-batchable events
// (e.g. update_started, rollback) are always sent immediately.
type Multi struct {
	mu        sync.RWMutex
	notifiers []Notifier
	log       Logger

	batchMu     sync.Mutex
	batchWindow time.Duration // 0 = disabled (immediate send)
	pending     []Event       // buffered events
	flushTimer  *time.Timer   // fires after batchWindow

	retryMu      sync.RWMutex
	maxRetries   int           // 0 = disabled (default)
	retryBackoff time.Duration // initial backoff, doubles each retry
}

// NewMulti creates a dispatcher from the given notifiers.
func NewMulti(log Logger, notifiers ...Notifier) *Multi {
	return &Multi{notifiers: notifiers, log: log}
}

// Notify sends an event to all registered notifiers.
// Returns true if at least one notifier succeeded (or none are configured).
// Errors are logged but never propagated — notifications must not block updates.
//
// When batching is enabled, batchable events are buffered and sent as
// aggregated summaries after the batch window elapses. Non-batchable events
// are always dispatched immediately.
func (m *Multi) Notify(ctx context.Context, event Event) bool {
	m.batchMu.Lock()
	window := m.batchWindow
	m.batchMu.Unlock()

	// No batching or non-batchable event: send immediately.
	if window <= 0 || !isBatchable(event.Type) {
		return m.dispatch(ctx, event)
	}

	// Buffer the event and start/reset the flush timer.
	m.batchMu.Lock()
	m.pending = append(m.pending, event)
	if m.flushTimer != nil {
		m.flushTimer.Stop()
	}
	m.flushTimer = time.AfterFunc(m.batchWindow, m.flush)
	m.batchMu.Unlock()

	return true
}

// SetBatchWindow configures the batching window at runtime.
// A duration of 0 disables batching (events are sent immediately).
func (m *Multi) SetBatchWindow(d time.Duration) {
	m.batchMu.Lock()
	m.batchWindow = d
	m.batchMu.Unlock()
}

// SetRetry configures retry behaviour for failed notification sends.
// maxRetries is the number of additional attempts after the first failure
// (0 = disabled). backoff is the initial delay before the first retry;
// it doubles on each subsequent attempt, capped at 30 seconds.
func (m *Multi) SetRetry(maxRetries int, backoff time.Duration) {
	m.retryMu.Lock()
	m.maxRetries = maxRetries
	m.retryBackoff = backoff
	m.retryMu.Unlock()
}

// Stop flushes any remaining pending events and stops the flush timer.
// Call this on shutdown to ensure no buffered events are lost.
func (m *Multi) Stop() {
	m.batchMu.Lock()
	if m.flushTimer != nil {
		m.flushTimer.Stop()
		m.flushTimer = nil
	}
	pending := m.pending
	m.pending = nil
	m.batchMu.Unlock()

	if len(pending) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, event := range aggregateEvents(pending) {
			m.dispatch(ctx, event)
		}
	}
}

// dispatch sends an event to all registered notifiers. This is the core
// fan-out logic used by both immediate sends and batch flushes.
func (m *Multi) dispatch(ctx context.Context, event Event) bool {
	m.mu.RLock()
	notifiers := m.notifiers
	m.mu.RUnlock()

	if len(notifiers) == 0 {
		return true
	}

	anyOK := false
	for _, n := range notifiers {
		if m.retrySend(ctx, n, event) {
			anyOK = true
		}
	}
	return anyOK
}

// retrySend attempts to send an event via a single notifier, retrying with
// exponential backoff on failure. Returns true if the send eventually
// succeeded, false if all attempts were exhausted or the context was cancelled.
func (m *Multi) retrySend(ctx context.Context, n Notifier, event Event) bool {
	m.retryMu.RLock()
	maxRetries := m.maxRetries
	backoff := m.retryBackoff
	m.retryMu.RUnlock()

	const maxBackoff = 30 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		err := n.Send(ctx, event)
		if err == nil {
			if attempt > 0 {
				m.log.Info("notification succeeded after retry",
					"provider", n.Name(),
					"event", string(event.Type),
					"attempt", attempt+1,
				)
			}
			return true
		}

		// Last attempt — no more retries, log final failure.
		if attempt == maxRetries {
			m.log.Error("notification failed",
				"provider", n.Name(),
				"event", string(event.Type),
				"container", event.ContainerName,
				"error", err.Error(),
			)
			return false
		}

		// Log the failure and wait before retrying.
		m.log.Error("notification failed, retrying",
			"provider", n.Name(),
			"event", string(event.Type),
			"container", event.ContainerName,
			"attempt", attempt+1,
			"next_backoff", backoff.String(),
			"error", err.Error(),
		)

		// Respect context cancellation during backoff wait.
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}

		// Double the backoff for the next attempt, capped at 30s.
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	return false
}

// flush aggregates pending events and dispatches the summaries.
// Called by the flush timer after the batch window elapses.
func (m *Multi) flush() {
	m.batchMu.Lock()
	pending := m.pending
	m.pending = nil
	m.flushTimer = nil
	m.batchMu.Unlock()

	if len(pending) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, event := range aggregateEvents(pending) {
		m.dispatch(ctx, event)
	}
}

// Reconfigure replaces the notifier chain at runtime.
func (m *Multi) Reconfigure(notifiers ...Notifier) {
	// Flush pending events with the old notifier list before swapping.
	m.batchMu.Lock()
	if m.flushTimer != nil {
		m.flushTimer.Stop()
		m.flushTimer = nil
	}
	pending := m.pending
	m.pending = nil
	m.batchMu.Unlock()

	// Dispatch any buffered events with the old notifiers.
	if len(pending) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, event := range aggregateEvents(pending) {
			m.dispatch(ctx, event)
		}
	}

	// Now swap the notifier chain.
	m.mu.Lock()
	m.notifiers = notifiers
	m.mu.Unlock()
}
