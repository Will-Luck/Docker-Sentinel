package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/moby/moby/api/types/container"
)

// An update stops the old container with the container's own stop timeout
// when it is longer than Sentinel's, instead of killing it after 30s.
func TestUpdateStopsWithTheContainersStopTimeout(t *testing.T) {
	f, u, old := setupFakeUpdate(t, true, nil)
	long := 120
	old.cfg.StopTimeout = &long

	if err := u.UpdateContainer(context.Background(), "old", "app", ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := f.stopTimeouts["old"]; got != 120 {
		t.Errorf("old container stopped with %ds, want its own 120s", got)
	}
}

// startingHealth reports "starting" for the first n inspects, then healthy.
type startingHealth struct {
	docker.API
	n     int32
	calls atomic.Int32
	hc    *container.HealthConfig
}

func (s *startingHealth) InspectContainer(ctx context.Context, id string) (container.InspectResponse, error) {
	status := container.Healthy
	if s.calls.Add(1) <= s.n {
		status = container.Starting
	}
	return container.InspectResponse{
		ID:     id,
		State:  &container.State{Running: true, Health: &container.Health{Status: status}},
		Config: &container.Config{Healthcheck: s.hc},
	}, nil
}

// advancingClock moves on by the time waited for.
type advancingClock struct{ *mockClock }

func (c advancingClock) After(d time.Duration) <-chan time.Time {
	c.Advance(d)
	return c.mockClock.After(0)
}

// Validation waits as long as the container's healthcheck can take to
// decide (start period, then retries at the interval), not a fixed 60s.
func TestValidationWaitsForTheHealthcheck(t *testing.T) {
	hc := &container.HealthConfig{Interval: 30 * time.Second, Retries: 3, StartPeriod: 2 * time.Minute}
	d := &startingHealth{API: newMockDocker(), n: 100, hc: hc} // healthy after ~200s
	u, clk := newTestUpdater(t, newMockDocker())
	u.docker = d
	u.clock = advancingClock{clk}

	healthy, err := u.validateContainer(context.Background(), "new")
	if err != nil || !healthy {
		t.Errorf("healthy = %v err = %v, want it to wait for the healthcheck", healthy, err)
	}
}
