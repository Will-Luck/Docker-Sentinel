package notify

import (
	"context"
	"sync"
	"testing"
	"time"
)

// safeNotifier is a thread-safe version of stubNotifier for tests involving
// timer-based flushes where Send() is called from a background goroutine.
type safeNotifier struct {
	mu   sync.Mutex
	name string
	sent []Event
}

func (s *safeNotifier) Name() string { return s.name }
func (s *safeNotifier) Send(_ context.Context, event Event) error {
	s.mu.Lock()
	s.sent = append(s.sent, event)
	s.mu.Unlock()
	return nil
}
func (s *safeNotifier) events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]Event, len(s.sent))
	copy(cp, s.sent)
	return cp
}

// --- isBatchable tests ---

func TestIsBatchable(t *testing.T) {
	batchable := []EventType{
		EventUpdateAvailable,
		EventVersionAvailable,
		EventUpdateSucceeded,
		EventUpdateFailed,
	}
	for _, et := range batchable {
		if !isBatchable(et) {
			t.Errorf("isBatchable(%q) = false, want true", et)
		}
	}

	passThrough := []EventType{
		EventUpdateStarted,
		EventRollbackOK,
		EventRollbackFailed,
		EventContainerState,
		EventDigest,
	}
	for _, et := range passThrough {
		if isBatchable(et) {
			t.Errorf("isBatchable(%q) = true, want false", et)
		}
	}
}

// --- aggregateEvents tests ---

func TestAggregateEvents_Empty(t *testing.T) {
	result := aggregateEvents(nil)
	if result != nil {
		t.Errorf("aggregateEvents(nil) = %v, want nil", result)
	}

	result = aggregateEvents([]Event{})
	if result != nil {
		t.Errorf("aggregateEvents([]) = %v, want nil", result)
	}
}

func TestAggregateEvents_SingleEvent(t *testing.T) {
	event := Event{
		Type:          EventUpdateAvailable,
		ContainerName: "nginx",
		OldImage:      "nginx:1.25",
		NewImage:      "nginx:1.26",
		Timestamp:     time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}

	result := aggregateEvents([]Event{event})
	if len(result) != 1 {
		t.Fatalf("got %d events, want 1", len(result))
	}
	if result[0].ContainerName != "nginx" {
		t.Errorf("ContainerName = %q, want nginx", result[0].ContainerName)
	}
	if result[0].Type != EventUpdateAvailable {
		t.Errorf("Type = %q, want update_available", result[0].Type)
	}
	// M3: single events should also populate ContainerNames for consumer consistency.
	if len(result[0].ContainerNames) != 1 || result[0].ContainerNames[0] != "nginx" {
		t.Errorf("ContainerNames = %v, want [nginx]", result[0].ContainerNames)
	}
}

func TestAggregateEvents_MultipleAvailable(t *testing.T) {
	events := []Event{
		{Type: EventUpdateAvailable, ContainerName: "nginx", Timestamp: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)},
		{Type: EventUpdateAvailable, ContainerName: "redis", Timestamp: time.Date(2026, 3, 1, 12, 0, 1, 0, time.UTC)},
		{Type: EventUpdateAvailable, ContainerName: "postgres", Timestamp: time.Date(2026, 3, 1, 12, 0, 2, 0, time.UTC)},
	}

	result := aggregateEvents(events)
	if len(result) != 1 {
		t.Fatalf("got %d events, want 1", len(result))
	}

	r := result[0]
	if r.Type != EventUpdateAvailable {
		t.Errorf("Type = %q, want update_available", r.Type)
	}
	if r.ContainerName != "3 containers" {
		t.Errorf("ContainerName = %q, want '3 containers'", r.ContainerName)
	}
	if len(r.ContainerNames) != 3 {
		t.Fatalf("ContainerNames length = %d, want 3", len(r.ContainerNames))
	}
	if r.ContainerNames[0] != "nginx" || r.ContainerNames[1] != "redis" || r.ContainerNames[2] != "postgres" {
		t.Errorf("ContainerNames = %v, want [nginx redis postgres]", r.ContainerNames)
	}
	// Timestamp should be from the last event.
	if !r.Timestamp.Equal(events[2].Timestamp) {
		t.Errorf("Timestamp = %v, want %v", r.Timestamp, events[2].Timestamp)
	}
}

func TestAggregateEvents_MultipleVersionAvailable(t *testing.T) {
	events := []Event{
		{Type: EventVersionAvailable, ContainerName: "app-a", Timestamp: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)},
		{Type: EventVersionAvailable, ContainerName: "app-b", Timestamp: time.Date(2026, 3, 1, 12, 0, 1, 0, time.UTC)},
	}

	result := aggregateEvents(events)
	if len(result) != 1 {
		t.Fatalf("got %d events, want 1", len(result))
	}
	if result[0].ContainerName != "2 containers" {
		t.Errorf("ContainerName = %q, want '2 containers'", result[0].ContainerName)
	}
	if result[0].Type != EventVersionAvailable {
		t.Errorf("Type = %q, want version_available", result[0].Type)
	}
}

func TestAggregateEvents_MixedSucceededFailed(t *testing.T) {
	events := []Event{
		{Type: EventUpdateSucceeded, ContainerName: "nginx", Timestamp: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)},
		{Type: EventUpdateSucceeded, ContainerName: "redis", Timestamp: time.Date(2026, 3, 1, 12, 0, 1, 0, time.UTC)},
		{Type: EventUpdateFailed, ContainerName: "postgres", Error: "pull timeout", Timestamp: time.Date(2026, 3, 1, 12, 0, 2, 0, time.UTC)},
	}

	result := aggregateEvents(events)
	if len(result) != 2 {
		t.Fatalf("got %d events, want 2", len(result))
	}

	// First should be the succeeded summary.
	s := result[0]
	if s.Type != EventUpdateSucceeded {
		t.Errorf("result[0].Type = %q, want update_succeeded", s.Type)
	}
	if s.ContainerName != "2 containers" {
		t.Errorf("succeeded ContainerName = %q, want '2 containers'", s.ContainerName)
	}
	if len(s.ContainerNames) != 2 {
		t.Fatalf("succeeded ContainerNames length = %d, want 2", len(s.ContainerNames))
	}
	// Should include failure count in Error field.
	if s.Error != "2 succeeded, 1 failed" {
		t.Errorf("succeeded Error = %q, want '2 succeeded, 1 failed'", s.Error)
	}

	// Second should be the failed summary.
	f := result[1]
	if f.Type != EventUpdateFailed {
		t.Errorf("result[1].Type = %q, want update_failed", f.Type)
	}
	if f.ContainerName != "1 containers" {
		t.Errorf("failed ContainerName = %q, want '1 containers'", f.ContainerName)
	}
	if f.Error != "postgres: pull timeout" {
		t.Errorf("failed Error = %q, want 'postgres: pull timeout'", f.Error)
	}
}

func TestAggregateEvents_OnlySucceeded(t *testing.T) {
	events := []Event{
		{Type: EventUpdateSucceeded, ContainerName: "nginx", Timestamp: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)},
		{Type: EventUpdateSucceeded, ContainerName: "redis", Timestamp: time.Date(2026, 3, 1, 12, 0, 1, 0, time.UTC)},
	}

	result := aggregateEvents(events)
	if len(result) != 1 {
		t.Fatalf("got %d events, want 1", len(result))
	}
	if result[0].Error != "" {
		t.Errorf("Error = %q, want empty (no failures)", result[0].Error)
	}
	if result[0].ContainerName != "2 containers" {
		t.Errorf("ContainerName = %q, want '2 containers'", result[0].ContainerName)
	}
}

func TestAggregateEvents_SingleSucceeded(t *testing.T) {
	// A single succeeded event should pass through unchanged.
	event := Event{
		Type:          EventUpdateSucceeded,
		ContainerName: "nginx",
		OldImage:      "nginx:1.25",
		NewImage:      "nginx:1.26",
		Timestamp:     time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}
	result := aggregateEvents([]Event{event})
	if len(result) != 1 {
		t.Fatalf("got %d events, want 1", len(result))
	}
	if result[0].ContainerName != "nginx" {
		t.Errorf("ContainerName = %q, want nginx (passthrough)", result[0].ContainerName)
	}
	if result[0].OldImage != "nginx:1.25" {
		t.Errorf("OldImage = %q, want nginx:1.25 (preserved)", result[0].OldImage)
	}
	// M3: single events should populate ContainerNames.
	if len(result[0].ContainerNames) != 1 || result[0].ContainerNames[0] != "nginx" {
		t.Errorf("ContainerNames = %v, want [nginx]", result[0].ContainerNames)
	}
}

func TestAggregateEvents_MixedTypes(t *testing.T) {
	// Mix of available and succeeded events: both types get separate summaries.
	events := []Event{
		{Type: EventUpdateAvailable, ContainerName: "nginx", Timestamp: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)},
		{Type: EventUpdateAvailable, ContainerName: "redis", Timestamp: time.Date(2026, 3, 1, 12, 0, 1, 0, time.UTC)},
		{Type: EventUpdateSucceeded, ContainerName: "postgres", Timestamp: time.Date(2026, 3, 1, 12, 0, 2, 0, time.UTC)},
	}

	result := aggregateEvents(events)
	if len(result) != 2 {
		t.Fatalf("got %d events, want 2", len(result))
	}

	// First: aggregated available.
	if result[0].Type != EventUpdateAvailable {
		t.Errorf("result[0].Type = %q, want update_available", result[0].Type)
	}
	if result[0].ContainerName != "2 containers" {
		t.Errorf("result[0].ContainerName = %q, want '2 containers'", result[0].ContainerName)
	}

	// Second: single succeeded passes through.
	if result[1].Type != EventUpdateSucceeded {
		t.Errorf("result[1].Type = %q, want update_succeeded", result[1].Type)
	}
	if result[1].ContainerName != "postgres" {
		t.Errorf("result[1].ContainerName = %q, want postgres", result[1].ContainerName)
	}
}

// --- Multi batch integration tests ---

func TestMultiBatchImmediate(t *testing.T) {
	// With batchWindow=0 (default), events are sent immediately.
	spy := &stubNotifier{name: "spy"}
	log := &spyLogger{}
	m := NewMulti(log, spy)

	event := testEvent(EventUpdateAvailable)
	ok := m.Notify(context.Background(), event)

	if !ok {
		t.Error("Notify() = false, want true")
	}
	if len(spy.sent) != 1 {
		t.Fatalf("got %d events, want 1 (immediate)", len(spy.sent))
	}
	if spy.sent[0].ContainerName != "nginx" {
		t.Errorf("ContainerName = %q, want nginx", spy.sent[0].ContainerName)
	}
}

func TestMultiBatchBuffers(t *testing.T) {
	// With batchWindow > 0, batchable events are buffered and flushed after the window.
	// Uses safeNotifier because flush runs from a timer goroutine.
	spy := &safeNotifier{name: "spy"}
	log := &spyLogger{}
	m := NewMulti(log, spy)
	m.SetBatchWindow(50 * time.Millisecond)

	// Send several batchable events rapidly.
	for _, name := range []string{"nginx", "redis", "postgres"} {
		m.Notify(context.Background(), Event{
			Type:          EventUpdateAvailable,
			ContainerName: name,
			Timestamp:     time.Now(),
		})
	}

	// Nothing should have been sent yet.
	if evts := spy.events(); len(evts) != 0 {
		t.Fatalf("got %d events immediately, want 0 (buffered)", len(evts))
	}

	// Wait for the flush timer to fire.
	time.Sleep(120 * time.Millisecond)

	evts := spy.events()
	if len(evts) != 1 {
		t.Fatalf("got %d events after flush, want 1 (aggregated)", len(evts))
	}
	if evts[0].ContainerName != "3 containers" {
		t.Errorf("ContainerName = %q, want '3 containers'", evts[0].ContainerName)
	}
	if len(evts[0].ContainerNames) != 3 {
		t.Errorf("ContainerNames length = %d, want 3", len(evts[0].ContainerNames))
	}
}

func TestMultiBatchPassThrough(t *testing.T) {
	// Non-batchable events send immediately even with batch window active.
	spy := &stubNotifier{name: "spy"}
	log := &spyLogger{}
	m := NewMulti(log, spy)
	m.SetBatchWindow(5 * time.Second) // Long window, but shouldn't matter.

	// update_started is not batchable.
	event := testEvent(EventUpdateStarted)
	ok := m.Notify(context.Background(), event)

	if !ok {
		t.Error("Notify() = false, want true")
	}
	if len(spy.sent) != 1 {
		t.Fatalf("got %d events, want 1 (immediate pass-through)", len(spy.sent))
	}
	if spy.sent[0].Type != EventUpdateStarted {
		t.Errorf("Type = %q, want update_started", spy.sent[0].Type)
	}

	// Also test rollback events pass through.
	m.Notify(context.Background(), Event{
		Type:          EventRollbackOK,
		ContainerName: "redis",
		Timestamp:     time.Now(),
	})
	if len(spy.sent) != 2 {
		t.Fatalf("got %d events, want 2 (both pass-through)", len(spy.sent))
	}
}

func TestMultiBatchStop(t *testing.T) {
	// Stop() flushes remaining pending events.
	spy := &stubNotifier{name: "spy"}
	log := &spyLogger{}
	m := NewMulti(log, spy)
	m.SetBatchWindow(10 * time.Second) // Very long, won't fire naturally.

	// Buffer some events.
	m.Notify(context.Background(), Event{
		Type:          EventUpdateSucceeded,
		ContainerName: "nginx",
		Timestamp:     time.Now(),
	})
	m.Notify(context.Background(), Event{
		Type:          EventUpdateSucceeded,
		ContainerName: "redis",
		Timestamp:     time.Now(),
	})

	if len(spy.sent) != 0 {
		t.Fatalf("got %d events before Stop, want 0", len(spy.sent))
	}

	// Stop should flush immediately (synchronous).
	m.Stop()

	if len(spy.sent) != 1 {
		t.Fatalf("got %d events after Stop, want 1 (aggregated)", len(spy.sent))
	}
	if spy.sent[0].ContainerName != "2 containers" {
		t.Errorf("ContainerName = %q, want '2 containers'", spy.sent[0].ContainerName)
	}
}

func TestMultiBatchStopEmpty(t *testing.T) {
	// Stop() with no pending events is a no-op.
	spy := &stubNotifier{name: "spy"}
	log := &spyLogger{}
	m := NewMulti(log, spy)
	m.SetBatchWindow(time.Second)

	m.Stop() // Should not panic or send anything.

	if len(spy.sent) != 0 {
		t.Fatalf("got %d events, want 0", len(spy.sent))
	}
}

func TestMultiBatchConcurrent(t *testing.T) {
	// Verify no races when multiple goroutines send events.
	// Uses safeNotifier because flush runs from a timer goroutine.
	spy := &safeNotifier{name: "spy"}
	log := &spyLogger{}
	m := NewMulti(log, spy)
	m.SetBatchWindow(100 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Notify(context.Background(), Event{
				Type:          EventUpdateAvailable,
				ContainerName: "container",
				Timestamp:     time.Now(),
			})
		}()
	}
	wg.Wait()

	// Wait for flush.
	time.Sleep(200 * time.Millisecond)

	evts := spy.events()

	// Should have at least one aggregated event (may have been flushed in
	// multiple batches if timer fired mid-send, so just check >= 1).
	if len(evts) < 1 {
		t.Fatalf("got %d events, want >= 1", len(evts))
	}

	// Total container names across all sent events should be 20.
	total := 0
	for _, e := range evts {
		if len(e.ContainerNames) > 0 {
			total += len(e.ContainerNames)
		} else {
			total++ // Single event pass-through.
		}
	}
	if total != 20 {
		t.Errorf("total containers = %d, want 20", total)
	}
}
