package events

import (
	"sync"
	"testing"
	"time"
)

func TestPublishToSubscriber(t *testing.T) {
	bus := New()
	ch, cancel := bus.Subscribe()
	defer cancel()

	evt := SSEEvent{
		Type:          EventContainerUpdate,
		ContainerName: "nginx",
		Message:       "update available",
		Timestamp:     time.Now(),
	}
	bus.Publish(evt)

	select {
	case got := <-ch:
		if got.Type != evt.Type {
			t.Errorf("Type = %q, want %q", got.Type, evt.Type)
		}
		if got.ContainerName != evt.ContainerName {
			t.Errorf("ContainerName = %q, want %q", got.ContainerName, evt.ContainerName)
		}
		if got.Message != evt.Message {
			t.Errorf("Message = %q, want %q", got.Message, evt.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestMultipleSubscribers(t *testing.T) {
	bus := New()
	ch1, cancel1 := bus.Subscribe()
	defer cancel1()
	ch2, cancel2 := bus.Subscribe()
	defer cancel2()

	evt := SSEEvent{
		Type:    EventScanComplete,
		Message: "total=5 updated=2",
	}
	bus.Publish(evt)

	for i, ch := range []<-chan SSEEvent{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Type != evt.Type {
				t.Errorf("subscriber %d: Type = %q, want %q", i, got.Type, evt.Type)
			}
			if got.Message != evt.Message {
				t.Errorf("subscriber %d: Message = %q, want %q", i, got.Message, evt.Message)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for event", i)
		}
	}
}

func TestCancelUnsubscribes(t *testing.T) {
	bus := New()
	ch, cancel := bus.Subscribe()

	// Cancel removes the subscriber and closes the channel.
	cancel()

	// Publish after cancel must not block.
	bus.Publish(SSEEvent{Type: EventQueueChange, Message: "test"})

	// The channel should be closed (receive zero value immediately).
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out -- channel not closed after cancel")
	}

	// Double cancel must not panic.
	cancel()
}

func TestSlowSubscriberDropsEvents(t *testing.T) {
	bus := New()
	ch, cancel := bus.Subscribe()
	defer cancel()

	// Fill the subscriber buffer completely.
	for i := range subscriberBufferSize {
		bus.Publish(SSEEvent{
			Type:      EventContainerUpdate,
			Message:   "fill",
			Timestamp: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
		})
	}

	// This publish should be dropped (not block).
	done := make(chan struct{})
	go func() {
		bus.Publish(SSEEvent{Type: EventContainerUpdate, Message: "overflow"})
		close(done)
	}()

	select {
	case <-done:
		// Good -- publish returned without blocking.
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on full subscriber buffer")
	}

	// Drain and count -- should have exactly subscriberBufferSize events.
	count := 0
	for range subscriberBufferSize {
		select {
		case <-ch:
			count++
		default:
			t.Fatalf("expected %d buffered events, got %d", subscriberBufferSize, count)
		}
	}

	// No more events should be available (the overflow was dropped).
	select {
	case evt := <-ch:
		t.Errorf("unexpected extra event: %+v", evt)
	default:
		// Good -- buffer is empty.
	}
}

func TestConcurrentPublish(t *testing.T) {
	bus := New()
	ch, cancel := bus.Subscribe()
	defer cancel()

	const goroutines = 10
	const perGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			for i := range perGoroutine {
				bus.Publish(SSEEvent{
					Type:      EventContainerUpdate,
					Message:   "concurrent",
					Timestamp: time.Date(2026, 1, 1, 0, 0, id*perGoroutine+i, 0, time.UTC),
				})
			}
		}(g)
	}
	wg.Wait()

	// Drain whatever was received (some may have been dropped due to buffer size).
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	// We should have received at least some events and no more than the total.
	if count == 0 {
		t.Error("no events received from concurrent publishers")
	}
	if count > goroutines*perGoroutine {
		t.Errorf("received %d events, more than published (%d)", count, goroutines*perGoroutine)
	}
}
