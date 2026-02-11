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

func TestCanonicaliseEventKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"update_complete", "update_succeeded"},
		{"rollback", "rollback_succeeded"},
		{"state_change", "container_state"},
		{"update_available", "update_available"},
		{"update_started", "update_started"},
		{"update_succeeded", "update_succeeded"},
		{"update_failed", "update_failed"},
		{"rollback_succeeded", "rollback_succeeded"},
		{"rollback_failed", "rollback_failed"},
		{"container_state", "container_state"},
		{"unknown_key", "unknown_key"},
	}
	for _, tt := range tests {
		got := canonicaliseEventKey(tt.input)
		if got != tt.want {
			t.Errorf("canonicaliseEventKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFilteredNotifierLegacyKeysMatchCurrentEvents(t *testing.T) {
	inner := &stubNotifier{name: "test"}

	// Simulate a channel saved with old legacy keys.
	f := newFilteredNotifier(inner, []string{"update_complete", "rollback", "state_change"})

	// "update_complete" should match EventUpdateSucceeded.
	if err := f.Send(context.Background(), testEvent(EventUpdateSucceeded)); err != nil {
		t.Fatalf("Send(EventUpdateSucceeded) error = %v", err)
	}
	if len(inner.sent) != 1 {
		t.Fatalf("EventUpdateSucceeded: got %d events, want 1", len(inner.sent))
	}

	// "rollback" should match EventRollbackOK (rollback_succeeded).
	if err := f.Send(context.Background(), testEvent(EventRollbackOK)); err != nil {
		t.Fatalf("Send(EventRollbackOK) error = %v", err)
	}
	if len(inner.sent) != 2 {
		t.Fatalf("EventRollbackOK: got %d events, want 2", len(inner.sent))
	}

	// "state_change" should match EventContainerState.
	if err := f.Send(context.Background(), testEvent(EventContainerState)); err != nil {
		t.Fatalf("Send(EventContainerState) error = %v", err)
	}
	if len(inner.sent) != 3 {
		t.Fatalf("EventContainerState: got %d events, want 3", len(inner.sent))
	}

	// Events NOT in the legacy set should still be blocked.
	if err := f.Send(context.Background(), testEvent(EventUpdateAvailable)); err != nil {
		t.Fatalf("Send(EventUpdateAvailable) error = %v", err)
	}
	if len(inner.sent) != 3 {
		t.Fatalf("EventUpdateAvailable should be blocked: got %d events, want 3", len(inner.sent))
	}
}

func TestFilteredNotifierMixedLegacyAndCurrentKeys(t *testing.T) {
	inner := &stubNotifier{name: "test"}

	// Mix of legacy and current keys.
	f := newFilteredNotifier(inner, []string{"update_complete", "update_available", "rollback_failed"})

	// "update_complete" (legacy) -> update_succeeded
	if err := f.Send(context.Background(), testEvent(EventUpdateSucceeded)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	// "update_available" (current, unchanged)
	if err := f.Send(context.Background(), testEvent(EventUpdateAvailable)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	// "rollback_failed" (current, unchanged)
	if err := f.Send(context.Background(), testEvent(EventRollbackFailed)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(inner.sent) != 3 {
		t.Fatalf("got %d events, want 3", len(inner.sent))
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
