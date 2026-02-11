package notify

import "context"

// legacyEventKeys maps old event filter key strings (saved in BoltDB by earlier
// frontend versions) to the current EventType constants. This ensures channels
// configured before the key rename still match correctly.
var legacyEventKeys = map[string]string{
	"update_complete": "update_succeeded",
	"rollback":        "rollback_succeeded",
	"state_change":    "container_state",
}

// canonicaliseEventKey returns the current EventType string for a given key,
// mapping legacy keys to their modern equivalents.
func canonicaliseEventKey(key string) string {
	if mapped, ok := legacyEventKeys[key]; ok {
		return mapped
	}
	return key
}

// filteredNotifier wraps a Notifier and only forwards events whose type
// matches the allowed set. If the allowed set is empty, all events pass through.
type filteredNotifier struct {
	inner   Notifier
	allowed map[EventType]struct{}
}

// newFilteredNotifier creates a notifier that only forwards events matching
// the given event type strings. An empty list means all events are forwarded.
// Legacy event key strings are canonicalised to current constants automatically.
func newFilteredNotifier(inner Notifier, events []string) *filteredNotifier {
	allowed := make(map[EventType]struct{}, len(events))
	for _, e := range events {
		allowed[EventType(canonicaliseEventKey(e))] = struct{}{}
	}
	return &filteredNotifier{inner: inner, allowed: allowed}
}

// Name returns the name of the wrapped notifier.
func (f *filteredNotifier) Name() string { return f.inner.Name() }

// Send forwards the event to the inner notifier only if the event type
// is in the allowed set.
func (f *filteredNotifier) Send(ctx context.Context, event Event) error {
	if len(f.allowed) > 0 {
		if _, ok := f.allowed[event.Type]; !ok {
			return nil
		}
	}
	return f.inner.Send(ctx, event)
}
