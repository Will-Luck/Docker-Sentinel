package notify

import (
	"context"
	"testing"
)

func TestFilteredNotifierAllowsMatchingEvents(t *testing.T) {
	inner := &stubNotifier{name: "test"}
	f := newFilteredNotifier(inner, []string{"update_available", "update_failed"})

	// Should be forwarded.
	if err := f.Send(context.Background(), testEvent(EventUpdateAvailable)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(inner.sent) != 1 {
		t.Fatalf("got %d events, want 1", len(inner.sent))
	}

	// Should also be forwarded.
	if err := f.Send(context.Background(), testEvent(EventUpdateFailed)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(inner.sent) != 2 {
		t.Fatalf("got %d events, want 2", len(inner.sent))
	}
}

func TestFilteredNotifierBlocksNonMatchingEvents(t *testing.T) {
	inner := &stubNotifier{name: "test"}
	f := newFilteredNotifier(inner, []string{"update_available"})

	// Should be blocked.
	if err := f.Send(context.Background(), testEvent(EventUpdateSucceeded)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(inner.sent) != 0 {
		t.Fatalf("got %d events, want 0 (should be filtered out)", len(inner.sent))
	}
}

func TestFilteredNotifierEmptyFilterAllowsAll(t *testing.T) {
	inner := &stubNotifier{name: "test"}
	f := newFilteredNotifier(inner, []string{})

	// All events should pass through.
	if err := f.Send(context.Background(), testEvent(EventUpdateAvailable)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if err := f.Send(context.Background(), testEvent(EventUpdateSucceeded)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if err := f.Send(context.Background(), testEvent(EventRollbackOK)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(inner.sent) != 3 {
		t.Fatalf("got %d events, want 3 (empty filter should pass all)", len(inner.sent))
	}
}

func TestFilteredNotifierNilFilterAllowsAll(t *testing.T) {
	inner := &stubNotifier{name: "test"}
	f := newFilteredNotifier(inner, nil)

	if err := f.Send(context.Background(), testEvent(EventUpdateFailed)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(inner.sent) != 1 {
		t.Fatalf("got %d events, want 1 (nil filter should pass all)", len(inner.sent))
	}
}

func TestFilteredNotifierPreservesName(t *testing.T) {
	inner := &stubNotifier{name: "gotify"}
	f := newFilteredNotifier(inner, []string{"update_available"})

	if f.Name() != "gotify" {
		t.Errorf("Name() = %q, want %q", f.Name(), "gotify")
	}
}

func TestBuildFilteredNotifierWithEvents(t *testing.T) {
	settings := []byte(`{"url":"http://example.com","token":"tok"}`)
	ch := Channel{
		ID:       "test-1",
		Type:     ProviderGotify,
		Name:     "Gotify",
		Enabled:  true,
		Settings: settings,
		Events:   []string{"update_available", "update_failed"},
	}

	n, err := BuildFilteredNotifier(ch)
	if err != nil {
		t.Fatalf("BuildFilteredNotifier() error = %v", err)
	}

	// Should be a filteredNotifier wrapping gotify.
	if _, ok := n.(*filteredNotifier); !ok {
		t.Errorf("expected *filteredNotifier, got %T", n)
	}
}

func TestBuildFilteredNotifierWithoutEvents(t *testing.T) {
	settings := []byte(`{"url":"http://example.com","token":"tok"}`)
	ch := Channel{
		ID:       "test-2",
		Type:     ProviderGotify,
		Name:     "Gotify",
		Enabled:  true,
		Settings: settings,
	}

	n, err := BuildFilteredNotifier(ch)
	if err != nil {
		t.Fatalf("BuildFilteredNotifier() error = %v", err)
	}

	// Should be a plain Gotify notifier (no filter wrapper).
	if _, ok := n.(*Gotify); !ok {
		t.Errorf("expected *Gotify (no filter), got %T", n)
	}
}
