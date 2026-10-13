package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Two rapid saves (on, then off) while the first connect is still running
// must end disconnected, and trigger must never wait for the connect.
func TestApplyWorkerLastSaveWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	desired, connected := true, false
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	first := true

	w := startApplyWorker(ctx, func() {
		mu.Lock()
		want := desired
		mu.Unlock()
		started <- struct{}{}
		if first {
			first = false
			<-release // the slow 10 second connect
		}
		mu.Lock()
		connected = want
		mu.Unlock()
	})

	w.trigger() // save: enabled
	<-started
	mu.Lock()
	desired = false // save: disabled, while the connect is in flight
	mu.Unlock()

	done := make(chan struct{})
	go func() { w.trigger(); w.trigger(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("trigger blocked on the in-flight connect")
	}
	close(release)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-started: // second run began
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			final := connected
			mu.Unlock()
			if final {
				t.Fatal("final state is connected although the last save disabled discovery")
			}
			return
		case <-deadline:
			t.Fatal("worker never ran for the last save")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}
