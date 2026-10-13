package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Startup recovery waits for a daemon that is still coming up after a
// reboot, backing off between pings, and gives up after the timeout.
func TestWaitForDocker(t *testing.T) {
	var slept []time.Duration
	sleep := func(_ context.Context, d time.Duration) { slept = append(slept, d) }

	calls := 0
	ping := func(context.Context) error {
		calls++
		if calls < 4 {
			return errors.New("connection refused")
		}
		return nil
	}
	if err := waitForDocker(context.Background(), ping, time.Minute, sleep); err != nil {
		t.Fatalf("waitForDocker: %v", err)
	}
	if calls != 4 || len(slept) != 3 || slept[0] != time.Second || slept[2] != 4*time.Second {
		t.Errorf("calls = %d slept = %v, want 4 pings with 1s, 2s, 4s between", calls, slept)
	}

	slept = nil
	never := func(context.Context) error { return errors.New("connection refused") }
	if err := waitForDocker(context.Background(), never, 10*time.Second, sleep); err == nil {
		t.Fatal("expected an error when the daemon never answers")
	}
	var total time.Duration
	for _, d := range slept {
		total += d
	}
	if total > 10*time.Second {
		t.Errorf("waited %v, want no more than the 10s timeout", total)
	}
}
