// Package events provides a fan-out pub/sub event bus for SSE streaming.
package events

import (
	"sync"
	"time"
)

// EventType identifies the kind of SSE event.
type EventType string

const (
	EventContainerUpdate EventType = "container_update"
	EventContainerState  EventType = "container_state"
	EventQueueChange     EventType = "queue_change"
	EventScanComplete    EventType = "scan_complete"
	EventPolicyChange    EventType = "policy_change"
)

// SSEEvent is a single event published through the bus and streamed to SSE clients.
type SSEEvent struct {
	Type          EventType `json:"type"`
	ContainerName string    `json:"container_name,omitempty"`
	Message       string    `json:"message,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
}

// subscriberBufferSize is the channel buffer for each subscriber.
const subscriberBufferSize = 64

// Bus is a fan-out pub/sub event bus. Subscribers receive all events published
// after they subscribe. Slow subscribers that fall behind have events dropped
// rather than blocking publishers.
type Bus struct {
	mu   sync.RWMutex
	subs map[uint64]chan SSEEvent
	next uint64
}

// New creates a ready-to-use Bus.
func New() *Bus {
	return &Bus{
		subs: make(map[uint64]chan SSEEvent),
	}
}

// Publish sends an event to all current subscribers. If a subscriber's buffer
// is full, the event is dropped for that subscriber (non-blocking).
func (b *Bus) Publish(evt SSEEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.subs {
		select {
		case ch <- evt:
		default:
			// Subscriber buffer full -- drop the event rather than blocking.
		}
	}
}

// Subscribe returns a channel that receives all future events and a cancel
// function that unsubscribes and closes the channel. The caller must invoke
// cancel when done to avoid resource leaks.
func (b *Bus) Subscribe() (<-chan SSEEvent, func()) {
	ch := make(chan SSEEvent, subscriberBufferSize)

	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(ch)
		}
	}

	return ch, cancel
}
