package main

import (
	"context"
	"fmt"
	"time"
)

// waitForDocker pings the daemon until it answers, backing off from 1s up
// to 8s between pings, and gives up once timeout would be passed. After a
// host reboot the daemon can still be coming up when Sentinel starts, and
// startup recovery needs it.
func waitForDocker(ctx context.Context, ping func(context.Context) error, timeout time.Duration, sleep func(context.Context, time.Duration)) error {
	var waited time.Duration
	delay := time.Second
	for {
		err := ping(ctx)
		if err == nil {
			return nil
		}
		if waited+delay > timeout || ctx.Err() != nil {
			return fmt.Errorf("docker not reachable after %v: %w", waited, err)
		}
		sleep(ctx, delay)
		waited += delay
		delay = min(delay*2, 8*time.Second)
	}
}

// sleepCtx sleeps for d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
