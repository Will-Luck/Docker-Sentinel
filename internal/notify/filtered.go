package notify

import "context"

// filteredNotifier wraps a Notifier and only forwards events whose type
// matches the allowed set. If the allowed set is empty, all events pass through.
type filteredNotifier struct {
	inner   Notifier
	allowed map[EventType]struct{}
}

// newFilteredNotifier creates a notifier that only forwards events matching
// the given event type strings. An empty list means all events are forwarded.
func newFilteredNotifier(inner Notifier, events []string) *filteredNotifier {
	allowed := make(map[EventType]struct{}, len(events))
	for _, e := range events {
		allowed[EventType(e)] = struct{}{}
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
