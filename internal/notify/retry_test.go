package notify

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// failingNotifier fails a configurable number of times then succeeds.
type failingNotifier struct {
	name      string
	failCount int // how many times to fail before succeeding
	calls     atomic.Int32
}

func (f *failingNotifier) Name() string { return f.name }
func (f *failingNotifier) Send(_ context.Context, _ Event) error {
	n := int(f.calls.Add(1))
	if n <= f.failCount {
		return errors.New("temporary failure")
	}
	return nil
}

// alwaysFailNotifier never succeeds.
type alwaysFailNotifier struct {
	name  string
	calls atomic.Int32
}

func (a *alwaysFailNotifier) Name() string { return a.name }
func (a *alwaysFailNotifier) Send(_ context.Context, _ Event) error {
	a.calls.Add(1)
	return errors.New("permanent failure")
}

func TestDispatch_RetrySucceedsOnSecondAttempt(t *testing.T) {
	fn := &failingNotifier{name: "flaky", failCount: 1}
	log := &spyLogger{}
	m := NewMulti(log, fn)
	m.SetRetry(2, 10*time.Millisecond)

	ok := m.Notify(context.Background(), testEvent(EventUpdateSucceeded))
	if !ok {
		t.Fatal("expected Notify to succeed after retry")
	}
	if got := fn.calls.Load(); got != 2 {
		t.Errorf("expected 2 calls, got %d", got)
	}

	// Should have logged a retry error then a success message.
	foundError := false
	for _, c := range log.errorCalls {
		if c.msg == "notification failed, retrying" {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Error("expected retry error log")
	}

	foundSuccess := false
	for _, c := range log.infoCalls {
		if c.msg == "notification succeeded after retry" {
			foundSuccess = true
			break
		}
	}
	if !foundSuccess {
		t.Error("expected success-after-retry info log")
	}
}

func TestDispatch_RetryExhausted(t *testing.T) {
	af := &alwaysFailNotifier{name: "broken"}
	log := &spyLogger{}
	m := NewMulti(log, af)
	m.SetRetry(2, 10*time.Millisecond)

	ok := m.Notify(context.Background(), testEvent(EventUpdateFailed))
	if ok {
		t.Fatal("expected Notify to fail when retries exhausted")
	}
	// initial attempt + 2 retries = 3 total calls
	if got := af.calls.Load(); got != 3 {
		t.Errorf("expected 3 calls (1 + 2 retries), got %d", got)
	}
}

func TestDispatch_NoRetryByDefault(t *testing.T) {
	af := &alwaysFailNotifier{name: "broken"}
	log := &spyLogger{}
	m := NewMulti(log, af)
	// No SetRetry call — retries should be disabled.

	ok := m.Notify(context.Background(), testEvent(EventUpdateFailed))
	if ok {
		t.Fatal("expected Notify to fail")
	}
	if got := af.calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 call (no retries), got %d", got)
	}
}

func TestDispatch_RetryRespectsContextCancellation(t *testing.T) {
	af := &alwaysFailNotifier{name: "broken"}
	log := &spyLogger{}
	m := NewMulti(log, af)
	m.SetRetry(3, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	ok := m.Notify(ctx, testEvent(EventUpdateFailed))
	if ok {
		t.Fatal("expected Notify to fail with cancelled context")
	}
	// With a cancelled context the first attempt runs, but no retries
	// should be attempted because the context is already done.
	if got := af.calls.Load(); got != 1 {
		t.Errorf("expected 1 call (no retries with cancelled ctx), got %d", got)
	}
}
