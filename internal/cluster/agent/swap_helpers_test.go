package agent

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// openTestStore opens a fresh agent database in a temporary directory.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "sentinel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newSwapAgent returns a test agent whose updates go through the journalled
// swap, on a fresh database.
func newSwapAgent(t *testing.T, d DockerAPI) *Agent {
	t.Helper()
	return newSwapAgentOn(t, d, openTestStore(t), clock.Real{})
}

// newSwapAgentOn is newSwapAgent on an existing database and clock, as an
// agent started again on the same host would be.
func newSwapAgentOn(t *testing.T, d DockerAPI, s *store.Store, clk clock.Clock) *Agent {
	t.Helper()
	a := newTestAgent(t.TempDir(), d)
	a.cfg.Store = s
	a.useSwaps(d, s, clk)
	if a.swaps == nil {
		t.Fatal("the test Docker client is not a docker.API")
	}
	return a
}

// stepClock is a clock that moves only with Advance. After fires once
// Advance passes its deadline, or never when held. It is safe for
// concurrent use, so a background goroutine can wait on it.
type stepClock struct {
	mu     sync.Mutex
	now    time.Time
	held   bool
	timers []stepTimer
}

type stepTimer struct {
	at time.Time
	ch chan time.Time
}

func newStepClock(held bool) *stepClock {
	return &stepClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), held: held}
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }

func (c *stepClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	switch {
	case c.held:
	case d <= 0:
		ch <- c.now
	default:
		c.timers = append(c.timers, stepTimer{at: c.now.Add(d), ch: ch})
	}
	return ch
}

func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.timers[:0]
	for _, tm := range c.timers {
		if tm.at.After(c.now) {
			kept = append(kept, tm)
			continue
		}
		tm.ch <- c.now
	}
	c.timers = kept
}
