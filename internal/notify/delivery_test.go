package notify

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestNotifyFalseWhenOnlyRealChannelFails(t *testing.T) {
	log := &spyLogger{}
	m := NewMulti(log, NewLogNotifier(log), &alwaysFailNotifier{name: "gotify"})
	if m.Notify(context.Background(), testEvent(EventUpdateAvailable)) {
		t.Fatal("Notify returned true although the only real channel failed")
	}
}

func TestNotifyTrueWhenRealChannelSucceeds(t *testing.T) {
	log := &spyLogger{}
	m := NewMulti(log, NewLogNotifier(log), &alwaysFailNotifier{name: "a"}, &stubNotifier{name: "b"})
	if !m.Notify(context.Background(), testEvent(EventUpdateAvailable)) {
		t.Fatal("Notify returned false although one real channel succeeded")
	}
}

func TestNotifyTrueWithOnlyLogNotifier(t *testing.T) {
	log := &spyLogger{}
	m := NewMulti(log, NewLogNotifier(log))
	if !m.Notify(context.Background(), testEvent(EventUpdateAvailable)) {
		t.Fatal("Notify returned false with no real channels configured")
	}
}

func TestBatchWindowIsFixedFromFirstEvent(t *testing.T) {
	sink := &stubNotifierSync{}
	m := NewMulti(&spyLogger{}, sink)
	m.SetBatchWindow(150 * time.Millisecond)
	// Events arriving every 60ms must not keep pushing the flush back.
	for i := 0; i < 5; i++ {
		m.Notify(context.Background(), testEvent(EventUpdateFailed))
		time.Sleep(60 * time.Millisecond)
	}
	if sink.count() == 0 {
		t.Fatal("no batch was flushed while events kept arriving inside the window")
	}
	m.Stop()
}

func TestRetryInitialBackoffIsCapped(t *testing.T) {
	log := &spyLogger{}
	fn := &alwaysFailNotifier{name: "x"}
	m := NewMulti(log, fn)
	m.SetRetry(1, 10*time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// With the cap the log line reports at most 30s as next_backoff.
	m.Notify(ctx, testEvent(EventUpdateSucceeded))
	for _, c := range log.errorCalls {
		for i := 0; i+1 < len(c.args); i += 2 {
			if c.args[i] == "next_backoff" && c.args[i+1] != "30s" {
				t.Fatalf("next_backoff = %v, want 30s", c.args[i+1])
			}
		}
	}
}

var syncMu sync.Mutex

type stubNotifierSync struct {
	n int
}

func (s *stubNotifierSync) Name() string { return "sync" }
func (s *stubNotifierSync) Send(_ context.Context, _ Event) error {
	syncMu.Lock()
	defer syncMu.Unlock()
	s.n++
	return nil
}
func (s *stubNotifierSync) count() int {
	syncMu.Lock()
	defer syncMu.Unlock()
	return s.n
}
